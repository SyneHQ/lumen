package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
	"github.com/SyneHQ/lumen/gen/lumen/v1/lumenv1connect"
	"github.com/SyneHQ/lumen/internal/pg"
)

func TestOriginAllowed(t *testing.T) {
	allowed := []string{"shop.example", "*.example.com"}
	for origin, want := range map[string]bool{
		"https://shop.example":          true,  // exact
		"http://shop.example:8080":      true,  // port ignored
		"https://SHOP.example":          true,  // case-insensitive
		"https://www.example.com":       true,  // wildcard subdomain
		"https://a.b.example.com":       true,  // deeper subdomain
		"https://example.com":           false, // wildcard does not match apex
		"https://badexample.com":        false,
		"https://evil.com":              false,
		"https://shop.example.evil.com": false,
		"null":                          false,
		"":                              false, // missing Origin
	} {
		if got := OriginAllowed(origin, allowed); got != want {
			t.Errorf("OriginAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestNormalizeAllowedOrigins(t *testing.T) {
	if k, o, err := NormalizeAllowedOrigins("", nil); err != nil || k != KindServer || o != nil {
		t.Fatalf("empty kind: %q %v %v", k, o, err)
	}
	if k, o, err := NormalizeAllowedOrigins("web", []string{" Example.COM ", "*.example.com"}); err != nil || k != KindWeb || o[0] != "example.com" || o[1] != "*.example.com" {
		t.Fatalf("web: %q %v %v", k, o, err)
	}
	for _, bad := range [][]string{nil, {"https://example.com"}, {"example.com:443"}, {"example.com/x"}, {"*.com"}, {"*"}, {"a.*.com"}, {"-x.com"}, {""}} {
		if _, _, err := NormalizeAllowedOrigins("web", bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, _, err := NormalizeAllowedOrigins("server", []string{"example.com"}); err == nil {
		t.Error("server key accepted origins")
	}
	if _, _, err := NormalizeAllowedOrigins("mobile", nil); err == nil {
		t.Error("unknown kind accepted")
	}
}

type seenCtx struct {
	lumenv1connect.UnimplementedIngestServiceHandler
	ctx context.Context
}

func (s *seenCtx) Track(ctx context.Context, _ *connect.Request[lumenv1.TrackRequest]) (*connect.Response[lumenv1.TrackResponse], error) {
	s.ctx = ctx
	return connect.NewResponse(&lumenv1.TrackResponse{Success: true}), nil
}

func (s *seenCtx) Identify(ctx context.Context, _ *connect.Request[lumenv1.IdentifyRequest]) (*connect.Response[lumenv1.IdentifyResponse], error) {
	s.ctx = ctx
	return connect.NewResponse(&lumenv1.IdentifyResponse{Success: true}), nil
}

func TestWebKeyInterceptor(t *testing.T) {
	a, _ := NewAuthenticator(nil)
	saltCalls := 0
	a.lookup = func(_ context.Context, h []byte) (pg.KeyAuth, error) {
		switch string(h) {
		case string(HashKey("web")):
			return pg.KeyAuth{TeamID: "t1", StoreIP: true, Kind: KindWeb, AllowedOrigins: []string{"*.example.com"}}, nil
		case string(HashKey("srv")):
			return pg.KeyAuth{TeamID: "t1", StoreIP: true, Kind: KindServer}, nil
		}
		return pg.KeyAuth{}, errors.New("nope")
	}
	a.salt = func(context.Context, time.Time) ([]byte, error) { saltCalls++; return []byte("salt"), nil }

	svc := &seenCtx{}
	path, h := lumenv1connect.NewIngestServiceHandler(svc, connect.WithInterceptors(a.NewAuthInterceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, h)

	call := func(method, key, origin string) int {
		svc.ctx = nil
		body := `{}`
		if method == "Identify" {
			body = `{"anonId":"a","userId":"u"}`
		}
		req := httptest.NewRequest("POST", "/lumen.v1.IngestService/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	if c := call("Track", "web", "https://www.example.com"); c != 200 {
		t.Fatalf("allowed origin: %d", c)
	}
	wk, ok := GetWebKey(svc.ctx)
	if !ok || string(wk.Salt) != "salt" || wk.ID == "" || GetStoreIP(svc.ctx) || GetTeamID(svc.ctx) != "t1" {
		t.Fatalf("web ctx wrong: %+v %v storeIP=%v", wk, ok, GetStoreIP(svc.ctx))
	}
	if c := call("Track", "web", ""); c != 403 {
		t.Fatalf("missing origin: %d", c)
	}
	if c := call("Track", "web", "https://example.com"); c != 403 {
		t.Fatalf("apex origin: %d", c)
	}
	if c := call("Track", "web", "https://evil.com"); c != 403 {
		t.Fatalf("other origin: %d", c)
	}
	if c := call("Identify", "web", "https://www.example.com"); c != 403 || svc.ctx != nil {
		t.Fatalf("identify with web key: %d", c)
	}
	if c := call("Track", "web", "https://a.example.com"); c != 200 || saltCalls != 2 {
		t.Fatalf("salt not cached per day: %d calls", saltCalls)
	}

	// Server keys are unchanged: no Origin needed, Identify allowed, store_ip kept.
	if c := call("Identify", "srv", ""); c != 200 {
		t.Fatalf("server identify: %d", c)
	}
	if _, ok := GetWebKey(svc.ctx); ok || !GetStoreIP(svc.ctx) {
		t.Fatal("server key got web treatment")
	}
}

func TestWebSaltCachedPerDay(t *testing.T) {
	a, _ := NewAuthenticator(nil)
	n := 0
	a.salt = func(_ context.Context, d time.Time) ([]byte, error) { n++; return []byte(d.Format("0102")), nil }
	day1 := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	s1, _ := a.webSalts(context.Background(), day1)
	s1b, _ := a.webSalts(context.Background(), day1.Add(30*time.Minute))
	s2, _ := a.webSalts(context.Background(), day1.Add(2*time.Hour))
	if string(s1[0]) != string(s1b[0]) || string(s1[0]) == string(s2[0]) || n != 4 {
		t.Fatalf("salts %q %q %q after %d fetches", s1, s1b, s2, n)
	}
	if string(s2[1]) != string(s1[0]) || string(s1[1]) != "1231" {
		t.Fatalf("yesterday's salt wrong: %q then %q", s1, s2)
	}
}

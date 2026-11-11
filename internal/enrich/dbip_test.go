package enrich

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	geoip2 "github.com/oschwald/geoip2-golang/v2"
)

// mmdb builds a minimal IPv6 MaxMind DB whose every address resolves to country.
func mmdb(country string) []byte {
	str := func(s string) []byte { return append([]byte{0x40 | byte(len(s))}, s...) }
	u16 := func(v uint16) []byte { return []byte{0xA2, byte(v >> 8), byte(v)} }
	u32 := func(v uint32) []byte { b := []byte{0xC4, 0, 0, 0, 0}; binary.BigEndian.PutUint32(b[1:], v); return b }
	cat := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	var b []byte
	b = append(b, 0, 0, 17, 0, 0, 17) // one node, both 24-bit records -> data offset 0 (1 node + 16)
	b = append(b, make([]byte, 16)...)
	b = append(b, cat([]byte{0xE1}, str("country"), []byte{0xE1}, str("iso_code"), str(country))...)
	b = append(b, "\xAB\xCD\xEFMaxMind.com"...)
	b = append(b, cat([]byte{0xE9},
		str("node_count"), u32(1),
		str("record_size"), u16(24),
		str("ip_version"), u16(6),
		str("database_type"), str("DBIP-City-Lite"),
		str("languages"), []byte{0x00, 0x04},
		str("binary_format_major_version"), u16(2),
		str("binary_format_minor_version"), u16(0),
		str("build_epoch"), []byte{0x01, 0x02, 0x01},
		str("description"), []byte{0xE0},
	)...)
	return b
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// fakeDBIP serves files by name; anything missing is a 404.
func fakeDBIP(t *testing.T, files map[string][]byte) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mu.Lock()
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/free/")]
		mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newUpdater(t *testing.T, srv *httptest.Server, dir string, now time.Time) (*Enricher, *dbipUpdater) {
	e, err := NewEnricher("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e, &dbipUpdater{e: e, dir: dir, baseURL: srv.URL + "/free/", client: srv.Client(),
		now: func() time.Time { return now }, every: time.Hour, backoff: 10 * time.Millisecond}
}

func country(e *Enricher) string { return e.Enrich("", "8.8.8.8", "", "", false).Country }

var nov = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

func TestDBIPCurrentMonth(t *testing.T) {
	srv, _ := fakeDBIP(t, map[string][]byte{"dbip-city-lite-2026-11.mmdb.gz": gz(mmdb("US"))})
	dir := t.TempDir()
	e, u := newUpdater(t, srv, dir, nov)
	if err := u.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := country(e); got != "US" {
		t.Fatalf("country = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "dbip-city-lite-2026-11.mmdb")); err != nil {
		t.Fatal(err)
	}
}

func TestDBIPPreviousMonthFallback(t *testing.T) {
	srv, hits := fakeDBIP(t, map[string][]byte{"dbip-city-lite-2026-10.mmdb.gz": gz(mmdb("DE"))})
	e, u := newUpdater(t, srv, t.TempDir(), nov)
	if err := u.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := country(e); got != "DE" || u.loaded != "2026-10" {
		t.Fatalf("country = %q loaded = %q", got, u.loaded)
	}
	// Next check retries the current month only; prev stays loaded.
	hits.Store(0)
	if err := u.refresh(t.Context()); err != nil || hits.Load() != 1 {
		t.Fatalf("err=%v hits=%d", err, hits.Load())
	}
}

func TestDBIPCorruptKeepsOldReader(t *testing.T) {
	files := map[string][]byte{"dbip-city-lite-2026-10.mmdb.gz": gz(mmdb("DE"))}
	srv, _ := fakeDBIP(t, files)
	dir := t.TempDir()
	e, u := newUpdater(t, srv, dir, nov)
	if err := u.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	files["dbip-city-lite-2026-11.mmdb.gz"] = gz([]byte("not an mmdb"))
	if err := u.refresh(t.Context()); err == nil {
		t.Fatal("corrupt file accepted")
	}
	files["dbip-city-lite-2026-11.mmdb.gz"] = []byte("not gzip")
	if err := u.refresh(t.Context()); err == nil {
		t.Fatal("non-gzip accepted")
	}
	if got := country(e); got != "DE" {
		t.Fatalf("old reader lost: %q", got)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 1 {
		t.Fatalf("temp files left behind: %v", left)
	}
}

func TestDBIPCachedFileReused(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "dbip-city-lite-2026-11.mmdb"), mmdb("FR"), 0o644)
	srv, hits := fakeDBIP(t, nil)
	e, u := newUpdater(t, srv, dir, nov)
	if err := u.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := country(e); got != "FR" || hits.Load() != 0 {
		t.Fatalf("country=%q hits=%d", got, hits.Load())
	}
}

func TestDBIPSwapDuringLookups(t *testing.T) {
	srv, _ := fakeDBIP(t, nil)
	e, u := newUpdater(t, srv, t.TempDir(), nov)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					country(e)
				}
			}
		}()
	}
	for i, c := range []string{"US", "DE", "FR", "JP", "US", "DE"} {
		p := filepath.Join(u.dir, c+strconv.Itoa(i)+".mmdb")
		os.WriteFile(p, mmdb(c), 0o644)
		r, err := geoip2.Open(p)
		if err != nil {
			t.Fatal(i, err)
		}
		e.swapGeo(r)
	}
	close(stop)
	wg.Wait()
	if got := country(e); got != "DE" {
		t.Fatalf("country = %q", got)
	}
}

func TestDBIPPathWinsAndStartDoesNotBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maxmind.mmdb")
	os.WriteFile(p, mmdb("CA"), 0o644)
	e, err := NewEnricher(p)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.StartDBIPLite(t.TempDir()) {
		t.Fatal("dbip started despite GEOIP_DB_PATH")
	}
	if got := country(e); got != "CA" {
		t.Fatalf("country = %q", got)
	}

	// A server that never answers must not block startup.
	hang := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(hang)
	e2, u := newUpdater(t, srv, t.TempDir(), nov)
	start := time.Now()
	if !e2.startDBIP(u) {
		t.Fatal("not started")
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("start blocked %s", d)
	}
	if got := country(e2); got != "" {
		t.Fatalf("country before load = %q", got)
	}
	e2.Close() // cancels the hung download
}

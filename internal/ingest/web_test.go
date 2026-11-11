package ingest

import (
	"net"
	"testing"
	"time"

	"github.com/SyneHQ/lumen/internal/ch"
)

func TestWebAnonID(t *testing.T) {
	a := webAnonID([]byte("s1"), "team", "example.com", "203.0.113.5", "UA")
	if a != webAnonID([]byte("s1"), "team", "example.com", "203.0.113.5", "UA") {
		t.Fatal("not deterministic")
	}
	if len(a) != 35 || a[:3] != "w1_" {
		t.Fatalf("bad shape %q", a)
	}
	for _, other := range []string{
		webAnonID([]byte("s2"), "team", "example.com", "203.0.113.5", "UA"), // next day's salt
		webAnonID([]byte("s1"), "team", "example.com", "203.0.113.6", "UA"), // other IP
		webAnonID([]byte("s1"), "team2", "example.com", "203.0.113.5", "UA"),
		webAnonID([]byte("s1"), "team", "other.com", "203.0.113.5", "UA"),
		webAnonID([]byte("s1"), "team", "example.com", "203.0.113.5", "UA2"),
	} {
		if other == a {
			t.Fatal("collision across inputs")
		}
	}
}

func TestApplyWebIdentityIgnoresClientIDsAndDropsIP(t *testing.T) {
	s := &Service{}
	now := time.Now()
	mk := func(url string) ch.EventRecord {
		return ch.EventRecord{TeamID: "t", URL: url, AnonID: "client-anon", UserID: "client-user", SessionID: "client-sess", IP: net.ParseIP("203.0.113.5")}
	}
	r1, r2 := mk("https://Example.com:8443/a?x=1"), mk("https://example.com/b")
	s.applyWebIdentity(&r1, []byte("salt"), "203.0.113.5", "UA", now)
	s.applyWebIdentity(&r2, []byte("salt"), "203.0.113.5", "UA", now.Add(time.Minute))
	if r1.AnonID != webAnonID([]byte("salt"), "t", "example.com", "203.0.113.5", "UA") || r1.AnonID != r2.AnonID {
		t.Fatalf("anon ids %q %q", r1.AnonID, r2.AnonID)
	}
	if r1.UserID != "" || r1.IP != nil || r1.SessionID == "client-sess" || r1.SessionID != r2.SessionID {
		t.Fatalf("record kept client data: %+v", r1)
	}
}

func TestWebSessionRotatesAfterIdle(t *testing.T) {
	s := &Service{}
	t0 := time.Now()
	a := s.webSessionID("v", t0)
	if b := s.webSessionID("v", t0.Add(29*time.Minute)); b != a {
		t.Fatal("rotated within 30 min")
	}
	if c := s.webSessionID("v", t0.Add(58*time.Minute)); c != a {
		t.Fatal("activity should extend the session")
	}
	if d := s.webSessionID("v", t0.Add(89*time.Minute)); d == a {
		t.Fatal("did not rotate after 31 min idle")
	}
	if s.webSessionID("other", t0) == a {
		t.Fatal("visitors share a session")
	}
}

func TestWebRateLimit(t *testing.T) {
	s := &Service{}
	now := time.Now()
	if !s.allowWeb("k", "1.1.1.1", 100, now) {
		t.Fatal("burst of 100 refused")
	}
	if s.allowWeb("k", "1.1.1.1", 1, now) {
		t.Fatal("event over burst allowed")
	}
	if !s.allowWeb("k", "2.2.2.2", 1, now) || !s.allowWeb("k2", "1.1.1.1", 1, now) {
		t.Fatal("bucket not scoped per (key, ip)")
	}
	if !s.allowWeb("k", "1.1.1.1", 50, now.Add(time.Second)) {
		t.Fatal("did not refill at 50/s")
	}
}

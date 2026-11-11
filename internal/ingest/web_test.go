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
	s.applyWebIdentity(&r1, []byte("salt"), nil, "203.0.113.5", "UA", now)
	s.applyWebIdentity(&r2, []byte("salt"), nil, "203.0.113.5", "UA", now.Add(time.Minute))
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
	a := s.webSessionID("v", "", t0)
	if b := s.webSessionID("v", "", t0.Add(29*time.Minute)); b != a {
		t.Fatal("rotated within 30 min")
	}
	if c := s.webSessionID("v", "", t0.Add(58*time.Minute)); c != a {
		t.Fatal("activity should extend the session")
	}
	if d := s.webSessionID("v", "", t0.Add(89*time.Minute)); d == a {
		t.Fatal("did not rotate after 31 min idle")
	}
	if s.webSessionID("other", "", t0) == a {
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

func TestWebSessionSurvivesMidnightSaltChange(t *testing.T) {
	s := &Service{}
	d1, d2 := []byte("day1"), []byte("day2")
	ev := func(salt, prev []byte, at time.Time) ch.EventRecord {
		r := ch.EventRecord{TeamID: "t", URL: "https://example.com/"}
		s.applyWebIdentity(&r, salt, prev, "203.0.113.5", "UA", at)
		return r
	}
	midnight := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	before := ev(d1, nil, midnight.Add(-time.Minute)) // 23:59
	after := ev(d2, d1, midnight.Add(5*time.Minute))  // 00:05
	if before.AnonID == after.AnonID || before.SessionID != after.SessionID {
		t.Fatalf("visit split at midnight: %+v vs %+v", before, after)
	}
	if again := ev(d2, d1, midnight.Add(10*time.Minute)); again.SessionID != after.SessionID || again.AnonID != after.AnonID {
		t.Fatal("session not stored under today's anon_id")
	}

	// A different visitor idle across midnight: 23:59 then 00:45 is a new session.
	s = &Service{}
	b := ev(d1, nil, midnight.Add(-time.Minute))
	late := ev(d2, d1, midnight.Add(45*time.Minute))
	if late.SessionID == b.SessionID {
		t.Fatal("idle visit kept its session across midnight")
	}
}

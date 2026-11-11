package ingest

import (
	"fmt"
	"net"
	"testing"
	"time"

	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
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
	a := s.webSessionID("t", "v", "", t0)
	if b := s.webSessionID("t", "v", "", t0.Add(29*time.Minute)); b != a {
		t.Fatal("rotated within 30 min")
	}
	if c := s.webSessionID("t", "v", "", t0.Add(58*time.Minute)); c != a {
		t.Fatal("activity should extend the session")
	}
	if d := s.webSessionID("t", "v", "", t0.Add(89*time.Minute)); d == a {
		t.Fatal("did not rotate after 31 min idle")
	}
	if s.webSessionID("t", "other", "", t0) == a {
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

func TestCheckWebEventNames(t *testing.T) {
	ok := []*lumenv1.Event{{Name: "$pageview"}, {Name: "signup"}, nil}
	if err := checkWebEventNames(ok); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"$identify", "$autocapture", "$"} {
		if checkWebEventNames(append(ok, &lumenv1.Event{Name: bad})) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestMinimiseURL(t *testing.T) {
	for _, c := range []struct {
		in   string
		page bool
		want string
	}{
		{"https://ex.com/a/b?token=secret#access_token=x", true, "https://ex.com/a/b"},
		{"https://ex.com/p?utm_content=c&x=1&utm_source=s&utm_medium=m&utm_term=t&utm_campaign=k", true, "https://ex.com/p?utm_source=s&utm_medium=m&utm_campaign=k&utm_term=t&utm_content=c"},
		{"https://ex.com/#/settings/profile", true, "https://ex.com/#/settings/profile"},
		{"https://ex.com/#section", true, "https://ex.com/"},
		{"https://ex.com:8443/a%2Fb", true, "https://ex.com:8443/a%2Fb"},
		{"https://user:pw@ex.com/a", true, "https://ex.com/a"},
		{"https://google.com/search?q=private&utm_source=x#/y", false, "https://google.com/search"},
		{"", false, ""},
		{"not a url", true, ""},
	} {
		if got := minimiseURL(c.in, c.page); got != c.want {
			t.Errorf("minimiseURL(%q, %v) = %q, want %q", c.in, c.page, got, c.want)
		}
	}
}

func TestWebSessionsArePerTeam(t *testing.T) {
	s := &Service{}
	now := time.Now()
	a := s.webSessionID("t1", "v", "", now)
	if b := s.webSessionID("t2", "v", "", now); b == a {
		t.Fatal("teams share sessions")
	}
	if s.webSessionID("t1", "v", "", now.Add(time.Minute)) != a {
		t.Fatal("team session lost")
	}
	// A team flooding its own cache evicts only its own visitors.
	for i := 0; i < webTeamCache+10; i++ {
		s.webSessionID("busy", fmt.Sprint(i), "", now)
	}
	if s.webSessionID("t1", "v", "", now.Add(2*time.Minute)) != a {
		t.Fatal("busy team evicted another team's session")
	}
}

func TestRateLimitGroupsIPv6By64(t *testing.T) {
	if rateKeyIP("2001:db8:1:2:aaaa::1") != rateKeyIP("2001:db8:1:2:bbbb::9") {
		t.Fatal("same /64 split")
	}
	if rateKeyIP("2001:db8:1:2::1") == rateKeyIP("2001:db8:1:3::1") {
		t.Fatal("different /64 merged")
	}
	if rateKeyIP("203.0.113.5") != "203.0.113.5" || rateKeyIP("::ffff:203.0.113.5") != "::ffff:203.0.113.5" {
		t.Fatal("IPv4 changed")
	}
	s := &Service{}
	now := time.Now()
	if !s.allowWeb("k", "2001:db8::1", 100, now) || s.allowWeb("k", "2001:db8::2", 1, now) {
		t.Fatal("IPv6 neighbours in one /64 got separate buckets")
	}
}

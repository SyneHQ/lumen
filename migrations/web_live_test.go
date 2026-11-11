package migrations_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/SyneHQ/lumen/internal/ch"
	"github.com/SyneHQ/lumen/migrations"
	"github.com/google/uuid"
)

// LUMEN_WEB_CH_LIVE_DSN points at a disposable ClickHouse, e.g.
// clickhouse://default:pw@127.0.0.1:19001/lumen. The test owns a fresh database.
func TestWebAnalyticsLive(t *testing.T) {
	dsn := os.Getenv("LUMEN_WEB_CH_LIVE_DSN")
	if dsn == "" {
		t.Skip("Set LUMEN_WEB_CH_LIVE_DSN to a disposable ClickHouse")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	pw, _ := u.User.Password()
	open := func(user, pass, db string) clickhouse.Conn {
		t.Helper()
		c, err := clickhouse.Open(&clickhouse.Options{Addr: []string{u.Host}, Auth: clickhouse.Auth{Username: user, Password: pass, Database: db}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	db := "web_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	admin := open(u.User.Username(), pw, "default")
	defer admin.Close()
	if err = admin.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+db)

	u.Path = "/" + db
	client, err := ch.NewClientContext(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	files, _ := migrations.FS.ReadDir("ch")
	for repeat := 0; repeat < 2; repeat++ {
		for _, f := range files {
			raw, _ := migrations.FS.ReadFile("ch/" + f.Name())
			if err = client.RunMigrations(ctx, string(raw)); err != nil {
				t.Fatalf("run %d %s: %v", repeat, f.Name(), err)
			}
		}
	}
	userA, userB := "lumen_t_webA_"+db[4:], "lumen_t_webB_"+db[4:]
	for team, user := range map[string]string{"team_a": userA, "team_b": userB} {
		if err = client.ProvisionTenant(ctx, team, user, "pw_"+user); err != nil {
			t.Fatal(err)
		}
		defer client.DeprovisionTenant(context.Background(), team, user)
	}

	// team, offset s, name, session, host, path, referrer_host, utm_source, utm_medium, device
	type ev struct {
		team, name, sess, host, path, ref, us, um, dev string
		at                                             int
	}
	fixtures := []ev{
		{"team_a", "$pageview", "s1", "example.com", "/", "google.com", "", "", "desktop", 0},
		{"team_a", "$pageview", "s1", "example.com", "/pricing", "www.ChatGPT.com", "", "", "desktop", 10},
		{"team_a", "$pageview", "s1", "example.com", "/docs", "", "", "", "desktop", 20},
		{"team_a", "$pageview", "s1", "example.com", "/docs/2", "example.com", "", "", "desktop", 30},
		{"team_a", "$pageview", "s1", "example.com", "/ad", "google.com", "google", "cpc", "desktop", 40},
		{"team_a", "$pageview", "s1", "example.com", "/blog", "news.example.org", "", "", "desktop", 45},
		{"team_a", "signup", "s1", "example.com", "/blog", "", "", "", "desktop", 50},
		{"team_a", "$pageview", "s2", "example.com", "/", "google.com", "", "", "bot", 5},
		{"team_b", "$pageview", "s3", "b.io", "/", "google.com", "", "", "mobile", 0},
	}
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var vals []string
	for _, e := range fixtures {
		vals = append(vals, fmt.Sprintf("('%s', toDateTime64('%s', 3, 'UTC'), '%s', generateUUIDv7(), 'anon_%s', '%s', 'https://%s%s', '%s', '%s', '%s', '%s', '%s', '%s', 'DE')",
			e.team, base.Add(time.Duration(e.at)*time.Second).Format("2006-01-02 15:04:05"), e.name, e.sess, e.sess, e.host, e.path, e.path, e.host, e.ref, e.us, e.um, e.dev))
	}
	if err = admin.Exec(ctx, "INSERT INTO "+db+".events (team_id, ts, name, event_id, anon_id, session_id, url, path, host, referrer_host, utm_source, utm_medium, device_type, country) VALUES "+strings.Join(vals, ", ")); err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}
	rows, err := admin.Query(ctx, "SELECT team_id, path, referrer_host, source, channel FROM "+db+".web_pageviews ORDER BY team_id, ts")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var team, path, ref, src, chn string
		if err = rows.Scan(&team, &path, &ref, &src, &chn); err != nil {
			t.Fatal(err)
		}
		t.Logf("web_pageviews %s %-8s ref=%-17q source=%-15q channel=%q", team, path, ref, src, chn)
		got[team+path] = src + "|" + chn
	}
	want := map[string]string{
		"team_a/":        "Google|Organic Search",
		"team_a/pricing": "ChatGPT|AI Assistants",
		"team_a/docs":    "Direct / None|Direct",
		"team_a/docs/2":  "Direct / None|Direct",
		"team_a/ad":      "google|Paid Search",
		"team_a/blog":    "news.example.org|Referral",
		"team_b/":        "Google|Organic Search",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("web_pageviews:\n got %v\nwant %v", got, want)
	}

	var (
		sess, host, entry, exit string
		pv                      uint64
		dur                     int64
	)
	if err = admin.QueryRow(ctx, "SELECT session_id, host, pageview_count, duration_s, entry_path, exit_path FROM "+db+".web_sessions WHERE team_id = 'team_a' AND session_id = 's1'").Scan(&sess, &host, &pv, &dur, &entry, &exit); err != nil {
		t.Fatal(err)
	}
	t.Logf("web_sessions %s host=%s pageviews=%d duration_s=%d entry=%s exit=%s", sess, host, pv, dur, entry, exit)
	if host != "example.com" || pv != 6 || dur != 50 || entry != "/" || exit != "/blog" {
		t.Fatal("web_sessions s1 wrong")
	}

	reader := open(userA, "pw_"+userA, db)
	defer reader.Close()
	for _, q := range []string{
		"SELECT countIf(team_id = 'team_b'), count() FROM " + db + ".web_pageviews",
		"SELECT countIf(team_id = 'team_b'), count() FROM " + db + ".web_sessions",
	} {
		var other, all uint64
		if err = reader.QueryRow(ctx, q).Scan(&other, &all); err != nil {
			t.Fatal(err)
		}
		t.Logf("team_a user: %s -> team_b rows=%d total=%d", q, other, all)
		if other != 0 || all == 0 {
			t.Fatal("row policy isolation broken")
		}
	}
}

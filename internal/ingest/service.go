// Package ingest implements the IngestService Connect handlers for event and identity ingestion.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/time/rate"

	"github.com/SyneHQ/lumen/ee"
	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
	"github.com/SyneHQ/lumen/gen/lumen/v1/lumenv1connect"
	"github.com/SyneHQ/lumen/internal/auth"
	"github.com/SyneHQ/lumen/internal/ch"
	"github.com/SyneHQ/lumen/internal/enrich"
)

// Service implements the lumenv1connect.IngestServiceHandler interface.
type Service struct {
	lumenv1connect.UnimplementedIngestServiceHandler
	chClient *ch.Client
	enricher *enrich.Enricher
	hooks    ee.Hooks

	// trustedHops is how many proxies append to X-Forwarded-For; 0 ignores it.
	trustedHops int

	webOnce  sync.Once
	webMu    sync.Mutex                                         // guards the get-then-set on sessions
	sessions *lru.Cache[string, *lru.Cache[string, webSession]] // team → visitor sessions
	limiters *lru.Cache[string, *rate.Limiter]
}

type webSession struct {
	id       string
	lastSeen time.Time
}

const (
	webSessionIdle = 30 * time.Minute
	webCacheSize   = 100_000 // rate limiter buckets
	webTeams       = 5_000   // team session caches kept; least recently used team is dropped
	webTeamCache   = 20_000  // sessions per team
	webRate        = 50      // events per second per (key, client IP)
	webBurst       = 100     // events
)

// NewService creates an Ingest Service handler.
//
// hooks supplies quota and metering extension points; the zero value is the
// open-source no-op set (unlimited quota, metering discarded).
func NewService(chClient *ch.Client, enricher *enrich.Enricher, hooks ee.Hooks) *Service {
	return &Service{
		chClient:    chClient,
		enricher:    enricher,
		hooks:       hooks.Normalize(),
		trustedHops: 1,
	}
}

// SetTrustedProxyHops sets how many reverse proxies append to X-Forwarded-For
// (config TRUSTED_PROXY_HOPS). 0 makes the peer address the client IP.
func (s *Service) SetTrustedProxyHops(n int) { s.trustedHops = n }

// Track handles incoming event batch ingestion over Connect/gRPC/HTTP.
func (s *Service) Track(ctx context.Context, req *connect.Request[lumenv1.TrackRequest]) (*connect.Response[lumenv1.TrackResponse], error) {
	teamID := auth.GetTeamID(ctx)
	storeIP := auth.GetStoreIP(ctx)
	if teamID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized team context"))
	}

	batch := req.Msg
	if batch == nil || len(batch.Events) == 0 {
		return connect.NewResponse(&lumenv1.TrackResponse{Success: true, ProcessedCount: 0}), nil
	}

	// Boundary Validation (§5.4): Max batch size 1000
	if len(batch.Events) > 1000 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("batch size exceeds max limit of 1000 events"))
	}

	// Commercial quota enforcement. No-op (always allows) in the open build.
	if err := s.hooks.QuotaEnforcer.CheckQuota(ctx, teamID, len(batch.Events)); err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}

	batchContext := batch.Context
	if batchContext == nil {
		batchContext = &lumenv1.Context{}
	}

	clientIP := resolveClientIP(req.Header(), req.Peer().Addr, s.trustedHops)

	webKey, isWeb := auth.GetWebKey(ctx)
	if isWeb {
		if err := checkWebEventNames(batch.Events); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	if isWeb && !s.allowWeb(webKey.ID, clientIP, len(batch.Events), time.Now()) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded"))
	}

	var records []ch.EventRecord
	now := time.Now().UTC()
	minAllowedTime := now.AddDate(-1, 0, 0) // 1 year ago clamp
	maxAllowedTime := now.Add(24 * time.Hour)

	// Process and enrich each event in the batch
	for _, ev := range batch.Events {
		if ev == nil || ev.Name == "" {
			continue
		}

		// Resolve context (event overrides take precedence over batch context)
		activeCtx := batchContext
		if ev.Overrides != nil {
			activeCtx = ev.Overrides
		}

		// Sanity check client timestamp (§5.4)
		eventTime := time.UnixMilli(ev.TsUnixMs).UTC()
		if eventTime.After(maxAllowedTime) {
			continue // Reject futuristic timestamp (> 24h future)
		}
		if eventTime.Before(minAllowedTime) {
			eventTime = minAllowedTime // Clamp (> 1y past)
		}

		// Parse or generate UUIDv7 idempotency key
		eventUUID, err := uuid.Parse(ev.EventId)
		if err != nil {
			eventUUID, _ = uuid.NewV7()
		}

		userAgent := activeCtx.UserAgent
		pageURL, referrer := activeCtx.Url, activeCtx.Referrer
		if isWeb {
			pageURL, referrer = minimiseURL(pageURL, true), minimiseURL(referrer, false)
			// Browsers send their real UA as a header; prefer it over the payload.
			if h := req.Header().Get("User-Agent"); h != "" {
				userAgent = h
			}
			storeIP = false
		}

		// Run server-side User-Agent, GeoIP, and URL enrichment (§5.3)
		enriched := s.enricher.Enrich(userAgent, clientIP, pageURL, referrer, storeIP)

		// Respect explicit OS/Device reported directly by native SDKs
		finalOS := enriched.OS
		if activeCtx.Os != "" {
			finalOS = activeCtx.Os
		}
		finalOSVersion := enriched.OSVersion
		if activeCtx.OsVersion != "" {
			finalOSVersion = activeCtx.OsVersion
		}
		finalDeviceType := enriched.DeviceType

		record := ch.EventRecord{
			TeamID:         teamID,
			TS:             eventTime,
			Name:           ev.Name,
			EventID:        eventUUID,
			AnonID:         activeCtx.AnonId,
			UserID:         activeCtx.UserId,
			SessionID:      activeCtx.SessionId,
			SDK:            activeCtx.Sdk,
			SDKVersion:     activeCtx.SdkVersion,
			AppVersion:     activeCtx.AppVersion,
			OS:             finalOS,
			OSVersion:      finalOSVersion,
			DeviceType:     finalDeviceType,
			DeviceModel:    activeCtx.DeviceModel,
			Manufacturer:   activeCtx.Manufacturer,
			Browser:        enriched.Browser,
			BrowserVersion: enriched.BrowserVersion,
			ScreenW:        uint16(activeCtx.ScreenW),
			ScreenH:        uint16(activeCtx.ScreenH),
			ViewportW:      uint16(activeCtx.ViewportW),
			ViewportH:      uint16(activeCtx.ViewportH),
			Locale:         activeCtx.Locale,
			Timezone:       activeCtx.Timezone,
			URL:            pageURL,
			Path:           enriched.Path,
			Host:           enriched.Host,
			Referrer:       referrer,
			ReferrerHost:   enriched.ReferrerHost,
			UTMSource:      enriched.UTMSource,
			UTMMedium:      enriched.UTMMedium,
			UTMCampaign:    enriched.UTMCampaign,
			UTMTerm:        enriched.UTMTerm,
			UTMContent:     enriched.UTMContent,
			Country:        enriched.Country,
			Region:         enriched.Region,
			City:           enriched.City,
			IP:             enriched.ParsedIP,
			Props:          string(ev.PropsJson),
		}

		if isWeb {
			s.applyWebIdentity(&record, webKey.Salt, webKey.PrevSalt, clientIP, userAgent, time.Now())
		}

		records = append(records, record)
	}

	if len(records) == 0 {
		return connect.NewResponse(&lumenv1.TrackResponse{Success: true, ProcessedCount: 0}), nil
	}

	// Compute deduplication token from batch contents
	dedupToken := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", teamID, records[0].TS.UnixMilli(), records[0].EventID.String()))))

	// Insert batch into ClickHouse asynchronously with durability (§5.4)
	if err := s.chClient.InsertBatch(ctx, records, dedupToken); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to write event batch: %w", err))
	}

	// Commercial usage metering. Discarded in the open build.
	s.hooks.UsageMeter.RecordUsage(ctx, teamID, len(records))

	return connect.NewResponse(&lumenv1.TrackResponse{
		Success:        true,
		ProcessedCount: uint32(len(records)),
	}), nil
}

// Identify associates an anonymous identity with an authenticated user identity.
func (s *Service) Identify(ctx context.Context, req *connect.Request[lumenv1.IdentifyRequest]) (*connect.Response[lumenv1.IdentifyResponse], error) {
	teamID := auth.GetTeamID(ctx)
	if teamID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("unauthorized team context"))
	}

	msg := req.Msg
	if msg == nil || msg.AnonId == "" || msg.UserId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("anon_id and user_id are required"))
	}

	// 1. Record identity mapping in ClickHouse identities table (§3.3)
	if err := s.chClient.InsertIdentity(ctx, teamID, msg.AnonId, msg.UserId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to insert identity: %w", err))
	}

	// 2. Emit synthetic $identify event into lumen.events
	eventUUID, _ := uuid.NewV7()
	syntheticEvent := ch.EventRecord{
		TeamID:    teamID,
		TS:        time.Now().UTC(),
		Name:      "$identify",
		EventID:   eventUUID,
		AnonID:    msg.AnonId,
		UserID:    msg.UserId,
		SessionID: "",
		Props:     string(msg.TraitsJson),
	}

	_ = s.chClient.InsertBatch(ctx, []ch.EventRecord{syntheticEvent}, eventUUID.String())

	return connect.NewResponse(&lumenv1.IdentifyResponse{Success: true}), nil
}

func (s *Service) initWeb() {
	s.webOnce.Do(func() {
		s.sessions, _ = lru.New[string, *lru.Cache[string, webSession]](webTeams)
		s.limiters, _ = lru.New[string, *rate.Limiter](webCacheSize)
	})
}

// allowWeb applies the per (web key, client IP) token bucket to n events.
// ponytail: in-memory per-replica limiter; each ingest replica grants its own budget — move to a shared store if replicas > 1 and that matters.
func (s *Service) allowWeb(keyID, clientIP string, n int, now time.Time) bool {
	s.initWeb()
	k := keyID + "|" + rateKeyIP(clientIP)
	s.webMu.Lock()
	l, ok := s.limiters.Get(k)
	if !ok {
		l = rate.NewLimiter(webRate, webBurst)
		s.limiters.Add(k, l)
	}
	s.webMu.Unlock()
	return l.AllowN(now, n)
}

// applyWebIdentity replaces client-sent ids with server-derived cookieless ones
// and drops the IP, whatever the tenant's store_ip setting.
// The stored anon_id always uses today's salt; yesterday's only finds a session
// that was live at UTC midnight so the visit continues across the salt change.
func (s *Service) applyWebIdentity(rec *ch.EventRecord, salt, prevSalt []byte, clientIP, userAgent string, now time.Time) {
	host := ""
	if u, err := url.Parse(rec.URL); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	rec.AnonID = webAnonID(salt, rec.TeamID, host, clientIP, userAgent)
	rec.UserID = ""
	prevAnon := ""
	if prevSalt != nil {
		prevAnon = webAnonID(prevSalt, rec.TeamID, host, clientIP, userAgent)
	}
	rec.SessionID = s.webSessionID(rec.TeamID, rec.AnonID, prevAnon, now)
	rec.IP = nil
}

// webAnonID is "w1_" + 32 hex chars of sha256(salt ‖ team ‖ host ‖ ip ‖ ua),
// fields NUL-separated so they cannot run into each other.
func webAnonID(salt []byte, teamID, host, clientIP, userAgent string) string {
	h := sha256.New()
	h.Write(salt)
	for _, f := range []string{teamID, host, clientIP, userAgent} {
		h.Write([]byte{0})
		h.Write([]byte(f))
	}
	return "w1_" + hex.EncodeToString(h.Sum(nil))[:32]
}

// webSessionID returns the visitor's session, starting a new one after 30 min idle.
// A live session under prevAnonID (yesterday's salt) is adopted when anonID has none.
// ponytail: per-replica session state; with >1 ingest replica a visit can split — move to a shared store if replicas > 1
// Each team has its own bounded cache so one busy site cannot evict another's visits.
func (s *Service) webSessionID(teamID, anonID, prevAnonID string, now time.Time) string {
	s.initWeb()
	s.webMu.Lock()
	defer s.webMu.Unlock()
	team, ok := s.sessions.Get(teamID)
	if !ok {
		team, _ = lru.New[string, webSession](webTeamCache)
		s.sessions.Add(teamID, team)
	}
	live := func(id string) (webSession, bool) {
		sess, ok := team.Get(id)
		return sess, ok && now.Sub(sess.lastSeen) <= webSessionIdle
	}
	sess, ok := live(anonID)
	if !ok && prevAnonID != "" && prevAnonID != anonID {
		sess, ok = live(prevAnonID)
	}
	if !ok {
		id, _ := uuid.NewV7()
		sess.id = id.String()
	}
	sess.lastSeen = now
	team.Add(anonID, sess)
	return sess.id
}

// checkWebEventNames lets web keys send $pageview and custom (non-$) events
// only; reserved names such as $identify reject the whole request.
func checkWebEventNames(events []*lumenv1.Event) error {
	for _, ev := range events {
		if ev != nil && strings.HasPrefix(ev.Name, "$") && ev.Name != "$pageview" {
			return fmt.Errorf("event name %q is reserved", ev.Name)
		}
	}
	return nil
}

var keptUTM = []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"}

// minimiseURL keeps scheme://host/path. For page URLs it also keeps the UTM
// params (fixed order) and a "#/" hash-routing fragment; referrers keep nothing
// else. Unparseable or host-less input becomes "".
func minimiseURL(raw string, page bool) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	out := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}
	if page {
		q := u.Query()
		var parts []string
		for _, k := range keptUTM {
			if v := q.Get(k); v != "" {
				parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
			}
		}
		out.RawQuery = strings.Join(parts, "&")
		if strings.HasPrefix(u.Fragment, "/") {
			out.Fragment = u.Fragment
		}
	}
	return out.String()
}

// rateKeyIP groups IPv6 clients by /64 (one subscriber's prefix); IPv4 is as-is.
func rateKeyIP(ip string) string {
	p := net.ParseIP(ip)
	if p == nil || p.To4() != nil {
		return ip
	}
	return p.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// resolveClientIP returns the client IP as seen by the outermost trusted proxy.
// Each of the `hops` proxies appends one X-Forwarded-For entry, so the client is
// the entry hops-from-the-right; anything left of it is client-controlled and
// ignored. Fewer entries than hops uses the leftmost. Without a usable header
// (or hops == 0) the direct peer address is used.
func resolveClientIP(headers http.Header, peerAddr string, hops int) string {
	if xff := headers.Values("X-Forwarded-For"); hops > 0 && len(xff) > 0 {
		entries := strings.Split(strings.Join(xff, ","), ",")
		i := max(len(entries)-hops, 0)
		if ip := net.ParseIP(strings.TrimSpace(entries[i])); ip != nil {
			return ip.String()
		}
	}

	if host, _, err := net.SplitHostPort(peerAddr); err == nil {
		return host
	}
	return peerAddr
}

// TrackStream handles bidirectional long-lived gRPC streaming ingestion.
func (s *Service) TrackStream(ctx context.Context, stream *connect.BidiStream[lumenv1.TrackRequest, lumenv1.TrackAck]) error {
	for {
		req, err := stream.Receive()
		if err != nil {
			return err
		}

		// Convert single stream request using Track logic
		resp, err := s.Track(ctx, connect.NewRequest(req))
		if err != nil {
			return err
		}

		if err := stream.Send(&lumenv1.TrackAck{ProcessedCount: resp.Msg.ProcessedCount}); err != nil {
			return err
		}
	}
}

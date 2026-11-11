// Package auth provides API key hashing, Ristretto TTL caching, and Connect interceptors.
package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/dgraph-io/ristretto"

	"github.com/SyneHQ/lumen/internal/pg"
)

type contextKey string

const (
	teamIDKey  contextKey = "team_id"
	storeIPKey contextKey = "store_ip"
	webKeyKey  contextKey = "web_key"
)

// Key kinds stored in lumen_api_keys.kind.
const (
	KindServer = "server"
	KindWeb    = "web"
)

// CachedAuth represents the cached outcome of an API key validation.
type CachedAuth struct {
	TeamID         string
	StoreIP        bool
	Valid          bool
	Kind           string
	AllowedOrigins []string
}

// WebKey is injected into the request context when the caller used a web key.
type WebKey struct {
	ID   string // key hash hex; identifies the key without exposing it
	Salt []byte // today's (UTC) shared salt for cookieless visitor ids
	// PrevSalt is yesterday's salt, used only to carry a visit's session across midnight.
	PrevSalt []byte
}

// Authenticator handles API key verification with in-memory Ristretto caching.
type Authenticator struct {
	pgStore *pg.Store
	cache   *ristretto.Cache

	lookup func(context.Context, []byte) (pg.KeyAuth, error)
	salt   func(context.Context, time.Time) ([]byte, error)

	saltMu  sync.Mutex
	saltDay string
	saltVal [2][]byte // today, yesterday
}

// NewAuthenticator creates an Authenticator instance with an initialized Ristretto cache.
func NewAuthenticator(pgStore *pg.Store) (*Authenticator, error) {
	cache, err := ristretto.NewCache(&ristretto.Config{
		NumCounters: 1e5,     // 100k keys tracker
		MaxCost:     1 << 20, // 1MB max memory cost
		BufferItems: 64,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create ristretto cache: %w", err)
	}

	a := &Authenticator{pgStore: pgStore, cache: cache}
	if pgStore != nil {
		a.lookup = pgStore.GetKeyByHash
		a.salt = pgStore.WebSalt
	}
	return a, nil
}

// HashKey produces a SHA-256 byte slice checksum from a raw secret key string.
func HashKey(key string) []byte {
	hash := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hash[:]
}

// ValidateKey checks an API key against the in-memory cache or Postgres control plane.
func (a *Authenticator) ValidateKey(ctx context.Context, rawKey string) (string, bool, error) {
	res, _, err := a.resolveKey(ctx, rawKey)
	return res.TeamID, res.StoreIP, err
}

func (a *Authenticator) resolveKey(ctx context.Context, rawKey string) (CachedAuth, string, error) {
	if rawKey == "" {
		return CachedAuth{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("missing x-lumen-key header"))
	}

	hashBytes := HashKey(rawKey)
	hashHex := fmt.Sprintf("%x", hashBytes)

	// Check Ristretto cache first
	if value, found := a.cache.Get(hashHex); found {
		if authResult, ok := value.(CachedAuth); ok {
			if !authResult.Valid {
				return CachedAuth{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or revoked api key"))
			}
			return authResult, hashHex, nil
		}
	}

	// Cache miss: resolve key against Postgres database
	k, err := a.lookup(ctx, hashBytes)
	if err != nil {
		// Cache negative lookup for 10 seconds to defend against key enumeration floods
		a.cache.SetWithTTL(hashHex, CachedAuth{Valid: false}, 1, 10*time.Second)
		return CachedAuth{}, "", connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or revoked api key"))
	}

	// Cache positive lookup for 60 seconds (a revoked key dies within 1 minute)
	res := CachedAuth{TeamID: k.TeamID, StoreIP: k.StoreIP, Valid: true, Kind: k.Kind, AllowedOrigins: k.AllowedOrigins}
	a.cache.SetWithTTL(hashHex, res, 1, 60*time.Second)

	return res, hashHex, nil
}

// NewAuthInterceptor returns a Connect UnaryInterceptor enforcing API key authorization.
func (a *Authenticator) NewAuthInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			// Extract API key from x-lumen-key metadata or Authorization bearer header
			rawKey := req.Header().Get("x-lumen-key")
			if rawKey == "" {
				authHeader := req.Header().Get("Authorization")
				if strings.HasPrefix(authHeader, "Bearer ") {
					rawKey = strings.TrimPrefix(authHeader, "Bearer ")
				}
			}

			res, keyID, err := a.resolveKey(ctx, rawKey)
			if err != nil {
				return nil, err
			}

			if res.Kind == KindWeb {
				if !strings.HasSuffix(req.Spec().Procedure, "/Track") && !strings.HasSuffix(req.Spec().Procedure, "/TrackStream") {
					return nil, connect.NewError(connect.CodePermissionDenied, errors.New("web keys may only send events"))
				}
				if !OriginAllowed(req.Header().Get("Origin"), res.AllowedOrigins) {
					return nil, connect.NewError(connect.CodePermissionDenied, errors.New("origin not allowed for this key"))
				}
				salts, err := a.webSalts(ctx, time.Now())
				if err != nil {
					return nil, connect.NewError(connect.CodeUnavailable, errors.New("web identity unavailable"))
				}
				ctx = WithWebKey(ctx, WebKey{ID: keyID, Salt: salts[0], PrevSalt: salts[1]})
				res.StoreIP = false // never keep raw IPs from browsers
			}

			// Inject team parameters into context
			ctx = context.WithValue(ctx, teamIDKey, res.TeamID)
			ctx = context.WithValue(ctx, storeIPKey, res.StoreIP)

			return next(ctx, req)
		}
	}
}

// webSalts returns today's and yesterday's (UTC) shared salts, cached per day.
func (a *Authenticator) webSalts(ctx context.Context, now time.Time) ([2][]byte, error) {
	day := now.UTC().Format("2006-01-02")
	a.saltMu.Lock()
	defer a.saltMu.Unlock()
	if a.saltDay == day {
		return a.saltVal, nil
	}
	today, err := a.salt(ctx, now)
	if err != nil {
		return [2][]byte{}, err
	}
	yesterday, err := a.salt(ctx, now.AddDate(0, 0, -1))
	if err != nil {
		return [2][]byte{}, err
	}
	a.saltDay, a.saltVal = day, [2][]byte{today, yesterday}
	return a.saltVal, nil
}

// WithWebKey marks ctx as authenticated by a web key.
func WithWebKey(ctx context.Context, k WebKey) context.Context {
	return context.WithValue(ctx, webKeyKey, k)
}

// GetWebKey reports whether the request used a web key, and its details.
func GetWebKey(ctx context.Context) (WebKey, bool) {
	k, ok := ctx.Value(webKeyKey).(WebKey)
	return k, ok
}

// OriginAllowed reports whether the Origin header's hostname matches an allowed
// entry exactly, or a "*.example.com" entry as a strict subdomain.
func OriginAllowed(origin string, allowed []string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, a := range allowed {
		if suffix, ok := strings.CutPrefix(a, "*"); ok {
			if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
				return true
			}
		} else if host == a {
			return true
		}
	}
	return false
}

// NormalizeAllowedOrigins validates a CreateKey kind and its allowed origins.
// Empty kind means "server". Web keys need at least one origin; entries are
// bare hostnames, optionally "*.example.com", stored lowercase.
func NormalizeAllowedOrigins(kind string, origins []string) (string, []string, error) {
	switch kind {
	case "", KindServer:
		if len(origins) > 0 {
			return "", nil, errors.New("allowed_origins only apply to web keys")
		}
		return KindServer, nil, nil
	case KindWeb:
	default:
		return "", nil, fmt.Errorf("unknown key kind %q", kind)
	}
	if len(origins) == 0 {
		return "", nil, errors.New("web keys need at least one allowed origin")
	}
	out := make([]string, 0, len(origins))
	for _, o := range origins {
		o = strings.ToLower(strings.TrimSpace(o))
		base, wild := strings.CutPrefix(o, "*.")
		if !validHostname(base) || (wild && !strings.Contains(base, ".")) {
			return "", nil, fmt.Errorf("allowed origin %q must be a hostname like example.com or *.example.com", o)
		}
		out = append(out, o)
	}
	return KindWeb, out, nil
}

func validHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// GetTeamID retrieves the team_id injected into context by the auth interceptor.
func GetTeamID(ctx context.Context) string {
	if val, ok := ctx.Value(teamIDKey).(string); ok {
		return val
	}
	return ""
}

// GetStoreIP retrieves the store_ip preference injected into context by the auth interceptor.
func GetStoreIP(ctx context.Context) bool {
	if val, ok := ctx.Value(storeIPKey).(bool); ok {
		return val
	}
	return false
}

// Package auth provides API key hashing, Ristretto TTL caching, and Connect interceptors.
package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/dgraph-io/ristretto"

	"github.com/SyneHQ/lumen/internal/pg"
)

type contextKey string

const (
	teamIDKey  contextKey = "team_id"
	storeIPKey contextKey = "store_ip"
)

// CachedAuth represents the cached outcome of an API key validation.
type CachedAuth struct {
	TeamID  string
	StoreIP bool
	Valid   bool
}

// Authenticator handles API key verification with in-memory Ristretto caching.
type Authenticator struct {
	pgStore *pg.Store
	cache   *ristretto.Cache
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

	return &Authenticator{
		pgStore: pgStore,
		cache:   cache,
	}, nil
}

// HashKey produces a SHA-256 byte slice checksum from a raw secret key string.
func HashKey(key string) []byte {
	hash := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hash[:]
}

// ValidateKey checks an API key against the in-memory cache or Postgres control plane.
func (a *Authenticator) ValidateKey(ctx context.Context, rawKey string) (string, bool, error) {
	if rawKey == "" {
		return "", false, connect.NewError(connect.CodeUnauthenticated, errors.New("missing x-lumen-key header"))
	}

	hashBytes := HashKey(rawKey)
	hashHex := fmt.Sprintf("%x", hashBytes)

	// Check Ristretto cache first
	if value, found := a.cache.Get(hashHex); found {
		if authResult, ok := value.(CachedAuth); ok {
			if !authResult.Valid {
				return "", false, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or revoked api key"))
			}
			return authResult.TeamID, authResult.StoreIP, nil
		}
	}

	// Cache miss: resolve key against Postgres database
	teamID, storeIP, err := a.pgStore.GetTeamIDByKeyHash(ctx, hashBytes)
	if err != nil {
		// Cache negative lookup for 10 seconds to defend against key enumeration floods
		a.cache.SetWithTTL(hashHex, CachedAuth{Valid: false}, 1, 10*time.Second)
		return "", false, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or revoked api key"))
	}

	// Cache positive lookup for 60 seconds (a revoked key dies within 1 minute)
	a.cache.SetWithTTL(hashHex, CachedAuth{TeamID: teamID, StoreIP: storeIP, Valid: true}, 1, 60*time.Second)

	return teamID, storeIP, nil
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

			teamID, storeIP, err := a.ValidateKey(ctx, rawKey)
			if err != nil {
				return nil, err
			}

			// Inject team parameters into context
			ctx = context.WithValue(ctx, teamIDKey, teamID)
			ctx = context.WithValue(ctx, storeIPKey, storeIP)

			return next(ctx, req)
		}
	}
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

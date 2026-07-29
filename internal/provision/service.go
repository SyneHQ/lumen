// Package provision implements the AdminService Connect handlers for provisioning and key lifecycle management.
package provision

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"

	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
	"github.com/SyneHQ/lumen/gen/lumen/v1/lumenv1connect"
	"github.com/SyneHQ/lumen/internal/auth"
	"github.com/SyneHQ/lumen/internal/ch"
	"github.com/SyneHQ/lumen/internal/pg"
)

// AdminService implements the lumenv1connect.AdminServiceHandler interface.
type AdminService struct {
	lumenv1connect.UnimplementedAdminServiceHandler
	chClient   *ch.Client
	pgStore    *pg.Store
	adminToken string
	chHost     string
	chPort     int
}

// NewAdminService creates a new AdminService handler.
func NewAdminService(chClient *ch.Client, pgStore *pg.Store, adminToken, chHost string, chPort int) *AdminService {
	return &AdminService{
		chClient:   chClient,
		pgStore:    pgStore,
		adminToken: adminToken,
		chHost:     chHost,
		chPort:     chPort,
	}
}

// Provision sets up ClickHouse credentials, table row security policies, quotas, and API keys for a tenant (§10).
func (a *AdminService) Provision(ctx context.Context, req *connect.Request[lumenv1.ProvisionRequest]) (*connect.Response[lumenv1.ProvisionResponse], error) {
	if err := a.authorizeAdmin(req.Header()); err != nil {
		return nil, err
	}

	teamID := req.Msg.TeamId
	if teamID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("team_id is required"))
	}

	slug := sanitizeSlug(teamID)
	chUser := fmt.Sprintf("lumen_t_%s", slug)
	chPassword := generateRandomString(32)

	// 1. Provision ClickHouse user, row security policies, and quotas (§4)
	if err := a.chClient.ProvisionTenant(ctx, teamID, chUser, chPassword); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to provision clickhouse tenant: %w", err))
	}

	// 2. Register tenant in Postgres control plane
	if err := a.pgStore.RegisterTenant(ctx, teamID, chUser, req.Msg.StoreIp); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to register postgres tenant: %w", err))
	}

	// 3. Generate raw ingest key: lum_live_<8 char prefix>_<32 byte secret>
	prefix := generateRandomString(8)
	secret := generateRandomString(32)
	ingestKey := fmt.Sprintf("lum_live_%s_%s", prefix, secret)
	keyHash := auth.HashKey(ingestKey)

	// 4. Store API key hash in Postgres
	if err := a.pgStore.SaveAPIKey(ctx, keyHash, fmt.Sprintf("lum_live_%s...", prefix), "Default Ingestion Key", teamID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save api key: %w", err))
	}

	return connect.NewResponse(&lumenv1.ProvisionResponse{
		Host:      a.chHost,
		Port:      int32(a.chPort),
		Database:  "lumen",
		Username:  chUser,
		Password:  chPassword,
		IngestKey: ingestKey,
	}), nil
}

// CreateKey generates an additional ingestion key for a team.
func (a *AdminService) CreateKey(ctx context.Context, req *connect.Request[lumenv1.CreateKeyRequest]) (*connect.Response[lumenv1.CreateKeyResponse], error) {
	if err := a.authorizeAdmin(req.Header()); err != nil {
		return nil, err
	}

	teamID := req.Msg.TeamId
	if teamID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("team_id is required"))
	}

	keyName := req.Msg.Name
	if keyName == "" {
		keyName = "Ingestion Key"
	}

	prefix := generateRandomString(8)
	secret := generateRandomString(32)
	ingestKey := fmt.Sprintf("lum_live_%s_%s", prefix, secret)
	keyHash := auth.HashKey(ingestKey)
	keyPrefixDisplay := fmt.Sprintf("lum_live_%s...", prefix)
	now := time.Now().Unix()

	if err := a.pgStore.SaveAPIKey(ctx, keyHash, keyPrefixDisplay, keyName, teamID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save key: %w", err))
	}

	return connect.NewResponse(&lumenv1.CreateKeyResponse{
		IngestKey:     ingestKey,
		KeyPrefix:     keyPrefixDisplay,
		Name:          keyName,
		CreatedAtUnix: now,
	}), nil
}

// RevokeKey decommissions (revokes) a specific key for a team.
func (a *AdminService) RevokeKey(ctx context.Context, req *connect.Request[lumenv1.RevokeKeyRequest]) (*connect.Response[lumenv1.RevokeKeyResponse], error) {
	if err := a.authorizeAdmin(req.Header()); err != nil {
		return nil, err
	}

	teamID := req.Msg.TeamId
	keyPrefix := req.Msg.KeyPrefix

	if teamID == "" || keyPrefix == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("team_id and key_prefix are required"))
	}

	if err := a.pgStore.RevokeSpecificKey(ctx, teamID, keyPrefix); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to revoke key: %w", err))
	}

	return connect.NewResponse(&lumenv1.RevokeKeyResponse{Success: true}), nil
}

// ListKeys returns metadata for all provisioned keys (active and revoked) for a team.
func (a *AdminService) ListKeys(ctx context.Context, req *connect.Request[lumenv1.ListKeysRequest]) (*connect.Response[lumenv1.ListKeysResponse], error) {
	if err := a.authorizeAdmin(req.Header()); err != nil {
		return nil, err
	}

	teamID := req.Msg.TeamId
	if teamID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("team_id is required"))
	}

	records, err := a.pgStore.ListTeamKeys(ctx, teamID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list keys: %w", err))
	}

	var pbKeys []*lumenv1.KeyMetadata
	for _, rec := range records {
		var revokedUnix int64
		isActive := true
		if rec.RevokedAt != nil {
			revokedUnix = rec.RevokedAt.Unix()
			isActive = false
		}

		pbKeys = append(pbKeys, &lumenv1.KeyMetadata{
			KeyPrefix:     rec.KeyPrefix,
			Name:          rec.Name,
			CreatedAtUnix: rec.CreatedAt.Unix(),
			RevokedAtUnix: revokedUnix,
			IsActive:      isActive,
		})
	}

	return connect.NewResponse(&lumenv1.ListKeysResponse{Keys: pbKeys}), nil
}

// RotateKey revokes existing keys for a team and issues a new ingest key.
func (a *AdminService) RotateKey(ctx context.Context, req *connect.Request[lumenv1.RotateKeyRequest]) (*connect.Response[lumenv1.RotateKeyResponse], error) {
	if err := a.authorizeAdmin(req.Header()); err != nil {
		return nil, err
	}

	teamID := req.Msg.TeamId
	if teamID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("team_id is required"))
	}

	// 1. Revoke existing active API keys
	if err := a.pgStore.RevokeTeamKeys(ctx, teamID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to revoke old keys: %w", err))
	}

	// 2. Generate and store new ingest key
	prefix := generateRandomString(8)
	secret := generateRandomString(32)
	ingestKey := fmt.Sprintf("lum_live_%s_%s", prefix, secret)
	keyHash := auth.HashKey(ingestKey)

	if err := a.pgStore.SaveAPIKey(ctx, keyHash, fmt.Sprintf("lum_live_%s...", prefix), "Rotated Ingestion Key", teamID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to save new key: %w", err))
	}

	return connect.NewResponse(&lumenv1.RotateKeyResponse{NewIngestKey: ingestKey}), nil
}

// Deprovision removes ClickHouse users and policies and revokes tenant API keys.
func (a *AdminService) Deprovision(ctx context.Context, req *connect.Request[lumenv1.DeprovisionRequest]) (*connect.Response[lumenv1.DeprovisionResponse], error) {
	if err := a.authorizeAdmin(req.Header()); err != nil {
		return nil, err
	}

	teamID := req.Msg.TeamId
	if teamID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("team_id is required"))
	}

	slug := sanitizeSlug(teamID)
	chUser := fmt.Sprintf("lumen_t_%s", slug)

	// 1. Revoke Postgres keys
	_ = a.pgStore.RevokeTeamKeys(ctx, teamID)

	// 2. Remove ClickHouse users, policies, and quotas
	_ = a.chClient.DeprovisionTenant(ctx, teamID, chUser)

	return connect.NewResponse(&lumenv1.DeprovisionResponse{Success: true}), nil
}

// DeleteUser initiates GDPR user deletion in ClickHouse.
func (a *AdminService) DeleteUser(ctx context.Context, req *connect.Request[lumenv1.DeleteUserRequest]) (*connect.Response[lumenv1.DeleteUserResponse], error) {
	if err := a.authorizeAdmin(req.Header()); err != nil {
		return nil, err
	}

	msg := req.Msg
	if msg.TeamId == "" || (msg.UserId == "" && msg.AnonId == "") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("team_id and at least one of user_id or anon_id are required"))
	}

	if err := a.chClient.DeleteUserData(ctx, msg.TeamId, msg.UserId, msg.AnonId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to submit delete query: %w", err))
	}

	return connect.NewResponse(&lumenv1.DeleteUserResponse{Success: true}), nil
}

func (a *AdminService) authorizeAdmin(headers map[string][]string) error {
	authHeader := ""
	if vals, ok := headers["authorization"]; ok && len(vals) > 0 {
		authHeader = vals[0]
	} else if vals, ok := headers["Authorization"]; ok && len(vals) > 0 {
		authHeader = vals[0]
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" || token != a.adminToken {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or missing admin bearer token"))
	}

	return nil
}

func generateRandomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func sanitizeSlug(input string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9_]`)
	return reg.ReplaceAllString(input, "_")
}

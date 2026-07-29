// Package ingest implements the IngestService Connect handlers for event and identity ingestion.
package ingest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

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
}

// NewService creates an Ingest Service handler.
func NewService(chClient *ch.Client, enricher *enrich.Enricher) *Service {
	return &Service{
		chClient: chClient,
		enricher: enricher,
	}
}

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

	batchContext := batch.Context
	if batchContext == nil {
		batchContext = &lumenv1.Context{}
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

		// Run server-side User-Agent, GeoIP, and URL enrichment (§5.3)
		enriched := s.enricher.Enrich(activeCtx.UserAgent, "", activeCtx.Url, activeCtx.Referrer, storeIP)

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
			URL:            activeCtx.Url,
			Path:           enriched.Path,
			Host:           enriched.Host,
			Referrer:       activeCtx.Referrer,
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

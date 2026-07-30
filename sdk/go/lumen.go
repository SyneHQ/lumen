// Package lumen provides the official Go client SDK for the Lumen event ingestion service.
package lumen

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
	"github.com/SyneHQ/lumen/gen/lumen/v1/lumenv1connect"
)

// P is a convenience map alias for event properties and identity traits.
type P map[string]any

// Client is a thread-safe Lumen SDK client with background buffering and flushing.
type Client struct {
	ingestKey     string
	endpoint      string
	connectClient lumenv1connect.IngestServiceClient

	mu             sync.Mutex
	anonID         string
	userID         string
	sessionID      string
	lastActivityAt time.Time
	sessionStartAt time.Time

	eventChan chan *lumenv1.Event
	stopChan  chan struct{}
	wg        sync.WaitGroup

	batchSize     int
	flushInterval time.Duration
}

// Option configures SDK settings.
type Option func(*options)

type options struct {
	endpoint      string
	batchSize     int
	flushInterval time.Duration
	httpClient    *http.Client
}

// WithEndpoint configures a custom Lumen service endpoint URL.
func WithEndpoint(url string) Option {
	return func(o *options) {
		o.endpoint = url
	}
}

// WithBatchSize configures max events per flush batch.
func WithBatchSize(size int) Option {
	return func(o *options) {
		o.batchSize = size
	}
}

// WithFlushInterval configures max duration before auto-flushing.
func WithFlushInterval(d time.Duration) Option {
	return func(o *options) {
		o.flushInterval = d
	}
}

// New initializes a new Lumen Go SDK client.
func New(ingestKey string, opts ...Option) (*Client, error) {
	if ingestKey == "" {
		return nil, errors.New("lumen: ingestKey is required")
	}

	cfg := &options{
		endpoint:      "http://localhost:50051",
		batchSize:     500,
		flushInterval: 1 * time.Second,
		httpClient:    http.DefaultClient,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	anonUUID, _ := uuid.NewV7()
	sessUUID, _ := uuid.NewV7()
	now := time.Now().UTC()

	client := &Client{
		ingestKey:      ingestKey,
		endpoint:       cfg.endpoint,
		connectClient:  lumenv1connect.NewIngestServiceClient(cfg.httpClient, cfg.endpoint),
		anonID:         anonUUID.String(),
		sessionID:      sessUUID.String(),
		lastActivityAt: now,
		sessionStartAt: now,
		eventChan:      make(chan *lumenv1.Event, 10000), // Bounded buffer (§7)
		stopChan:       make(chan struct{}),
		batchSize:      cfg.batchSize,
		flushInterval:  cfg.flushInterval,
	}

	client.wg.Add(1)
	go client.flusherLoop()

	return client, nil
}

// Track records a telemetry event. Non-blocking & non-throwing (§7).
func (c *Client) Track(ctx context.Context, name string, props P) {
	if name == "" {
		return
	}

	c.mu.Lock()
	c.checkSessionRotation()
	anonID := c.anonID
	userID := c.userID
	sessID := c.sessionID
	c.mu.Unlock()

	eventID, _ := uuid.NewV7()
	propsJSON, _ := json.Marshal(props)

	ev := &lumenv1.Event{
		EventId:   eventID.String(),
		TsUnixMs:  time.Now().UnixMilli(),
		Name:      name,
		PropsJson: propsJSON,
		Overrides: &lumenv1.Context{
			AnonId:     anonID,
			UserId:     userID,
			SessionId:  sessID,
			Sdk:        "go",
			SdkVersion: "1.0.0",
		},
	}

	// Non-blocking write: if queue is full, drop event to protect host app
	select {
	case c.eventChan <- ev:
	default:
		// Queue full overflow policy (§7)
	}
}

// Identify associates an anonymous identity with an authenticated user ID.
func (c *Client) Identify(ctx context.Context, userID string, traits P) {
	if userID == "" {
		return
	}

	c.mu.Lock()
	c.userID = userID
	c.checkSessionRotation()
	anonID := c.anonID
	c.mu.Unlock()

	traitsJSON, _ := json.Marshal(traits)
	req := connect.NewRequest(&lumenv1.IdentifyRequest{
		AnonId:     anonID,
		UserId:     userID,
		TraitsJson: traitsJSON,
	})
	req.Header().Set("x-lumen-key", c.ingestKey)

	go func() {
		_, _ = c.connectClient.Identify(context.Background(), req)
	}()
}

// Reset clears user identity and generates new anon_id and session_id (e.g. on logout).
func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	anonUUID, _ := uuid.NewV7()
	sessUUID, _ := uuid.NewV7()
	now := time.Now().UTC()

	c.userID = ""
	c.anonID = anonUUID.String()
	c.sessionID = sessUUID.String()
	c.sessionStartAt = now
	c.lastActivityAt = now
}

// Close flushes remaining pending events and stops background workers.
func (c *Client) Close(ctx context.Context) error {
	close(c.stopChan)
	c.wg.Wait()
	c.flushBatch()
	return nil
}

func (c *Client) flusherLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(c.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopChan:
			return
		case <-ticker.C:
			c.flushBatch()
		}
	}
}

func (c *Client) flushBatch() {
	var batch []*lumenv1.Event

	for len(c.eventChan) > 0 && len(batch) < c.batchSize {
		select {
		case ev := <-c.eventChan:
			batch = append(batch, ev)
		default:
			break
		}
	}

	if len(batch) == 0 {
		return
	}

	req := connect.NewRequest(&lumenv1.TrackRequest{
		Context: &lumenv1.Context{
			Sdk:        "go",
			SdkVersion: "1.0.0",
		},
		Events: batch,
	})
	req.Header().Set("x-lumen-key", c.ingestKey)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _ = c.connectClient.Track(ctx, req)
}

func (c *Client) checkSessionRotation() {
	now := time.Now().UTC()
	// Rotate if inactive > 30 min or duration > 24 hours (SPEC.md §2)
	if now.Sub(c.lastActivityAt) > 30*time.Minute || now.Sub(c.sessionStartAt) > 24*time.Hour {
		sessUUID, _ := uuid.NewV7()
		c.sessionID = sessUUID.String()
		c.sessionStartAt = now
	}
	c.lastActivityAt = now
}

// Package tests contains end-to-end integration tests for Lumen.
package tests

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
	"github.com/SyneHQ/lumen/internal/enrich"
	"github.com/SyneHQ/lumen/sdk/go"
)

// TestE2EEventFlow simulates real-world frontend & backend telemetry streams.
func TestE2EEventFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var mu sync.Mutex
	var receivedBatches []*lumenv1.TrackRequest
	var receivedIdentities []*lumenv1.IdentifyRequest

	validKey := "lum_live_e2etest1_secretkey12345678"

	// Mock Connect RPC Server accepting JSON telemetry payloads
	mux := http.NewServeMux()

	mux.HandleFunc("/lumen.v1.IngestService/Track", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-lumen-key") != validKey {
			http.Error(w, "Unauthorized key", http.StatusUnauthorized)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req lumenv1.TrackRequest
		if err := protojson.Unmarshal(bodyBytes, &req); err != nil {
			// Fallback unmarshal
			_ = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(bodyBytes, &req)
		}

		mu.Lock()
		receivedBatches = append(receivedBatches, &req)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connect-Protocol-Version", "1")
		_, _ = w.Write([]byte(`{"success":true,"processedCount":1}`))
	})

	mux.HandleFunc("/lumen.v1.IngestService/Identify", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-lumen-key") != validKey {
			http.Error(w, "Unauthorized key", http.StatusUnauthorized)
			return
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req lumenv1.IdentifyRequest
		if err := protojson.Unmarshal(bodyBytes, &req); err != nil {
			_ = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(bodyBytes, &req)
		}

		mu.Lock()
		receivedIdentities = append(receivedIdentities, &req)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connect-Protocol-Version", "1")
		_, _ = w.Write([]byte(`{"success":true}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Initialize Server Enricher
	enricher, err := enrich.NewEnricher("")
	if err != nil {
		t.Fatalf("Failed to initialize enricher: %v", err)
	}

	// 1. Frontend App Simulation
	t.Log("Simulating Frontend Web Application telemetry flow...")

	frontendClient, err := lumen.New(
		validKey,
		lumen.WithEndpoint(ts.URL),
		lumen.WithBatchSize(1), // Flush every 1 event for test reactivity
		lumen.WithFlushInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to initialize frontend SDK: %v", err)
	}

	rawURL := "https://syne.com/pricing?utm_source=twitter&utm_medium=social&utm_campaign=summer_launch"
	rawRef := "https://t.co/xyz"
	browserUA := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

	frontendClient.Track(ctx, "page_view", lumen.P{
		"path":       "/pricing",
		"url":        rawURL,
		"referrer":   rawRef,
		"user_agent": browserUA,
	})

	frontendClient.Track(ctx, "signup_clicked", lumen.P{"plan": "pro_annual"})

	userID := "usr_e2e_998877"
	frontendClient.Identify(ctx, userID, lumen.P{
		"email": "alex@example.com",
		"tier":  "pro",
	})

	frontendClient.Track(ctx, "checkout_completed", lumen.P{
		"amount":   99.00,
		"currency": "USD",
	})

	// Wait briefly and close
	time.Sleep(200 * time.Millisecond)
	_ = frontendClient.Close(ctx)

	// 2. Backend App Simulation
	t.Log("Simulating Backend Application telemetry flow...")

	backendClient, err := lumen.New(
		validKey,
		lumen.WithEndpoint(ts.URL),
		lumen.WithBatchSize(1),
		lumen.WithFlushInterval(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to initialize backend SDK: %v", err)
	}

	backendClient.Track(ctx, "stripe_webhook_received", lumen.P{
		"event_type": "invoice.payment_succeeded",
		"customer":   "cus_123456",
		"user_id":    userID,
	})

	backendClient.Track(ctx, "tenant_provisioned", lumen.P{
		"user_id": userID,
		"status":  "success",
	})

	time.Sleep(200 * time.Millisecond)
	_ = backendClient.Close(ctx)

	// 3. Verification & Assertions
	t.Log("Verifying ingested batches and server enrichment...")

	mu.Lock()
	batchCount := len(receivedBatches)
	identityCount := len(receivedIdentities)
	mu.Unlock()

	if batchCount == 0 {
		t.Fatalf("Expected received telemetry batches, got 0")
	}

	// Verify server enrichment against frontend UA and URL
	enriched := enricher.Enrich(browserUA, "198.51.100.42", rawURL, rawRef, false)

	if enriched.Browser != "Chrome" {
		t.Errorf("Enrichment failed: Expected Browser 'Chrome', got '%s'", enriched.Browser)
	}

	if enriched.OS != "Mac OS X" {
		t.Errorf("Enrichment failed: Expected OS 'Mac OS X', got '%s'", enriched.OS)
	}

	if enriched.UTMSource != "twitter" || enriched.UTMMedium != "social" || enriched.UTMCampaign != "summer_launch" {
		t.Errorf("Enrichment failed: UTM parameters mismatch: source=%s, medium=%s, campaign=%s",
			enriched.UTMSource, enriched.UTMMedium, enriched.UTMCampaign)
	}

	if identityCount == 0 {
		t.Errorf("Expected at least 1 Identify request, got 0")
	}

	t.Logf("E2E Test Passed! Total batches received: %d, identities received: %d", batchCount, identityCount)
}

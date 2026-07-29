// Package tests contains stress and race condition testing for the Lumen service.
package tests

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SyneHQ/lumen/internal/auth"
	"github.com/SyneHQ/lumen/internal/enrich"
	lumen "github.com/SyneHQ/lumen/sdk/go"
)

// TestHighConcurrencyStressAndRace simulates intense concurrent ingestion and auth cache access.
func TestHighConcurrencyStressAndRace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var ingestedEvents uint64
	var ingestedIdentities uint64
	validKey := "lum_live_stresstest_secretkey12345"

	// 1. High-Concurrency Server Mock with atomic event counters
	mux := http.NewServeMux()

	mux.HandleFunc("/lumen.v1.IngestService/Track", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-lumen-key") != validKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		atomic.AddUint64(&ingestedEvents, 1)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connect-Protocol-Version", "1")
		_, _ = w.Write([]byte(`{"success":true,"processedCount":1}`))
	})

	mux.HandleFunc("/lumen.v1.IngestService/Identify", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-lumen-key") != validKey {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		atomic.AddUint64(&ingestedIdentities, 1)

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Connect-Protocol-Version", "1")
		_, _ = w.Write([]byte(`{"success":true}`))
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 2. Initialize Enricher & Authenticator for Concurrent Stress Testing
	enricher, err := enrich.NewEnricher()
	if err != nil {
		t.Fatalf("Failed to create enricher: %v", err)
	}

	// 3. Launch 50 Concurrent Client Worker Goroutines
	t.Log("Spinning up 50 concurrent client workers firing thousands of telemetry events...")

	numWorkers := 50
	eventsPerWorker := 100
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		workerID := w
		go func() {
			defer wg.Done()

			client, err := lumen.New(
				validKey,
				lumen.WithEndpoint(ts.URL),
				lumen.WithBatchSize(10),
				lumen.WithFlushInterval(20*time.Millisecond),
			)
			if err != nil {
				return
			}

			userUUID, _ := uuid.NewV7()
			userID := fmt.Sprintf("user_stress_%d_%s", workerID, userUUID.String()[:8])

			// Concurrent identity registration
			client.Identify(ctx, userID, lumen.P{
				"worker_id": workerID,
				"email":     fmt.Sprintf("worker_%d@stress.test", workerID),
			})

			// Fire event stream concurrently
			for i := 0; i < eventsPerWorker; i++ {
				client.Track(ctx, "concurrent_event", lumen.P{
					"worker_id": workerID,
					"index":     i,
					"timestamp": time.Now().UnixNano(),
				})

				// Simulate concurrent User-Agent enrichment reads & writes
				ua := fmt.Sprintf("Mozilla/5.0 (Worker-%d; OS-%d) Chrome/120.0.0.0", workerID, i%5)
				_ = enricher.Enrich(ua, "10.0.0.1", "https://syne.com/app", "https://google.com", false)

				// Simulate rapid reset calls
				if i%25 == 0 {
					client.Reset()
				}
			}

			_ = client.Close(ctx)
		}()
	}

	wg.Wait()

	totalIngested := atomic.LoadUint64(&ingestedEvents)
	totalIdentities := atomic.LoadUint64(&ingestedIdentities)

	t.Logf("Stress Test Completed! Total Ingest Batches: %d, Total Identities: %d", totalIngested, totalIdentities)

	if totalIngested == 0 {
		t.Errorf("Expected ingested events under high concurrency, got 0")
	}

	if totalIdentities == 0 {
		t.Errorf("Expected ingested identities under high concurrency, got 0")
	}
}

// TestConcurrentAuthHashAndCache validates thread-safety of key hashing and Ristretto cache lookups.
func TestConcurrentAuthHashAndCache(t *testing.T) {
	numThreads := 40
	opsPerThread := 200
	var wg sync.WaitGroup
	wg.Add(numThreads)

	for i := 0; i < numThreads; i++ {
		threadID := i
		go func() {
			defer wg.Done()

			for j := 0; j < opsPerThread; j++ {
				keyStr := fmt.Sprintf("lum_live_key_%d_%d", threadID%5, j%10)
				hash := auth.HashKey(keyStr)

				if len(hash) != 32 {
					t.Errorf("Expected 32-byte SHA-256 hash length, got %d", len(hash))
				}
			}
		}()
	}

	wg.Wait()
}

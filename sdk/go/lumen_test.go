package lumen

import (
	"context"
	"testing"
	"time"
)

func TestGoSDKLifecycle(t *testing.T) {
	client, err := New("lum_live_testkey_12345678", WithBatchSize(10), WithFlushInterval(100*time.Millisecond))
	if err != nil {
		t.Fatalf("Failed to initialize Go SDK: %v", err)
	}

	ctx := context.Background()

	// 1. Test Track
	client.Track(ctx, "signup_button_clicked", P{"plan": "pro", "price": 49.99})

	// 2. Test Identify
	client.Identify(ctx, "usr_998877", P{"email": "user@example.com", "name": "Jane Doe"})

	// Verify identity state
	if client.userID != "usr_998877" {
		t.Errorf("Expected userID to be set to 'usr_998877', got '%s'", client.userID)
	}

	// 3. Test Reset
	oldAnonID := client.anonID
	client.Reset()

	if client.userID != "" {
		t.Errorf("Expected userID to be cleared after Reset()")
	}

	if client.anonID == oldAnonID {
		t.Errorf("Expected new anonID after Reset()")
	}

	// 4. Test Close
	err = client.Close(ctx)
	if err != nil {
		t.Errorf("Unexpected error closing client: %v", err)
	}
}

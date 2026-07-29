package auth

import (
	"bytes"
	"testing"
)

func TestHashKey(t *testing.T) {
	key1 := "lum_live_12345678_abcdef"
	key2 := "lum_live_12345678_abcdef"
	key3 := "lum_live_different_key"

	hash1 := HashKey(key1)
	hash2 := HashKey(key2)
	hash3 := HashKey(key3)

	if !bytes.Equal(hash1, hash2) {
		t.Errorf("Expected identical hashes for identical key input")
	}

	if bytes.Equal(hash1, hash3) {
		t.Errorf("Expected different hashes for different key input")
	}
}

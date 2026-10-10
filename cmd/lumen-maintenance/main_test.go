package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAllowlistRequiresExactReviewedPrivateInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowlist.json")
	good := `{"team_ids":["one","two"],"reason":"operator_confirmed_legacy_deletion"}`
	if err := os.WriteFile(path, []byte(good), 0600); err != nil {
		t.Fatal(err)
	}
	value, digest, err := readAllowlist(path)
	if err != nil || len(value.TeamIDs) != 2 || len(digest) != 64 {
		t.Fatal("valid allowlist rejected")
	}
	for _, raw := range []string{`{"team_ids":[],"reason":"operator_confirmed_legacy_deletion"}`, `{"team_ids":["one","one"],"reason":"operator_confirmed_legacy_deletion"}`, `{"team_ids":["one"],"reason":"guess"}`, good + ` {}`, `{"team_ids":["one"],"reason":"operator_confirmed_legacy_deletion","extra":true}`} {
		os.WriteFile(path, []byte(raw), 0600)
		if _, _, err := readAllowlist(path); err == nil {
			t.Fatal("invalid allowlist accepted")
		}
	}
	os.WriteFile(path, []byte(good), 0600)
	os.Chmod(path, 0644)
	if _, _, err := readAllowlist(path); err == nil {
		t.Fatal("public allowlist accepted")
	}
	os.Chmod(path, 0600)
	link := filepath.Join(filepath.Dir(path), "link.json")
	os.Symlink(path, link)
	if _, _, err := readAllowlist(link); err == nil {
		t.Fatal("symlink accepted")
	}
}

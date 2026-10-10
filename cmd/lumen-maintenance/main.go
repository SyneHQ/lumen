// lumen-maintenance performs explicit, bounded tenant lifecycle recovery.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/SyneHQ/lumen/internal/ch"
	"github.com/SyneHQ/lumen/internal/config"
	"github.com/SyneHQ/lumen/internal/pg"
	"github.com/SyneHQ/lumen/internal/provision"
	"github.com/SyneHQ/lumen/migrations"
)

type allowlist struct {
	TeamIDs []string `json:"team_ids"`
	Reason  string   `json:"reason"`
}

func readAllowlist(path string) (allowlist, string, error) {
	var value allowlist
	if !filepath.IsAbs(path) {
		return value, "", errors.New("allowlist path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return value, "", errors.New("allowlist is unavailable")
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || !ownedByOperator(before) || before.Size() > 1<<20 {
		return value, "", errors.New("allowlist must be a regular mode-0600 file owned by the current operator, no larger than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return value, "", errors.New("allowlist cannot be opened")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm() != 0600 || !ownedByOperator(after) {
		return value, "", errors.New("allowlist changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return value, "", errors.New("allowlist read failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return value, "", errors.New("allowlist JSON is invalid")
	}
	if value.Reason != "operator_confirmed_legacy_deletion" || len(value.TeamIDs) == 0 || len(value.TeamIDs) > 1000 {
		return value, "", errors.New("allowlist must confirm legacy deletion for 1 to 1000 exact tenant IDs")
	}
	seen := map[string]bool{}
	for _, id := range value.TeamIDs {
		if id == "" || len(id) > 1024 || seen[id] {
			return value, "", errors.New("allowlist contains an invalid or duplicate tenant ID")
		}
		seen[id] = true
	}
	digest := sha256.Sum256(raw)
	return value, hex.EncodeToString(digest[:]), nil
}

func run(args []string) error {
	if len(args) == 0 || args[0] != "recover-legacy-deletions" {
		return errors.New("use recover-legacy-deletions --allowlist /absolute/path.json")
	}
	flags := flag.NewFlagSet("recover-legacy-deletions", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("allowlist", "", "protected exact tenant allowlist")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return errors.New("invalid maintenance arguments")
	}
	selected, digest, err := readAllowlist(*path)
	if err != nil {
		return err
	}
	cfg := config.Load()
	if cfg.Validate() != nil {
		return errors.New("maintenance configuration is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	store, err := pg.NewStore(ctx, cfg.PostgresDSN)
	if err != nil {
		return errors.New("maintenance PostgreSQL connection failed")
	}
	defer store.Close()
	files, err := migrations.FS.ReadDir("pg")
	if err != nil {
		return errors.New("PostgreSQL migrations are unavailable")
	}
	for _, file := range files {
		raw, err := migrations.FS.ReadFile("pg/" + file.Name())
		if err != nil || store.RunMigrations(ctx, string(raw)) != nil {
			return errors.New("maintenance PostgreSQL migration failed")
		}
	}
	access, err := ch.NewClientContextWithOptions(ctx, cfg.ClickHouseDSN, ch.ClientOptions{Compression: cfg.ClickHouseCompression})
	if err != nil {
		return errors.New("maintenance ClickHouse connection failed")
	}
	defer access.Close()
	if err = provision.RecoverLegacyDeletion(ctx, store, access, selected.TeamIDs); err != nil {
		return errors.New("legacy deletion recovery did not complete; inspect tenant state and retry the same reviewed allowlist")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"schema_version": 1, "status": "passed", "stage": "legacy-tenant-deletion-recovery", "allowlist_sha256": digest, "recovered_count": len(selected.TeamIDs), "cleanup_complete": true})
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

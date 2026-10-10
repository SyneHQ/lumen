package provision

import (
	"context"
	"errors"
	"fmt"

	"github.com/SyneHQ/lumen/internal/pg"
)

type tenantMetadata interface {
	GetTenant(context.Context, string) (*pg.TenantRecord, error)
	BeginDeletion(context.Context, string, string) error
	FinishDeletion(context.Context, string) error
	ActivateLegacy(context.Context, string) error
	PrepareProvision(context.Context, string, string, bool) error
	ActivateProvision(context.Context, string, []byte, string) error
	ConfirmTenantCreated(context.Context, string) error
}
type tenantAccess interface {
	CreateTenantUser(context.Context, string, string) error
	EnsureTenantAccess(context.Context, string, string) error
	DeprovisionTenant(context.Context, string, string) error
}

func removeTenant(ctx context.Context, store tenantMetadata, access tenantAccess, row *pg.TenantRecord, reason string) error {
	if row != nil && row.State == "creating" {
		return ErrTenantCreationUnconfirmed
	}
	if row == nil || row.State == "inactive" {
		return nil
	}
	if err := store.BeginDeletion(ctx, row.TeamID, reason); err != nil {
		return fmt.Errorf("persist tenant deletion: %w", err)
	}
	if err := access.DeprovisionTenant(ctx, row.TeamID, row.CHUser); err != nil {
		return fmt.Errorf("remove tenant database access: %w", err)
	}
	if err := store.FinishDeletion(ctx, row.TeamID); err != nil {
		return fmt.Errorf("complete tenant deletion: %w", err)
	}
	return nil
}

// reconcileTenant never recreates users. An ambiguous legacy row blocks startup
// instead of guessing whether an administrator wanted SQL access retained.
func reconcileTenant(ctx context.Context, store tenantMetadata, access tenantAccess, row *pg.TenantRecord) error {
	if row == nil {
		return nil
	}
	switch row.State {
	case "inactive":
		return nil
	case "deleting", "provisioning":
		return removeTenant(ctx, store, access, row, "interrupted_"+row.State)
	case "creating":
		return ErrTenantCreationUnconfirmed
	case "legacy":
		if row.ActiveKeyCount == 0 {
			return ErrLegacyRepairRequired
		}
		if err := access.EnsureTenantAccess(ctx, row.TeamID, row.CHUser); err != nil {
			return err
		}
		return store.ActivateLegacy(ctx, row.TeamID)
	case "active":
		return access.EnsureTenantAccess(ctx, row.TeamID, row.CHUser)

	default:
		return errors.New("tenant lifecycle state is invalid")
	}
}

// ReconcileTenantAccess runs before listeners start. Each row is re-read under
// the same cross-instance lock used by provision and delete requests.
func ReconcileTenantAccess(ctx context.Context, store *pg.Store, access tenantAccess) error {
	tenants, err := store.ListTenants(ctx)
	if err != nil {
		return fmt.Errorf("list tenant lifecycle records: %w", err)
	}
	for _, record := range tenants {
		err = store.WithTenantLock(ctx, record.TeamID, func(locked *pg.Store) error {
			row, err := locked.GetTenant(ctx, record.TeamID)
			if err != nil {
				return err
			}
			return reconcileTenant(ctx, locked, access, row)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func provisionTenant(ctx context.Context, store tenantMetadata, access tenantAccess, teamID, user, password string, storeIP bool, key []byte, prefix string) error {
	row, err := store.GetTenant(ctx, teamID)
	if err != nil {
		return err
	}
	if row != nil {
		if row.State == "active" || row.State == "legacy" {
			return errors.New("tenant already exists; deprovision it before provisioning again")
		}
		if row.CHUser != user {
			return errors.New("tenant database identity changed")
		}
		if err := removeTenant(ctx, store, access, row, "provision_retry_cleanup"); err != nil {
			return err
		}
	}
	if err := store.PrepareProvision(ctx, teamID, user, storeIP); err != nil {
		return err
	}
	if err := access.CreateTenantUser(ctx, user, password); err != nil {
		return err
	}
	if err := store.ConfirmTenantCreated(ctx, teamID); err != nil {
		return err
	}
	if err := access.EnsureTenantAccess(ctx, teamID, user); err != nil {
		return err
	}

	return store.ActivateProvision(ctx, teamID, key, prefix)
}

var ErrLegacyRepairRequired = errors.New("legacy tenant has no active keys; review prior deletion evidence, then run lumen-maintenance recover-legacy-deletions --allowlist /absolute/path.json")

var ErrTenantCreationUnconfirmed = errors.New("tenant user creation is unconfirmed; an operator must review the creating records and ClickHouse ownership before retrying startup")

// RecoverLegacyDeletion is an operator-invoked operation, never a startup heuristic.
// The caller must have reviewed prior deletion evidence for every supplied team.
func RecoverLegacyDeletion(ctx context.Context, store *pg.Store, access tenantAccess, teams []string) error {
	if len(teams) == 0 || len(teams) > 1000 {
		return errors.New("supply between 1 and 1000 exact tenant IDs")
	}
	seen := map[string]bool{}
	for _, team := range teams {
		if team == "" || seen[team] {
			return errors.New("tenant allowlist contains an empty or duplicate ID")
		}
		seen[team] = true
	}
	for _, team := range teams {
		err := store.WithTenantLock(ctx, team, func(locked *pg.Store) error {
			row, err := locked.GetTenant(ctx, team)
			if err != nil {
				return err
			}
			if row == nil {
				return errors.New("allowlisted tenant does not exist")
			}
			if row.KeyCount == 0 || row.ActiveKeyCount != 0 {
				return errors.New("allowlisted tenant does not have exclusively revoked key history")
			}
			if row.State == "inactive" {
				return nil
			}
			if row.State != "legacy" && row.State != "deleting" {
				return errors.New("allowlisted tenant is not pending legacy deletion")
			}
			return removeTenant(ctx, locked, access, row, "operator_confirmed_legacy_deletion")
		})
		if err != nil {
			return err
		}
	}
	return nil
}

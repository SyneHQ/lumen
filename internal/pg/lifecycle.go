package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const tenantSelect = `SELECT t.team_id, t.ch_user, t.store_ip, t.lifecycle_state,
 (SELECT count(*) FROM lumen_api_keys k WHERE k.team_id=t.team_id),
 (SELECT count(*) FROM lumen_api_keys k WHERE k.team_id=t.team_id AND k.revoked_at IS NULL)
 FROM lumen_tenants t`

// WithTenantLock serializes lifecycle changes across instances. All callback SQL
// uses the reserved connection, so concurrent callbacks cannot exhaust the pool
// while waiting for a second connection. The lock covers durable intent and CH work.
func (s *Store) WithTenantLock(ctx context.Context, teamID string, work func(*Store) error) (result error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	wait, cancel := context.WithTimeout(ctx, 15*time.Second)
	_, err = conn.Exec(wait, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, "lumen-tenant:"+teamID)
	cancel()
	if err != nil {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelClose()
		_ = conn.Conn().Close(closeCtx)
		return fmt.Errorf("tenant lock acquisition failed: %w", err)
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released bool
		err := conn.QueryRow(release, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, "lumen-tenant:"+teamID).Scan(&released)
		if err != nil || !released {
			// Never return a connection with an uncertain session lock to the pool.
			_ = conn.Conn().Close(release)
			result = errors.Join(result, errors.New("tenant lock release failed"))
		}
	}()
	return work(&Store{pool: s.pool, db: conn})
}

func (s *Store) GetTenant(ctx context.Context, teamID string) (*TenantRecord, error) {
	var row TenantRecord
	err := s.db.QueryRow(ctx, tenantSelect+` WHERE t.team_id=$1`, teamID).Scan(
		&row.TeamID, &row.CHUser, &row.StoreIP, &row.State, &row.KeyCount, &row.ActiveKeyCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) transaction(ctx context.Context, work func(pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollback, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollback)
	}()
	if err := work(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BeginDeletion commits the durable stop intent and revokes keys together.
// Keep the row and key history for restart recovery and audit.
func (s *Store) BeginDeletion(ctx context.Context, teamID, reason string) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE lumen_tenants SET lifecycle_state='deleting',
   lifecycle_reason=CASE WHEN lifecycle_state='deleting' THEN lifecycle_reason ELSE $2 END,
 legacy_recovery_reason=CASE WHEN $2='operator_confirmed_legacy_deletion' THEN COALESCE(legacy_recovery_reason,$2) ELSE legacy_recovery_reason END,
 legacy_recovery_started_at=CASE WHEN $2='operator_confirmed_legacy_deletion' THEN COALESCE(legacy_recovery_started_at,now()) ELSE legacy_recovery_started_at END, lifecycle_changed_at=now() WHERE team_id=$1`, teamID, reason)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("tenant record is missing")
		}
		_, err = tx.Exec(ctx, `UPDATE lumen_api_keys SET revoked_at=COALESCE(revoked_at, now()) WHERE team_id=$1`, teamID)
		return err
	})
}

func (s *Store) FinishDeletion(ctx context.Context, teamID string) error {
	result, err := s.db.Exec(ctx, `UPDATE lumen_tenants SET lifecycle_state='inactive', lifecycle_changed_at=now()
  WHERE team_id=$1 AND lifecycle_state='deleting'`, teamID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("tenant deletion intent is missing")
	}
	return nil
}

func (s *Store) ActivateLegacy(ctx context.Context, teamID string) error {
	result, err := s.db.Exec(ctx, `UPDATE lumen_tenants SET lifecycle_state='active',
  lifecycle_reason='legacy_user_and_active_key_verified', lifecycle_changed_at=now()
  WHERE team_id=$1 AND lifecycle_state='legacy'`, teamID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("legacy tenant state changed")
	}
	return nil
}

// PrepareProvision reserves the unique CH username before any external mutation.
// Reusing an inactive tenant never restores its previous ingest keys.
func (s *Store) PrepareProvision(ctx context.Context, teamID, user string, storeIP bool) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `INSERT INTO lumen_tenants (team_id,ch_user,store_ip,lifecycle_state,lifecycle_reason)
   VALUES ($1,$2,$3,'creating','creation_unconfirmed')
   ON CONFLICT (team_id) DO UPDATE SET store_ip=EXCLUDED.store_ip,
    lifecycle_state='creating',lifecycle_reason='creation_unconfirmed',lifecycle_changed_at=now()
   WHERE lumen_tenants.lifecycle_state='inactive' AND lumen_tenants.ch_user=EXCLUDED.ch_user`, teamID, user, storeIP)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("tenant is not inactive")
		}
		_, err = tx.Exec(ctx, `UPDATE lumen_api_keys SET revoked_at=COALESCE(revoked_at,now()) WHERE team_id=$1`, teamID)
		return err
	})
}

func (s *Store) ActivateProvision(ctx context.Context, teamID string, keyHash []byte, prefix string) error {
	return s.transaction(ctx, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE lumen_tenants SET lifecycle_state='active',
   lifecycle_reason='provision_completed',lifecycle_changed_at=now()
   WHERE team_id=$1 AND lifecycle_state='provisioning'`, teamID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("tenant provisioning intent is missing")
		}
		_, err = tx.Exec(ctx, `INSERT INTO lumen_api_keys (key_hash,key_prefix,name,team_id)
   VALUES ($1,$2,'Default Ingestion Key',$3)`, keyHash, prefix, teamID)
		return err
	})
}

// ConfirmTenantCreated records acknowledged principal ownership before any grants.
// If this write fails, startup must not infer ownership from a reserved name.
func (s *Store) ConfirmTenantCreated(ctx context.Context, teamID string) error {
	result, err := s.db.Exec(ctx, `UPDATE lumen_tenants SET lifecycle_state='provisioning',
  lifecycle_reason='user_creation_confirmed',lifecycle_changed_at=now()
  WHERE team_id=$1 AND lifecycle_state='creating'`, teamID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errors.New("tenant creation intent is missing")
	}
	return nil
}

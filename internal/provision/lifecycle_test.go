package provision

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/SyneHQ/lumen/internal/pg"
)

var syntheticFailure = errors.New("synthetic boundary failure")

type lifecycleFixture struct {
	row    *pg.TenantRecord
	exists bool
	fail   string
	calls  []string
}

func fixture(state string) *lifecycleFixture {
	return &lifecycleFixture{row: &pg.TenantRecord{TeamID: "team", CHUser: "lumen_t_team", State: state, KeyCount: 1, ActiveKeyCount: 1}, exists: true}
}
func (f *lifecycleFixture) step(name string) error {
	f.calls = append(f.calls, name)
	if f.fail == name {
		return syntheticFailure
	}
	return nil
}
func (f *lifecycleFixture) GetTenant(context.Context, string) (*pg.TenantRecord, error) {
	if err := f.step("get"); err != nil {
		return nil, err
	}
	if f.row == nil {
		return nil, nil
	}
	v := *f.row
	return &v, nil
}
func (f *lifecycleFixture) BeginDeletion(_ context.Context, _, _ string) error {
	if err := f.step("begin-delete"); err != nil {
		return err
	}
	f.row.State = "deleting"
	f.row.ActiveKeyCount = 0
	return nil
}
func (f *lifecycleFixture) FinishDeletion(context.Context, string) error {
	if err := f.step("finish-delete"); err != nil {
		return err
	}
	f.row.State = "inactive"
	return nil
}
func (f *lifecycleFixture) ActivateLegacy(context.Context, string) error {
	if err := f.step("activate-legacy"); err != nil {
		return err
	}
	f.row.State = "active"
	return nil
}
func (f *lifecycleFixture) PrepareProvision(_ context.Context, team, user string, privacy bool) error {
	if err := f.step("prepare"); err != nil {
		return err
	}
	f.row = &pg.TenantRecord{TeamID: team, CHUser: user, StoreIP: privacy, State: "creating"}
	return nil
}
func (f *lifecycleFixture) ActivateProvision(context.Context, string, []byte, string) error {
	if err := f.step("activate"); err != nil {
		return err
	}
	f.row.State = "active"
	f.row.KeyCount++
	f.row.ActiveKeyCount = 1
	return nil
}
func (f *lifecycleFixture) ConfirmTenantCreated(context.Context, string) error {
	if err := f.step("confirm"); err != nil {
		return err
	}
	f.row.State = "provisioning"
	return nil
}
func (f *lifecycleFixture) CreateTenantUser(context.Context, string, string) error {
	if err := f.step("create"); err != nil {
		return err
	}
	if f.exists {
		return syntheticFailure
	}
	f.exists = true
	return nil
}
func (f *lifecycleFixture) EnsureTenantAccess(context.Context, string, string) error {
	if err := f.step("ensure"); err != nil {
		return err
	}
	if !f.exists {
		return syntheticFailure
	}
	return nil
}
func (f *lifecycleFixture) DeprovisionTenant(context.Context, string, string) error {
	if err := f.step("drop"); err != nil {
		return err
	}
	f.exists = false
	return nil
}
func (f *lifecycleFixture) ProvisionTenant(context.Context, string, string, string) error {
	if err := f.step("create"); err != nil {
		return err
	}
	f.exists = true
	return nil
}

func TestDeletionFailureBoundariesRemainRecoverable(t *testing.T) {
	for _, stage := range []string{"begin-delete", "drop", "finish-delete"} {
		t.Run(stage, func(t *testing.T) {
			f := fixture("active")
			f.fail = stage
			if !errors.Is(removeTenant(context.Background(), f, f, f.row, "requested"), syntheticFailure) {
				t.Fatal("failure was not propagated")
			}
			if stage == "begin-delete" {
				if f.row.State != "active" || !reflect.DeepEqual(f.calls, []string{"begin-delete"}) {
					t.Fatal("external mutation ran before durable intent")
				}
				return
			}
			if f.row.State != "deleting" || f.row.ActiveKeyCount != 0 {
				t.Fatal("pending deletion or key revocation was lost")
			}
			f.fail = ""
			f.calls = nil
			if err := reconcileTenant(context.Background(), f, f, f.row); err != nil {
				t.Fatal(err)
			}
			if f.row.State != "inactive" || f.exists || !reflect.DeepEqual(f.calls, []string{"begin-delete", "drop", "finish-delete"}) {
				t.Fatal("restart did not complete pending cleanup without grants")
			}
		})
	}
}
func TestRepeatedDeleteAndMissingTenantAreNoOps(t *testing.T) {
	for _, row := range []*pg.TenantRecord{nil, fixture("inactive").row} {
		f := fixture("inactive")
		if err := removeTenant(context.Background(), f, f, row, "requested"); err != nil || len(f.calls) != 0 {
			t.Fatal("delete is not idempotent")
		}
	}
}
func TestProvisionFailureBoundariesAndReprovision(t *testing.T) {
	for _, stage := range []string{"prepare", "create", "confirm", "ensure", "activate"} {
		t.Run(stage, func(t *testing.T) {
			f := &lifecycleFixture{fail: stage}
			err := provisionTenant(context.Background(), f, f, "team", "lumen_t_team", "synthetic-password", false, []byte("hash"), "prefix")
			if !errors.Is(err, syntheticFailure) {
				t.Fatal("provision failure was not propagated")
			}
			if stage == "prepare" {
				if f.row != nil || f.exists {
					t.Fatal("username reservation failure changed CH")
				}
				return
			}
			if stage == "create" || stage == "confirm" {
				if f.row.State != "creating" {
					t.Fatal("unconfirmed creation state was lost")
				}
				f.fail = ""
				f.calls = nil
				if err := reconcileTenant(context.Background(), f, f, f.row); !errors.Is(err, ErrTenantCreationUnconfirmed) || len(f.calls) != 0 {
					t.Fatal("startup changed unconfirmed principal ownership")
				}
				return
			}
			if f.row.State != "provisioning" {
				t.Fatal("confirmed ownership milestone missing")
			}

			f.fail = ""
			if err := reconcileTenant(context.Background(), f, f, f.row); err != nil {
				t.Fatal(err)
			}
			if f.row.State != "inactive" || f.exists {
				t.Fatal("restart left incomplete credentials")
			}
			if err := provisionTenant(context.Background(), f, f, "team", "lumen_t_team", "new-password", true, []byte("newhash"), "newprefix"); err != nil {
				t.Fatal(err)
			}
			if f.row.State != "active" || !f.exists || !f.row.StoreIP {
				t.Fatal("inactive tenant could not provision fresh credentials")
			}
		})
	}
}
func TestActiveDuplicateDoesNotMutate(t *testing.T) {
	f := fixture("active")
	if err := provisionTenant(context.Background(), f, f, "team", "lumen_t_team", "password", false, nil, ""); err == nil {
		t.Fatal("active duplicate accepted")
	}
	if !reflect.DeepEqual(f.calls, []string{"get"}) {
		t.Fatal("active principal was changed")
	}
}
func TestUnownedExistingUserRemainsBlocked(t *testing.T) {
	f := &lifecycleFixture{exists: true}
	if err := provisionTenant(context.Background(), f, f, "team", "lumen_t_team", "password", false, nil, ""); err == nil {
		t.Fatal("unowned principal accepted")
	}
	f.calls = nil
	if f.row.State != "creating" {
		t.Fatal("unconfirmed ownership was lost")
	}
	if err := reconcileTenant(context.Background(), f, f, f.row); !errors.Is(err, ErrTenantCreationUnconfirmed) || len(f.calls) != 0 {
		t.Fatal("startup deleted an unowned principal")
	}
}
func TestLegacyClassificationRequiresExplicitRecoveryForRevokedKeys(t *testing.T) {
	for _, exists := range []bool{false, true} {
		for _, keys := range []int64{0, 2} {
			f := fixture("legacy")
			f.exists = exists
			f.row.KeyCount = keys
			f.row.ActiveKeyCount = 0
			if err := reconcileTenant(context.Background(), f, f, f.row); !errors.Is(err, ErrLegacyRepairRequired) || len(f.calls) != 0 || f.row.State != "legacy" {
				t.Fatal("legacy deletion was inferred without operator evidence")
			}
		}
	}
	f := fixture("legacy")
	if err := reconcileTenant(context.Background(), f, f, f.row); err != nil || f.row.State != "active" || !reflect.DeepEqual(f.calls, []string{"ensure", "activate-legacy"}) {
		t.Fatal("active legacy grants must succeed before activation")
	}
	f = fixture("legacy")
	f.exists = false
	if err := reconcileTenant(context.Background(), f, f, f.row); err == nil || f.row.State != "legacy" {
		t.Fatal("missing active legacy principal was silently skipped")
	}
}
func TestActiveMissingUserFailsAndInactiveIsNeverGranted(t *testing.T) {
	f := fixture("active")
	f.exists = false
	if err := reconcileTenant(context.Background(), f, f, f.row); err == nil {
		t.Fatal("active missing user was skipped")
	}
	f = fixture("inactive")
	if err := reconcileTenant(context.Background(), f, f, f.row); err != nil || len(f.calls) != 0 {
		t.Fatal("inactive user was reactivated")
	}
}

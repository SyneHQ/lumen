package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

type failingRows struct {
	pgx.Rows
	failure error
	closed  bool
}

func (r *failingRows) Next() bool { return false }
func (r *failingRows) Err() error { return r.failure }
func (r *failingRows) Close()     { r.closed = true }

type rowsDatabase struct {
	queryer
	rows pgx.Rows
}

func (d rowsDatabase) Query(context.Context, string, ...any) (pgx.Rows, error) { return d.rows, nil }
func TestIterationErrorsDoNotBecomeEmptySuccessfulResults(t *testing.T) {
	for _, kind := range []string{"tenants", "keys"} {
		sentinel := errors.New("synthetic row iteration error")
		rows := &failingRows{failure: sentinel}
		s := &Store{db: rowsDatabase{rows: rows}}
		var err error
		if kind == "tenants" {
			_, err = s.ListTenants(context.Background())
		} else {
			_, err = s.ListTeamKeys(context.Background(), "team")
		}
		if !errors.Is(err, sentinel) || !rows.closed {
			t.Fatal("iteration failure or row cleanup was lost")
		}
	}
}

package ch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTenantDeletionDropsUserBeforeRestrictionsAndPropagatesEveryError(t *testing.T) {
	for failure := -1; failure < 6; failure++ {
		calls := []string{}
		sentinel := errors.New("synthetic boundary failure")
		exec := func(_ context.Context, query string, _ ...any) error {
			calls = append(calls, query)
			if len(calls)-1 == failure {
				return sentinel
			}
			return nil
		}
		err := removeTenantAccess(context.Background(), exec, "team", "lumen_t_team")
		if !strings.HasPrefix(calls[0], "DROP USER IF EXISTS ") {
			t.Fatal("restrictions removed while principal remains")
		}
		if failure >= 0 {
			if !errors.Is(err, sentinel) || len(calls) != failure+1 {
				t.Fatal("cleanup failure ignored or later mutation executed")
			}
		} else if err != nil || len(calls) != 6 {
			t.Fatal("complete cleanup failed")
		}
	}
}
func TestTenantDeletionRejectsInvalidUserBeforeSQL(t *testing.T) {
	for _, user := range []string{"", "other_user", "lumen_t_x; DROP USER admin", "lumen_t_x`"} {
		called := false
		err := removeTenantAccess(context.Background(), func(context.Context, string, ...any) error { called = true; return nil }, "team", user)
		if err == nil || called {
			t.Fatal("invalid tenant identifier reached SQL")
		}
	}
}

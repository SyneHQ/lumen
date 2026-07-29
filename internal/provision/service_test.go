package provision

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	lumenv1 "github.com/SyneHQ/lumen/gen/lumen/v1"
)

func TestAuthorizeAdmin(t *testing.T) {
	adminSvc := NewAdminService(nil, nil, "secret-token", "localhost", 9000)

	// Case 1: Unauthorized missing header
	req1 := connect.NewRequest(&lumenv1.ProvisionRequest{TeamId: "team_123"})
	_, err := adminSvc.Provision(context.Background(), req1)
	if err == nil {
		t.Errorf("Expected error for missing authorization header")
	}

	// Case 2: Unauthorized wrong token
	req2 := connect.NewRequest(&lumenv1.ProvisionRequest{TeamId: "team_123"})
	req2.Header().Set("Authorization", "Bearer wrong-token")
	_, err = adminSvc.Provision(context.Background(), req2)
	if err == nil {
		t.Errorf("Expected error for invalid authorization token")
	}
}

func TestSanitizeSlug(t *testing.T) {
	slug1 := sanitizeSlug("team-abc-123!")
	expected1 := "team_abc_123_"

	if slug1 != expected1 {
		t.Errorf("Expected sanitized slug '%s', got '%s'", expected1, slug1)
	}
}

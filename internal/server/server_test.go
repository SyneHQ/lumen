package server

import (
	"github.com/SyneHQ/lumen/internal/auth"
	"github.com/SyneHQ/lumen/internal/config"
	"github.com/SyneHQ/lumen/internal/ingest"
	"github.com/SyneHQ/lumen/internal/provision"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRPCRejectsOversizedBodyBeforeServiceExecution(t *testing.T) {
	a, err := auth.NewAuthenticator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(&config.Config{}, a, &ingest.Service{}, &provision.AdminService{})
	for _, item := range []struct {
		path  string
		admin bool
	}{
		{"/lumen.v1.IngestService/Track", false},
		{"/lumen.v1.AdminService/Provision", true},
	} {
		req := httptest.NewRequest("POST", item.path, strings.NewReader(`{"event":"`+strings.Repeat("x", 3<<20)+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		if item.admin {
			s.adminServer.Handler.ServeHTTP(w, req)
		} else {
			s.ingestServer.Handler.ServeHTTP(w, req)
		}
		if w.Code != 429 && w.Code != 413 {
			t.Fatalf("%s: oversized input returned %d: %s", item.path, w.Code, w.Body.String())
		}
	}
}

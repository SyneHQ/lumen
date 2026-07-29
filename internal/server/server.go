// Package server sets up HTTP/2 Connect RPC listeners and health endpoints.
package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/SyneHQ/lumen/gen/lumen/v1/lumenv1connect"
	"github.com/SyneHQ/lumen/internal/auth"
	"github.com/SyneHQ/lumen/internal/config"
	"github.com/SyneHQ/lumen/internal/ingest"
	"github.com/SyneHQ/lumen/internal/provision"
)

// Server encapsulates HTTP servers for public ingest and internal admin endpoints.
type Server struct {
	cfg          *config.Config
	ingestServer *http.Server
	adminServer  *http.Server
	healthServer *http.Server
}

// NewServer initializes the Connect handlers, CORS middleware, and HTTP servers.
func NewServer(cfg *config.Config, authenticator *auth.Authenticator, ingestSvc *ingest.Service, adminSvc *provision.AdminService) *Server {
	// 1. Setup Public Ingest Handler (Connect + gRPC + gRPC-Web) with Auth Interceptor
	authInterceptor := authenticator.NewAuthInterceptor()
	ingestPath, ingestHandler := lumenv1connect.NewIngestServiceHandler(
		ingestSvc,
		connect.WithInterceptors(authInterceptor),
	)

	ingestMux := http.NewServeMux()
	ingestMux.Handle(ingestPath, corsMiddleware(ingestHandler))

	ingestServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.IngestPort),
		Handler:      h2c.NewHandler(ingestMux, &http2.Server{}),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	// 2. Setup Internal Admin Handler
	adminPath, adminHandler := lumenv1connect.NewAdminServiceHandler(adminSvc)

	adminMux := http.NewServeMux()
	adminMux.Handle(adminPath, adminHandler)

	adminServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.AdminPort),
		Handler:      h2c.NewHandler(adminMux, &http2.Server{}),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	// 3. Setup Metrics & Health Check Server
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	healthServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.MetricsPort),
		Handler: healthMux,
	}

	return &Server{
		cfg:          cfg,
		ingestServer: ingestServer,
		adminServer:  adminServer,
		healthServer: healthServer,
	}
}

// Start launches the public ingest, admin, and health HTTP servers.
func (s *Server) Start() error {
	errChan := make(chan error, 3)

	go func() {
		if err := s.ingestServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("ingest server error: %w", err)
		}
	}()

	go func() {
		if err := s.adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("admin server error: %w", err)
		}
	}()

	go func() {
		if err := s.healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("health server error: %w", err)
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

// Shutdown gracefully stops all running servers.
func (s *Server) Shutdown(ctx context.Context) error {
	_ = s.ingestServer.Shutdown(ctx)
	_ = s.adminServer.Shutdown(ctx)
	_ = s.healthServer.Shutdown(ctx)
	return nil
}

// corsMiddleware adds standard CORS headers allowing cross-origin Connect/gRPC-Web calls from browsers.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Connect-Protocol-Version, x-lumen-key, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Grpc-Status, Grpc-Message, Grpc-Accept-Encoding")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

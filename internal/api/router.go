package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/buildinfo"
	"stackpilot/internal/domain"
	"stackpilot/internal/events"
	"stackpilot/internal/incident"
	"stackpilot/internal/logs"
	"stackpilot/internal/orchestrator"
	"stackpilot/internal/security"
	"stackpilot/internal/workspace"
	webassets "stackpilot/web"
)

const apiVersion = "v1"

type traceIDContextKey struct{}

type incidentStore interface {
	List(context.Context, domain.WorkspaceID, int) ([]incident.Record, error)
	Get(context.Context, domain.IncidentID) (*incident.Record, error)
	ListAnalyses(context.Context, domain.IncidentID, int) ([]incident.Analysis, error)
}

// Config contains HTTP boundary dependencies and advertised capabilities.
type Config struct {
	BuildInfo        buildinfo.Info
	Capabilities     []string
	Readiness        func(context.Context) bool
	Logger           *slog.Logger
	Workspaces       *workspace.Manager
	WorkspaceImports *workspace.ImportService
	EventStore       events.Store
	EventBroker      *events.Broker
	EventHeartbeat   time.Duration
	LogManager       *logs.Manager
	LogScopes        logs.ScopeResolver
	LogBroker        *logs.Broker
	LogHeartbeat     time.Duration
	SingleService    *orchestrator.SingleService
	Auth             Authenticator
	Audit            security.AuditStore
	Secrets          security.SecretProvider
	Incidents        incidentStore
}

// NewHandler constructs the API router and embedded Web console handler.
func NewHandler(config Config) (http.Handler, error) {
	assets, err := webassets.Dist()
	if err != nil {
		return nil, err
	}
	spa, err := NewSPAHandler(assets)
	if err != nil {
		return nil, err
	}
	return newRouter(config, spa), nil
}

func newRouter(config Config, spa http.Handler) http.Handler {
	router := chi.NewRouter()
	router.Use(traceMiddleware)
	router.Use(recoveryMiddleware(config.Logger))
	router.Get("/health/live", liveHandler)
	router.Get("/health/ready", readyHandler(config.Readiness))
	router.Get("/version", versionHandler(config))
	router.Mount("/api/v1", apiV1Router(config))
	router.MethodNotAllowed(methodNotAllowedHandler)
	router.NotFound(spa.ServeHTTP)
	return router
}

func apiV1Router(config Config) http.Handler {
	router := chi.NewRouter()
	if config.Auth != nil {
		registerAuthRoutes(router, config.Auth)
		registerTokenRotationRoute(router, config.Auth, config.Audit, config.Logger)
		router.Group(func(protected chi.Router) {
			protected.Use(authenticationMiddleware(config.Auth))
			registerBusinessRoutes(protected, config)
		})
	} else {
		registerBusinessRoutes(router, config)
	}
	router.MethodNotAllowed(methodNotAllowedHandler)
	router.NotFound(func(response http.ResponseWriter, request *http.Request) {
		writeRegisteredError(response, request, ErrorResourceNotFound)
	})
	return router
}

func registerBusinessRoutes(router chi.Router, config Config) {
	if config.Audit != nil {
		registerAuditRoutes(router, config.Audit)
	}
	if config.Secrets != nil {
		registerSecretRoutes(router, config.Secrets, config.Auth, config.Audit, config.Logger)
	}
	if config.Workspaces != nil {
		registerWorkspaceRoutes(router, config.Workspaces, config.WorkspaceImports, config.SingleService, config.Auth, config.Audit, config.Logger)
	}
	if config.WorkspaceImports != nil {
		registerWorkspaceImportRoutes(router, config.WorkspaceImports, config.Auth, config.Audit, config.Logger)
	}
	if config.EventStore != nil && config.EventBroker != nil {
		router.Get("/events", eventsHandler(config.EventStore, config.EventBroker, config.EventHeartbeat))
	}
	if config.LogManager != nil && config.LogScopes != nil && config.LogBroker != nil {
		registerLogRoutes(router, config.LogManager, config.LogScopes, config.LogBroker, config.LogHeartbeat)
	}
	if config.Workspaces != nil && config.SingleService != nil {
		registerOperationRoutes(router, config.Workspaces, config.SingleService, config.Auth, config.Audit, config.Logger)
	}
	if config.Incidents != nil {
		registerIncidentRoutes(router, config.Incidents, config.SingleService, config.Auth, config.Audit, config.Logger)
	}
}

func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		traceID, err := newTraceID()
		if err != nil {
			traceID = "tr_unavailable"
		}
		response.Header().Set("X-Trace-ID", traceID)
		ctx := context.WithValue(request.Context(), traceIDContextKey{}, traceID)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func recoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					traceID := traceIDFromContext(request.Context())
					if logger != nil {
						logger.Error("http handler panic", "trace_id", traceID, "error_code", ErrorInternal)
					}
					writeRegisteredError(response, request, ErrorInternal)
				}
			}()
			next.ServeHTTP(response, request)
		})
	}
}

func newTraceID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate trace ID: %w", err)
	}
	return "tr_" + hex.EncodeToString(value), nil
}

func traceIDFromContext(ctx context.Context) string {
	traceID, _ := ctx.Value(traceIDContextKey{}).(string)
	if traceID == "" {
		return "tr_unavailable"
	}
	return traceID
}

func methodNotAllowedHandler(response http.ResponseWriter, request *http.Request) {
	writeRegisteredError(response, request, ErrorMethodNotAllowed)
}

type healthResponse struct {
	Status string `json:"status"`
}

func liveHandler(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, healthResponse{Status: "live"})
}

func readyHandler(readiness func(context.Context) bool) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if readiness == nil || !readiness(request.Context()) {
			response.Header().Set("Retry-After", "1")
			writeRegisteredError(response, request, ErrorHealthNotReady)
			return
		}
		writeJSON(response, http.StatusOK, healthResponse{Status: "ready"})
	}
}

type versionResponse struct {
	Version      string   `json:"version"`
	Commit       string   `json:"commit"`
	BuildTime    string   `json:"buildTime"`
	APIVersion   string   `json:"apiVersion"`
	Capabilities []string `json:"capabilities"`
}

func versionHandler(config Config) http.HandlerFunc {
	capabilities := normalizedCapabilities(config.Capabilities)
	return func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, versionResponse{
			Version:      config.BuildInfo.Version,
			Commit:       config.BuildInfo.Commit,
			BuildTime:    config.BuildInfo.BuildTime,
			APIVersion:   apiVersion,
			Capabilities: capabilities,
		})
	}
}

func normalizedCapabilities(values []string) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

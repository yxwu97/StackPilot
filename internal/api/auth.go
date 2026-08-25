package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"stackpilot/internal/security"
)

const (
	browserCSRFHeader = "X-StackPilot-CSRF"
	sessionCookieName = "stackpilot_session"
)

type authenticationContextKey struct{}

type authenticationKind uint8

const (
	authenticationBearer authenticationKind = iota + 1
	authenticationSession
)

// Authenticator is the API boundary required from the local authentication manager.
type Authenticator interface {
	AuthenticateBearer(context.Context, string) error
	IssueBootstrap() (string, time.Time, error)
	ExchangeBootstrap(string) (security.BrowserCredentials, error)
	AuthenticateSession(string) error
	RefreshSession(string) (security.BrowserCredentials, error)
	ValidateCSRF(string, string) error
	RevokeSession(string)
	Rotate(context.Context) (security.TokenRecord, error)
}

type authentication struct {
	kind         authenticationKind
	sessionValue string
}

type bootstrapResponse struct {
	Code      string `json:"code"`
	ExpiresAt string `json:"expiresAt"`
}

type sessionRequest struct {
	Bootstrap string `json:"bootstrap"`
}

type sessionResponse struct {
	CSRF      string `json:"csrf"`
	ExpiresAt string `json:"expiresAt"`
}

func registerAuthRoutes(router chi.Router, manager Authenticator) {
	router.With(requireBearer(manager)).Post("/auth/bootstrap", issueBootstrapHandler(manager))
	router.With(requireExchangeRequest).Post("/auth/session", exchangeSessionHandler(manager))
	router.With(requireSession(manager)).Get("/auth/session", refreshSessionHandler(manager))
	router.With(requireSession(manager), browserMutationGuard(manager)).Delete("/auth/session", revokeSessionHandler(manager))
}

func registerTokenRotationRoute(router chi.Router, manager Authenticator, audit security.AuditStore, logger *slog.Logger) {
	router.With(authenticationMiddleware(manager), auditMutation(audit, logger, "auth.token.rotate", "auth_token", ""),
		browserMutationGuard(manager)).Post("/auth/token/rotate", rotateTokenHandler(manager))
}

func authenticationMiddleware(manager Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			auth, err := authenticateRequest(request, manager)
			if err != nil {
				writeAuthenticationError(response, request, err)
				return
			}
			ctx := context.WithValue(request.Context(), authenticationContextKey{}, auth)
			next.ServeHTTP(response, request.WithContext(ctx))
		})
	}
}

func authenticateRequest(request *http.Request, manager Authenticator) (authentication, error) {
	authorization := request.Header.Get("Authorization")
	if authorization != "" {
		token, ok := parseBearer(authorization)
		if !ok {
			return authentication{}, security.ErrAuthenticationFailed
		}
		if err := manager.AuthenticateBearer(request.Context(), token); err != nil {
			return authentication{}, err
		}
		return authentication{kind: authenticationBearer}, nil
	}
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return authentication{}, security.ErrSessionInvalid
	}
	if err := manager.AuthenticateSession(cookie.Value); err != nil {
		return authentication{}, err
	}
	return authentication{kind: authenticationSession, sessionValue: cookie.Value}, nil
}

func requireBearer(manager Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			token, ok := parseBearer(request.Header.Get("Authorization"))
			if !ok {
				writeRegisteredError(response, request, ErrorAuthenticationRequired)
				return
			}
			if err := manager.AuthenticateBearer(request.Context(), token); err != nil {
				writeAuthenticationError(response, request, err)
				return
			}
			ctx := context.WithValue(request.Context(), authenticationContextKey{}, authentication{kind: authenticationBearer})
			next.ServeHTTP(response, request.WithContext(ctx))
		})
	}
}

func requireSession(manager Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			cookie, err := request.Cookie(sessionCookieName)
			if err != nil {
				writeRegisteredError(response, request, ErrorSessionInvalid)
				return
			}
			if err := manager.AuthenticateSession(cookie.Value); err != nil {
				writeAuthenticationError(response, request, err)
				return
			}
			ctx := context.WithValue(request.Context(), authenticationContextKey{}, authentication{
				kind: authenticationSession, sessionValue: cookie.Value,
			})
			next.ServeHTTP(response, request.WithContext(ctx))
		})
	}
}

func issueBootstrapHandler(manager Authenticator) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		code, expiresAt, err := manager.IssueBootstrap()
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		writeJSON(response, http.StatusCreated, bootstrapResponse{Code: code, ExpiresAt: formatAPITime(expiresAt)})
	}
}

func exchangeSessionHandler(manager Authenticator) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		var input sessionRequest
		if err := decodeJSONRequest(response, request, &input); err != nil || strings.TrimSpace(input.Bootstrap) == "" {
			writeRegisteredError(response, request, ErrorRequestValidationFailed)
			return
		}
		credentials, err := manager.ExchangeBootstrap(input.Bootstrap)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		setSessionCookie(response, credentials)
		writeJSON(response, http.StatusCreated, mapSession(credentials))
	}
}

func refreshSessionHandler(manager Authenticator) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		auth, _ := request.Context().Value(authenticationContextKey{}).(authentication)
		credentials, err := manager.RefreshSession(auth.sessionValue)
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		setSessionCookie(response, credentials)
		writeJSON(response, http.StatusOK, mapSession(credentials))
	}
}

func revokeSessionHandler(manager Authenticator) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		auth, _ := request.Context().Value(authenticationContextKey{}).(authentication)
		manager.RevokeSession(auth.sessionValue)
		http.SetCookie(response, expiredSessionCookie())
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(http.StatusNoContent)
	}
}

func rotateTokenHandler(manager Authenticator) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		record, err := manager.Rotate(request.Context())
		if err != nil {
			writeBoundaryError(response, request, err)
			return
		}
		auth, _ := request.Context().Value(authenticationContextKey{}).(authentication)
		if auth.kind == authenticationSession {
			http.SetCookie(response, expiredSessionCookie())
		}
		writeJSON(response, http.StatusOK, map[string]string{"createdAt": formatAPITime(record.CreatedAt)})
	}
}

func requireExchangeRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !hasExactOrigin(request) || !hasJSONContentType(request) {
			writeRegisteredError(response, request, ErrorBrowserRequestRejected)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func browserMutationGuard(manager Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if manager == nil {
			return next
		}
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			auth, ok := request.Context().Value(authenticationContextKey{}).(authentication)
			if ok && auth.kind == authenticationBearer {
				next.ServeHTTP(response, request)
				return
			}
			if !ok || auth.kind != authenticationSession || !hasExactOrigin(request) || !hasJSONContentType(request) ||
				manager.ValidateCSRF(auth.sessionValue, request.Header.Get(browserCSRFHeader)) != nil {
				writeRegisteredError(response, request, ErrorBrowserRequestRejected)
				return
			}
			next.ServeHTTP(response, request)
		})
	}
}

func parseBearer(value string) (string, bool) {
	parts := strings.Split(value, " ")
	returnValue := ""
	if len(parts) == 2 && parts[0] == "Bearer" && parts[1] != "" {
		returnValue = parts[1]
	}
	return returnValue, returnValue != ""
}

func hasExactOrigin(request *http.Request) bool {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Scheme != scheme || origin.Host != request.Host || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	hostname := origin.Hostname()
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func hasJSONContentType(request *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]), "application/json")
}

func setSessionCookie(response http.ResponseWriter, credentials security.BrowserCredentials) {
	http.SetCookie(response, sessionCookie(credentials.Session, credentials.ExpiresAt, credentials.Remaining))
}

func mapSession(credentials security.BrowserCredentials) sessionResponse {
	return sessionResponse{CSRF: credentials.CSRF, ExpiresAt: formatAPITime(credentials.ExpiresAt)}
}

func sessionCookie(value string, expiresAt time.Time, remaining time.Duration) *http.Cookie {
	if remaining <= 0 {
		return expiredSessionCookie()
	}
	maxAge := int((remaining + time.Second - 1) / time.Second)
	return &http.Cookie{
		Name: sessionCookieName, Value: value, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: expiresAt.UTC(),
	}
}

func expiredSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC(),
	}
}

func writeAuthenticationError(response http.ResponseWriter, request *http.Request, err error) {
	if errors.Is(err, security.ErrAuthenticationFailed) {
		writeRegisteredError(response, request, ErrorAuthenticationRequired)
		return
	}
	if errors.Is(err, security.ErrSessionInvalid) {
		writeRegisteredError(response, request, ErrorSessionInvalid)
		return
	}
	writeRegisteredError(response, request, ErrorInternal)
}

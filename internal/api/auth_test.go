package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/security"
)

func TestAuthenticationRoutesBootstrapExchangeRefreshAndRevoke(t *testing.T) {
	auth := newStubAuthenticator()
	handler := newRouter(Config{Auth: auth}, newTestSPAHandler(t))

	unauthorized := performRequest(handler, http.MethodPost, "/api/v1/auth/bootstrap")
	assertErrorCode(t, unauthorized, http.StatusUnauthorized, ErrorAuthenticationRequired)

	bootstrapRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", nil)
	bootstrapRequest.Header.Set("Authorization", "Bearer token")
	bootstrapResponse := httptest.NewRecorder()
	handler.ServeHTTP(bootstrapResponse, bootstrapRequest)
	if bootstrapResponse.Code != http.StatusCreated || !strings.Contains(bootstrapResponse.Body.String(), auth.bootstrap) {
		t.Fatalf("bootstrap response = (%d, %q)", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}

	malicious := newSessionExchangeRequest(auth.bootstrap, "http://malicious.example")
	maliciousResponse := httptest.NewRecorder()
	handler.ServeHTTP(maliciousResponse, malicious)
	assertErrorCode(t, maliciousResponse, http.StatusForbidden, ErrorBrowserRequestRejected)

	rebinding := newSessionExchangeRequest(auth.bootstrap, "http://malicious.example")
	rebinding.Host = "malicious.example"
	rebindingResponse := httptest.NewRecorder()
	handler.ServeHTTP(rebindingResponse, rebinding)
	assertErrorCode(t, rebindingResponse, http.StatusForbidden, ErrorBrowserRequestRejected)

	exchange := newSessionExchangeRequest(auth.bootstrap, "http://127.0.0.1")
	exchangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(exchangeResponse, exchange)
	if exchangeResponse.Code != http.StatusCreated || !strings.Contains(exchangeResponse.Body.String(), auth.csrf) {
		t.Fatalf("exchange response = (%d, %q)", exchangeResponse.Code, exchangeResponse.Body.String())
	}
	if strings.Contains(exchangeResponse.Body.String(), `"revision"`) || exchangeResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange contract = body %q, cache %q", exchangeResponse.Body.String(), exchangeResponse.Header().Get("Cache-Control"))
	}
	cookies := exchangeResponse.Result().Cookies()
	assertActiveSessionCookie(t, cookies, auth.session, auth.now.Add(30*time.Minute), 1800)

	refresh := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	refresh.AddCookie(cookies[0])
	refreshResponse := httptest.NewRecorder()
	handler.ServeHTTP(refreshResponse, refresh)
	if refreshResponse.Code != http.StatusOK || !strings.Contains(refreshResponse.Body.String(), auth.refreshedCSRF) {
		t.Fatalf("refresh response = (%d, %q)", refreshResponse.Code, refreshResponse.Body.String())
	}
	refreshedCookies := refreshResponse.Result().Cookies()
	assertActiveSessionCookie(t, refreshedCookies, auth.session, auth.now.Add(30*time.Minute), 1800)

	revoke := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1/api/v1/auth/session", nil)
	revoke.Header.Set("Origin", "http://127.0.0.1")
	revoke.Header.Set("Content-Type", "application/json")
	revoke.Header.Set(browserCSRFHeader, auth.refreshedCSRF)
	revoke.AddCookie(refreshedCookies[0])
	revokeResponse := httptest.NewRecorder()
	handler.ServeHTTP(revokeResponse, revoke)
	if revokeResponse.Code != http.StatusNoContent || !auth.revoked {
		t.Fatalf("revoke response = (%d, revoked=%t)", revokeResponse.Code, auth.revoked)
	}
	assertExpiredSessionCookie(t, revokeResponse.Result().Cookies())
}

func TestSessionCookieUsesAuthoritativeExpiryAndCeilingSeconds(t *testing.T) {
	expiresAt := time.Date(2026, 8, 19, 2, 0, 1, 100_000_000, time.UTC)
	cookie := sessionCookie("session", expiresAt, 1100*time.Millisecond)
	if cookie.MaxAge != 2 || !cookie.Expires.Equal(expiresAt) {
		t.Fatalf("sessionCookie() = %+v", cookie)
	}
	assertExpiredSessionCookie(t, []*http.Cookie{sessionCookie("session", expiresAt, 0)})
}

func TestBrowserTokenRotationExpiresSessionCookie(t *testing.T) {
	auth := newStubAuthenticator()
	handler := newRouter(Config{Auth: auth}, newTestSPAHandler(t))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/auth/token/rotate", nil)
	request.Header.Set("Origin", "http://127.0.0.1")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(browserCSRFHeader, auth.csrf)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("browser token rotation = (%d, %q)", response.Code, response.Body.String())
	}
	assertExpiredSessionCookie(t, response.Result().Cookies())
}

func TestBusinessRoutesRequireBearerOrSession(t *testing.T) {
	auth := newStubAuthenticator()
	handler := newWorkspaceAPIHandlerWithAuth(t, auth)

	unauthenticated := performRequest(handler, http.MethodGet, "/api/v1/workspaces")
	assertErrorCode(t, unauthenticated, http.StatusUnauthorized, ErrorSessionInvalid)

	bearer := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	bearer.Header.Set("Authorization", "Bearer token")
	bearerResponse := httptest.NewRecorder()
	handler.ServeHTTP(bearerResponse, bearer)
	if bearerResponse.Code != http.StatusOK {
		t.Fatalf("bearer business response = (%d, %q)", bearerResponse.Code, bearerResponse.Body.String())
	}

	session := httptest.NewRequest(http.MethodGet, "/api/v1/workspaces", nil)
	session.AddCookie(&http.Cookie{Name: sessionCookieName, Value: auth.session})
	sessionResponse := httptest.NewRecorder()
	handler.ServeHTTP(sessionResponse, session)
	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("session business response = (%d, %q)", sessionResponse.Code, sessionResponse.Body.String())
	}
}

func TestTokenRotationAcceptsBearerAndReturnsOnlyMetadata(t *testing.T) {
	auth := newStubAuthenticator()
	handler := newRouter(Config{Auth: auth}, newTestSPAHandler(t))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token/rotate", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"createdAt"`) ||
		strings.Contains(response.Body.String(), "local-next") || strings.Contains(response.Body.String(), "token") {
		t.Fatalf("token rotation response = (%d, %q)", response.Code, response.Body.String())
	}
}

func newSessionExchangeRequest(bootstrap, origin string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/v1/auth/session", strings.NewReader(`{"bootstrap":"`+bootstrap+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	return request
}

type stubAuthenticator struct {
	bootstrap     string
	session       string
	csrf          string
	refreshedCSRF string
	revoked       bool
	now           time.Time
}

func newStubAuthenticator() *stubAuthenticator {
	return &stubAuthenticator{
		bootstrap: strings.Repeat("b", 43), session: strings.Repeat("s", 43),
		csrf: strings.Repeat("c", 43), refreshedCSRF: strings.Repeat("r", 43),
		now: time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC),
	}
}

func (auth *stubAuthenticator) AuthenticateBearer(_ context.Context, token string) error {
	if token != "token" {
		return security.ErrAuthenticationFailed
	}
	return nil
}

func (auth *stubAuthenticator) IssueBootstrap() (string, time.Time, error) {
	return auth.bootstrap, time.Now().UTC().Add(time.Minute), nil
}

func (auth *stubAuthenticator) ExchangeBootstrap(code string) (security.BrowserCredentials, error) {
	if code != auth.bootstrap {
		return security.BrowserCredentials{}, security.ErrBootstrapInvalid
	}
	return security.BrowserCredentials{
		Session: auth.session, CSRF: auth.csrf, ExpiresAt: auth.now.Add(30 * time.Minute),
		Remaining: 30 * time.Minute,
	}, nil
}

func (auth *stubAuthenticator) AuthenticateSession(session string) error {
	if session != auth.session || auth.revoked {
		return security.ErrSessionInvalid
	}
	return nil
}

func (auth *stubAuthenticator) RefreshSession(session string) (security.BrowserCredentials, error) {
	if err := auth.AuthenticateSession(session); err != nil {
		return security.BrowserCredentials{}, err
	}
	auth.csrf = auth.refreshedCSRF
	return security.BrowserCredentials{
		Session: auth.session, CSRF: auth.csrf, ExpiresAt: auth.now.Add(30 * time.Minute),
		Remaining: 30 * time.Minute,
	}, nil
}

func (auth *stubAuthenticator) ValidateCSRF(session, csrf string) error {
	if err := auth.AuthenticateSession(session); err != nil || csrf != auth.csrf {
		return security.ErrCSRFInvalid
	}
	return nil
}

func (auth *stubAuthenticator) RevokeSession(session string) {
	auth.revoked = session == auth.session
}

func (auth *stubAuthenticator) Rotate(context.Context) (security.TokenRecord, error) {
	auth.revoked = true
	return security.TokenRecord{ID: "local-next", CreatedAt: time.Now().UTC()}, nil
}

var _ Authenticator = (*stubAuthenticator)(nil)

func assertActiveSessionCookie(t *testing.T, cookies []*http.Cookie, value string, expiresAt time.Time, maxAge int) {
	t.Helper()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %+v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || cookie.Value != value || cookie.Path != "/" || !cookie.HttpOnly ||
		cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != maxAge || !cookie.Expires.Equal(expiresAt) {
		t.Fatalf("active session cookie = %+v", cookie)
	}
}

func assertExpiredSessionCookie(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	if len(cookies) != 1 {
		t.Fatalf("expired session cookies = %+v", cookies)
	}
	cookie := cookies[0]
	if cookie.Name != sessionCookieName || cookie.Value != "" || cookie.Path != "/" || !cookie.HttpOnly ||
		cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != -1 || !cookie.Expires.Before(time.Now()) {
		t.Fatalf("expired session cookie = %+v", cookie)
	}
}

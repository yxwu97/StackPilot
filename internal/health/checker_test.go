package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"stackpilot/internal/driver"
	"stackpilot/internal/logs"
)

func TestProcessCheckerMapsRunningExitAndIdentityMismatch(t *testing.T) {
	identity := testIdentity()
	cases := []struct {
		name        string
		observation driver.RuntimeObservation
		err         error
		wantSuccess bool
		wantCode    ErrorCode
	}{
		{name: "running", observation: driver.RuntimeObservation{State: "running", Identity: identity}, wantSuccess: true},
		{name: "exited", observation: driver.RuntimeObservation{State: "exited", Identity: identity}, wantCode: CodeProcessExited},
		{name: "not found", err: driver.ErrRuntimeNotFound, wantCode: CodeProcessExited},
		{name: "identity mismatch", err: driver.ErrIdentityMismatch, wantCode: CodeProcessIdentityMismatch},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			checker := NewChecker(fakeInspector{observation: test.observation, err: test.err}, nil)
			result := checker.Check(context.Background(), ResolvedSpec{Kind: KindProcess, Identity: identity})
			if result.Success != test.wantSuccess || result.ErrorCode != test.wantCode {
				t.Fatalf("Check() = %#v", result)
			}
		})
	}
}

func TestTCPCheckerConnectsAndClassifiesRefusal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	checker := NewChecker(nil, nil)
	result := checker.Check(context.Background(), ResolvedSpec{Kind: KindTCP, Host: "127.0.0.1", Port: port})
	if !result.Success {
		t.Fatalf("running listener result = %#v", result)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	result = checker.Check(context.Background(), ResolvedSpec{Kind: KindTCP, Host: "127.0.0.1", Port: port})
	if result.ErrorCode != CodeTCPRefused {
		t.Fatalf("closed listener result = %#v", result)
	}
}

func TestNetworkCheckerStopsWhenManagedProcessExits(t *testing.T) {
	checker := NewChecker(fakeInspector{err: driver.ErrRuntimeNotFound}, nil)
	result := checker.Check(context.Background(), ResolvedSpec{
		Kind: KindTCP, Host: "127.0.0.1", Port: 12345, Identity: testIdentity(),
	})
	if result.Success || result.ErrorCode != CodeProcessExited {
		t.Fatalf("TCP process-exit result = %#v", result)
	}
	checker = NewChecker(fakeInspector{err: driver.ErrIdentityMismatch}, nil)
	result = checker.Check(context.Background(), ResolvedSpec{
		Kind: KindHTTP, URL: "http://127.0.0.1:12345/health", Identity: testIdentity(),
	})
	if result.Success || result.ErrorCode != CodeProcessIdentityMismatch {
		t.Fatalf("HTTP identity-mismatch result = %#v", result)
	}
}

func TestHTTPCheckerBoundsRedactsAndMapsStatus(t *testing.T) {
	secret := "Authorization: Bearer super-secret"
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(response, secret+strings.Repeat("x", maxHTTPBodyBytes))
	}))
	defer server.Close()
	redactor, err := logs.NewDefaultRedactor(nil)
	if err != nil {
		t.Fatalf("new redactor: %v", err)
	}
	result := NewChecker(nil, redactor).Check(context.Background(), ResolvedSpec{Kind: KindHTTP, URL: server.URL})
	if result.Success || result.ErrorCode != CodeHTTPStatusMismatch || result.StatusCode == nil || *result.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Check() = %#v", result)
	}
	if len(result.Summary) > maxSummaryBytes || strings.Contains(result.Summary, "super-secret") || !strings.Contains(result.Summary, "[REDACTED:authorization]") {
		t.Fatalf("unsafe HTTP summary = %q", result.Summary)
	}
}

func TestHTTPCheckerAcceptsTwoHundredsAndDoesNotFollowCrossHostRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/redirect", func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "http://example.com/health", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	checker := NewChecker(nil, nil)
	if result := checker.Check(context.Background(), ResolvedSpec{Kind: KindHTTP, URL: server.URL + "/ok"}); !result.Success {
		t.Fatalf("2xx result = %#v", result)
	}
	result := checker.Check(context.Background(), ResolvedSpec{Kind: KindHTTP, URL: server.URL + "/redirect"})
	if result.ErrorCode != CodeHTTPStatusMismatch || result.StatusCode == nil || *result.StatusCode != http.StatusFound {
		t.Fatalf("redirect result = %#v", result)
	}
}

func TestHTTPCheckerHonorsTimeoutAndRejectsUnsafeTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	checker := NewChecker(nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := checker.Check(ctx, ResolvedSpec{Kind: KindHTTP, URL: server.URL})
	if result.ErrorCode != CodeHTTPTimeout {
		t.Fatalf("timeout result = %#v", result)
	}
	result = checker.Check(context.Background(), ResolvedSpec{Kind: KindHTTP, URL: "http://192.0.2.1/health"})
	if result.ErrorCode != CodeHTTPStatusMismatch {
		t.Fatalf("unsafe target result = %#v", result)
	}
}

func TestSafeURLAcceptsOnlyLoopbackHTTPTargets(t *testing.T) {
	valid := []string{"http://127.0.0.1:8080/health", "http://localhost/", "http://[::1]:8080/"}
	for _, value := range valid {
		if _, err := safeURL(value); err != nil {
			t.Errorf("safeURL(%q) error = %v", value, err)
		}
	}
	invalid := []string{"ftp://127.0.0.1/x", "http://user:pass@127.0.0.1/x", "http://example.com/x", "http://127.0.0.1:70000/x"}
	for _, value := range invalid {
		if _, err := safeURL(value); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("safeURL(%q) error = %v", value, err)
		}
	}
}

func TestComposeCheckerMapsHealthyAndUnhealthyResults(t *testing.T) {
	checker := NewCheckerWithCompose(nil, fakeComposeInspector{ready: true, summary: "all healthy"}, nil)
	result := checker.Check(context.Background(), ResolvedSpec{Kind: KindCompose, ComposeIdentity: "opaque"})
	if !result.Success || result.ErrorCode != "" || result.Summary != "all healthy" {
		t.Fatalf("healthy Compose result = %#v", result)
	}
	checker = NewCheckerWithCompose(nil, fakeComposeInspector{}, nil)
	result = checker.Check(context.Background(), ResolvedSpec{Kind: KindCompose, ComposeIdentity: "opaque"})
	if result.Success || result.ErrorCode != CodeContainerUnhealthy {
		t.Fatalf("unhealthy Compose result = %#v", result)
	}
}

type fakeInspector struct {
	observation driver.RuntimeObservation
	err         error
}

type fakeComposeInspector struct {
	ready   bool
	summary string
	err     error
}

func (inspector fakeComposeInspector) CheckCompose(context.Context, string) (bool, string, error) {
	return inspector.ready, inspector.summary, inspector.err
}

func (inspector fakeInspector) Inspect(context.Context, driver.RuntimeIdentity) (driver.RuntimeObservation, error) {
	return inspector.observation, inspector.err
}

func testIdentity() driver.RuntimeIdentity {
	return driver.RuntimeIdentity{
		PID: 42, StartedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC), ExecutablePath: `C:\Java\java.exe`,
		CommandDigest: strings.Repeat("a", 64), PlatformToken: "opaque",
	}
}

package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"stackpilot/internal/driver"
)

// DefaultChecker dispatches Phase 1 checks and owns the hardened HTTP client boundary.
type DefaultChecker struct {
	inspector Inspector
	compose   ComposeInspector
	redactor  Redactor
}

// NewChecker constructs a checker. A nil redactor omits response bodies from summaries.
func NewChecker(inspector Inspector, redactor Redactor) *DefaultChecker {
	return &DefaultChecker{inspector: inspector, redactor: redactor}
}

// NewCheckerWithCompose constructs a checker with process and Compose adapters.
func NewCheckerWithCompose(inspector Inspector, compose ComposeInspector, redactor Redactor) *DefaultChecker {
	return &DefaultChecker{inspector: inspector, compose: compose, redactor: redactor}
}

// Check performs one non-retrying check.
func (checker *DefaultChecker) Check(ctx context.Context, spec ResolvedSpec) Result {
	started := time.Now()
	result := Result{Kind: spec.Kind, CheckedAt: started.UTC()}
	if spec.Kind != KindProcess && spec.Identity.PID > 0 && !checker.processAlive(ctx, spec, &result) {
		result.Duration = time.Since(started)
		return result
	}
	switch spec.Kind {
	case KindProcess:
		checker.checkProcess(ctx, spec, &result)
	case KindTCP:
		checkTCP(ctx, spec, &result)
	case KindHTTP:
		checker.checkHTTP(ctx, spec, &result)
	case KindCompose:
		checker.checkCompose(ctx, spec, &result)
	default:
		result.Summary = "unsupported health check"
	}
	result.Duration = time.Since(started)
	result.Summary = truncateUTF8(result.Summary, maxSummaryBytes)
	return result
}

func (checker *DefaultChecker) checkCompose(ctx context.Context, spec ResolvedSpec, result *Result) {
	if checker.compose == nil {
		fail(result, CodeContainerUnhealthy, "Compose health inspector is unavailable")
		return
	}
	ready, summary, err := checker.compose.CheckCompose(ctx, spec.ComposeIdentity)
	if err != nil || !ready {
		fail(result, CodeContainerUnhealthy, "One or more managed Compose containers are not healthy")
		return
	}
	result.Success = true
	result.Summary = truncateUTF8(summary, maxSummaryBytes)
}

func (checker *DefaultChecker) processAlive(ctx context.Context, spec ResolvedSpec, result *Result) bool {
	processResult := Result{}
	checker.checkProcess(ctx, spec, &processResult)
	if processResult.Success {
		return true
	}
	result.ErrorCode = processResult.ErrorCode
	result.Summary = processResult.Summary
	return false
}

func (checker *DefaultChecker) checkProcess(ctx context.Context, spec ResolvedSpec, result *Result) {
	if checker.inspector == nil {
		fail(result, CodeProcessIdentityMismatch, "process inspector is unavailable")
		return
	}
	observation, err := checker.inspector.Inspect(ctx, spec.Identity)
	if err == nil && observation.State == "running" && observation.ExitCode == nil {
		result.Success, result.Summary = true, "managed process identity is running"
		return
	}
	if errors.Is(err, driver.ErrIdentityMismatch) {
		fail(result, CodeProcessIdentityMismatch, "managed process identity does not match")
		return
	}
	if err == nil || errors.Is(err, driver.ErrRuntimeNotFound) {
		fail(result, CodeProcessExited, "managed process exited before readiness")
		return
	}
	if ctx.Err() != nil {
		result.Summary = "process check cancelled"
		return
	}
	fail(result, CodeProcessIdentityMismatch, "managed process identity could not be verified")
}

func checkTCP(ctx context.Context, spec ResolvedSpec, result *Result) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(spec.Host, strconv.Itoa(spec.Port)))
	if err == nil {
		result.Success, result.Summary = true, "TCP connection succeeded"
		if closeErr := connection.Close(); closeErr != nil {
			result.Summary = "TCP connection succeeded; connection close reported an error"
		}
		return
	}
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Summary = "TCP check cancelled"
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ETIMEDOUT) || isTimeout(err) {
		fail(result, CodeTCPTimeout, "TCP connection timed out")
		return
	}
	fail(result, CodeTCPRefused, "TCP connection was refused")
}

func (checker *DefaultChecker) checkHTTP(ctx context.Context, spec ResolvedSpec, result *Result) {
	target, err := safeURL(spec.URL)
	if err != nil {
		fail(result, CodeHTTPStatusMismatch, "HTTP target is unsafe")
		return
	}
	client := localHTTPClient(target)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		fail(result, CodeHTTPStatusMismatch, "HTTP request is invalid")
		return
	}
	response, err := client.Do(request)
	if err != nil {
		checker.mapHTTPError(ctx, err, result)
		return
	}
	defer response.Body.Close()
	status := response.StatusCode
	result.StatusCode = &status
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxHTTPBodyBytes+1))
	if readErr != nil {
		checker.mapHTTPError(ctx, readErr, result)
		return
	}
	result.Success = status >= http.StatusOK && status < http.StatusMultipleChoices
	if !result.Success {
		result.ErrorCode = CodeHTTPStatusMismatch
	}
	result.Summary = checker.httpSummary(status, body)
}

func (checker *DefaultChecker) mapHTTPError(ctx context.Context, err error, result *Result) {
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Summary = "HTTP check cancelled"
		return
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		fail(result, CodeHTTPTimeout, "HTTP request timed out")
		return
	}
	fail(result, CodeHTTPBodyMismatch, "HTTP response could not be read safely")
}

func (checker *DefaultChecker) httpSummary(status int, body []byte) string {
	prefix := fmt.Sprintf("HTTP status %d", status)
	truncated := len(body) > maxHTTPBodyBytes
	if truncated {
		body = body[:maxHTTPBodyBytes]
	}
	if checker.redactor == nil || len(body) == 0 || !utf8.Valid(body) {
		return prefix
	}
	redacted, err := checker.redactor.Redact(string(body))
	if err != nil {
		return prefix + "; response summary unavailable"
	}
	redacted = strings.Join(strings.Fields(redacted), " ")
	if redacted == "" {
		return prefix
	}
	suffix := "; body: " + truncateUTF8(redacted, maxSummaryBytes-len(prefix)-16)
	if truncated {
		suffix += " [truncated]"
	}
	return prefix + suffix
}

func safeURL(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	if err != nil || target.User != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, ErrInvalidSpec
	}
	if !isLoopbackName(target.Hostname()) || target.Fragment != "" {
		return nil, ErrInvalidSpec
	}
	if _, err := effectivePort(target); err != nil {
		return nil, ErrInvalidSpec
	}
	return target, nil
}

func localHTTPClient(target *url.URL) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           safeDialer(target),
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: 0,
	}
	return &http.Client{Transport: transport, CheckRedirect: sameTargetRedirect(target)}
}

func safeDialer(target *url.URL) func(context.Context, string, string) (net.Conn, error) {
	allowedPort, _ := effectivePort(target)
	allowedHost := strings.ToLower(target.Hostname())
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(host, allowedHost) || port != allowedPort {
			return nil, fmt.Errorf("%w: HTTP dial target changed", ErrInvalidSpec)
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("resolve local HTTP target")
		}
		for _, resolved := range addresses {
			if !resolved.IP.IsLoopback() {
				return nil, fmt.Errorf("%w: HTTP target resolved outside loopback", ErrInvalidSpec)
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
}

func sameTargetRedirect(target *url.URL) func(*http.Request, []*http.Request) error {
	wantPort, _ := effectivePort(target)
	return func(request *http.Request, via []*http.Request) error {
		port, err := effectivePort(request.URL)
		if len(via) >= 10 || err != nil || !strings.EqualFold(request.URL.Hostname(), target.Hostname()) || port != wantPort {
			return http.ErrUseLastResponse
		}
		return nil
	}
}

func effectivePort(target *url.URL) (string, error) {
	if value := target.Port(); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return "", ErrInvalidSpec
		}
		return value, nil
	}
	if target.Scheme == "http" {
		return "80", nil
	}
	if target.Scheme == "https" {
		return "443", nil
	}
	return "", ErrInvalidSpec
}

func isLoopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isTimeout(err error) bool {
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func fail(result *Result, code ErrorCode, summary string) {
	result.Success, result.ErrorCode, result.Summary = false, code, summary
}

func truncateUTF8(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	if len(value) <= maximum {
		return value
	}
	for maximum > 0 && !utf8.RuneStart(value[maximum]) {
		maximum--
	}
	return value[:maximum]
}

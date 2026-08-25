package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultServerURL = "http://127.0.0.1:32100"
	maximumResponse  = 8 << 20
)

var errCommandConfig = errors.New("CLI command configuration is invalid")

type apiClient struct {
	baseURL string
	token   []byte
	http    *http.Client
}

type apiError struct {
	Status  int
	Code    string
	Message string
	TraceID string
}

func (value *apiError) Error() string {
	if value.Code == "" {
		return fmt.Sprintf("StackPilot API returned HTTP %d", value.Status)
	}
	return value.Code + ": " + value.Message
}

func newAPIClient(baseURL string, token []byte) (*apiClient, error) {
	validated, err := validateServerURL(baseURL)
	if err != nil {
		return nil, err
	}
	if len(token) == 0 {
		return nil, fmt.Errorf("local authentication token is empty")
	}
	return &apiClient{baseURL: validated, token: append([]byte(nil), token...), http: &http.Client{
		Timeout: 30 * time.Second, CheckRedirect: rejectRedirect,
	}}, nil
}

func (client *apiClient) Close() {
	erase(client.token)
}

func (client *apiClient) JSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		defer erase(encoded)
		body = bytes.NewReader(encoded)
	}
	response, err := client.do(ctx, method, path, body, input != nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := decodeAPIStatus(response); err != nil {
		return err
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maximumResponse))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode StackPilot response: %w", err)
	}
	return nil
}

func (client *apiClient) Stream(ctx context.Context, path string) (*http.Response, error) {
	streamClient := *client.http
	streamClient.Timeout = 0
	response, err := client.doWith(ctx, &streamClient, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, err
	}
	if err := decodeAPIStatus(response); err != nil {
		response.Body.Close()
		return nil, err
	}
	return response, nil
}

func (client *apiClient) do(ctx context.Context, method, path string, body io.Reader, jsonBody bool) (*http.Response, error) {
	return client.doWith(ctx, client.http, method, path, body, jsonBody)
}

func (client *apiClient) doWith(ctx context.Context, httpClient *http.Client, method, path string, body io.Reader, jsonBody bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	authorization := "Bearer " + string(client.token)
	request.Header.Set("Authorization", authorization)
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete {
		request.Header.Set("Idempotency-Key", newIdempotencyKey())
	}
	if jsonBody {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := httpClient.Do(request)
	request.Header.Del("Authorization")
	authorization = ""
	if err != nil {
		return nil, fmt.Errorf("StackPilot server unavailable: %w", err)
	}
	return response, nil
}

func decodeAPIStatus(response *http.Response) error {
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	var envelope struct {
		Error struct {
			Code, Message, TraceID string
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&envelope)
	return &apiError{Status: response.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message, TraceID: envelope.Error.TraceID}
}

func validateServerURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("server must be an HTTP loopback origin")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() || parsed.Port() == "" {
		return "", fmt.Errorf("server must use a loopback IP address and explicit port")
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func exitCodeFor(err error) int {
	if errors.Is(err, errCommandConfig) {
		return 2
	}
	var apiFailure *apiError
	if errors.As(err, &apiFailure) {
		if apiFailure.Status == http.StatusUnauthorized || strings.HasPrefix(apiFailure.Code, "AUTH_") {
			return 5
		}
		if apiFailure.Status == http.StatusConflict {
			return 6
		}
		if apiFailure.Status == http.StatusBadRequest || apiFailure.Status == http.StatusUnprocessableEntity {
			return 2
		}
		return 4
	}
	return 3
}

func commandErrorf(format string, values ...any) error {
	return fmt.Errorf("%w: %s", errCommandConfig, fmt.Sprintf(format, values...))
}

func erase(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

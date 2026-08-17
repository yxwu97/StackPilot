package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPAHandlerServesIndexAssetAndRouteFallback(t *testing.T) {
	handler := newTestSPAHandler(t)
	tests := []struct {
		name        string
		path        string
		status      int
		body        string
		cachePolicy string
	}{
		{name: "root", path: "/", status: http.StatusOK, body: "StackPilot", cachePolicy: "no-cache"},
		{name: "asset", path: "/assets/app.js", status: http.StatusOK, body: "console.log", cachePolicy: "immutable"},
		{name: "client route", path: "/systems/btc", status: http.StatusOK, body: "StackPilot", cachePolicy: "no-cache"},
		{name: "missing asset", path: "/assets/missing.js", status: http.StatusNotFound, body: "404 page not found"},
		{name: "directory", path: "/assets", status: http.StatusOK, body: "StackPilot", cachePolicy: "no-cache"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.status || !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("response = (%d, %q), want (%d, body containing %q)", response.Code, response.Body.String(), test.status, test.body)
			}
			if test.cachePolicy != "" && !strings.Contains(response.Header().Get("Cache-Control"), test.cachePolicy) {
				t.Fatalf("Cache-Control = %q, want %q", response.Header().Get("Cache-Control"), test.cachePolicy)
			}
		})
	}
}

func TestSPAHandlerRejectsUnsupportedMethodAndTraversal(t *testing.T) {
	handler := newTestSPAHandler(t)

	postResponse := httptest.NewRecorder()
	handler.ServeHTTP(postResponse, httptest.NewRequest(http.MethodPost, "/", nil))
	if postResponse.Code != http.StatusMethodNotAllowed || postResponse.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST response = (%d, Allow %q), want 405 with Allow header", postResponse.Code, postResponse.Header().Get("Allow"))
	}

	traversalResponse := httptest.NewRecorder()
	handler.ServeHTTP(traversalResponse, httptest.NewRequest(http.MethodGet, "http://stackpilot.local/%2e%2e/private", nil))
	if traversalResponse.Code != http.StatusNotFound {
		t.Fatalf("traversal status = %d, want 404", traversalResponse.Code)
	}
}

func newTestSPAHandler(t *testing.T) *SPAHandler {
	t.Helper()
	assets := fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("<title>StackPilot</title>")},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('ready')")},
	}
	handler, err := NewSPAHandler(fs.FS(assets))
	if err != nil {
		t.Fatalf("NewSPAHandler() error = %v", err)
	}
	return handler
}

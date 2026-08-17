// Package api owns StackPilot HTTP routing and transport behavior.
package api

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	webassets "stackpilot/web"
)

// SPAHandler serves immutable frontend assets and route fallback responses.
type SPAHandler struct {
	assets fs.FS
	index  []byte
}

// NewHandler constructs the HTTP handler from the compiled web distribution.
func NewHandler() (http.Handler, error) {
	assets, err := webassets.Dist()
	if err != nil {
		return nil, err
	}
	return NewSPAHandler(assets)
}

// NewSPAHandler constructs a frontend handler from the supplied asset filesystem.
func NewSPAHandler(assets fs.FS) (*SPAHandler, error) {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("read embedded web index: %w", err)
	}
	return &SPAHandler{assets: assets, index: index}, nil
}

// ServeHTTP serves static files and falls back to index.html for client routes.
func (h *SPAHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	assetPath, valid := cleanAssetPath(request.URL.Path)
	if !valid {
		http.NotFound(response, request)
		return
	}
	if assetPath == "" {
		h.serveIndex(response, request)
		return
	}

	served, err := h.serveAsset(response, request, assetPath)
	if err != nil {
		http.Error(response, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if served {
		return
	}
	if path.Ext(assetPath) != "" {
		http.NotFound(response, request)
		return
	}
	h.serveIndex(response, request)
}

func (h *SPAHandler) serveAsset(response http.ResponseWriter, request *http.Request, name string) (bool, error) {
	info, err := fs.Stat(h.assets, name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat web asset %q: %w", name, err)
	}
	if info.IsDir() {
		return false, nil
	}

	contents, err := fs.ReadFile(h.assets, name)
	if err != nil {
		return false, fmt.Errorf("read web asset %q: %w", name, err)
	}
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(response, request, name, time.Time{}, bytes.NewReader(contents))
	return true, nil
}

func (h *SPAHandler) serveIndex(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(response, request, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func cleanAssetPath(requestPath string) (string, bool) {
	for _, segment := range strings.Split(requestPath, "/") {
		if segment == ".." {
			return "", false
		}
	}

	cleaned := strings.TrimPrefix(path.Clean("/"+requestPath), "/")
	if cleaned == "" || cleaned == "." {
		return "", true
	}
	return cleaned, fs.ValidPath(cleaned)
}

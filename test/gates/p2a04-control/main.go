// Command p2a04-control performs authenticated loopback requests for the P2A-04 Windows Gate.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"stackpilot/internal/security"
)

func main() {
	server := flag.String("server", "", "StackPilot loopback server URL")
	dataDir := flag.String("data-dir", "", "StackPilot data directory")
	method := flag.String("method", http.MethodGet, "HTTP method")
	path := flag.String("path", "", "API path")
	flag.Parse()
	if err := run(*server, *dataDir, *method, *path); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(server, dataDir, method, path string) error {
	base, err := url.Parse(server)
	if err != nil || base.Scheme != "http" || base.Path != "" || !loopback(base.Hostname()) {
		return fmt.Errorf("server must be an HTTP loopback origin")
	}
	if !strings.HasPrefix(path, "/api/v1/") || strings.Contains(path, "..") {
		return fmt.Errorf("path must be below /api/v1")
	}
	store, err := security.NewOSTokenStore(dataDir)
	if err != nil {
		return err
	}
	token, found, err := store.Load()
	if err != nil || !found {
		return fmt.Errorf("load Gate token: %w", err)
	}
	defer clear(token)
	request, err := http.NewRequest(method, server+path, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("request returned %s: %s", response.Status, payload)
	}
	_, err = os.Stdout.Write(payload)
	return err
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

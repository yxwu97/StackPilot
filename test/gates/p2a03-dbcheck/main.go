// p2a03-dbcheck verifies the isolated Secret launch projection used by the Windows Gate.
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type result struct {
	Count           int    `json:"count"`
	EnvironmentName string `json:"environmentName"`
	SystemID        string `json:"systemId"`
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	Version         int64  `json:"version"`
	SafeReference   bool   `json:"safeReference"`
}

func main() {
	databasePath := flag.String("db", "", "absolute isolated Gate database path")
	flag.Parse()
	if err := run(*databasePath); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("absolute database path is required")
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer database.Close()
	value := result{}
	err = database.QueryRow(`SELECT COUNT(*), environment_name, system_id, name, provider, version
        FROM service_instance_secret_versions`).Scan(&value.Count, &value.EnvironmentName, &value.SystemID, &value.Name, &value.Provider, &value.Version)
	if err != nil {
		return fmt.Errorf("query Secret launch projection: %w", err)
	}
	if value.Count != 1 || value.EnvironmentName != "STACKPILOT_GATE_SECRET" || value.SystemID != "p2a-secret" ||
		value.Name != "gate-key" || value.Provider != "dpapi-file" || value.Version != 1 {
		return fmt.Errorf("unexpected Secret launch projection")
	}
	var canonicalJSON string
	if err := database.QueryRow(`SELECT spec_json FROM resolved_system_specs LIMIT 1`).Scan(&canonicalJSON); err != nil {
		return fmt.Errorf("query safe resolved spec: %w", err)
	}
	value.SafeReference = strings.Contains(canonicalJSON, `${secret.gate-key}`)
	if !value.SafeReference {
		return fmt.Errorf("resolved spec omitted safe Secret reference")
	}
	return json.NewEncoder(os.Stdout).Encode(value)
}

//go:build windows

package supervisorspike

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func writeJSONAtomic(path string, value any) (err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create identity directory: %w", err)
	}
	temporary := path + ".tmp"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary identity file: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close identity file: %w", closeErr))
			}
		}
		if err != nil {
			_ = os.Remove(temporary)
		}
	}()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(value); err != nil {
		return fmt.Errorf("encode identity file: %w", err)
	}
	if err = file.Sync(); err != nil {
		return fmt.Errorf("flush identity file: %w", err)
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close identity file before rename: %w", err)
	}
	closed = true
	if err = os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish identity file: %w", err)
	}
	return nil
}

func readJSON(path string, target any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(contents, target); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

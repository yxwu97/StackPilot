//go:build windows

package security

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stackpilot/internal/domain"
)

const maximumProtectedSecretSize = 256 * 1024

var secretEntropy = []byte("StackPilot/secret/v1")

type osSecretStore struct {
	directory string
}

// NewOSSecretProvider constructs the current-user DPAPI provider below DATA_DIR/secrets.
func NewOSSecretProvider(dataDir string, metadata SecretMetadataStore, clock func() time.Time) (SecretProvider, error) {
	store, err := newOSSecretStore(dataDir)
	if err != nil {
		return nil, err
	}
	return newSecretProvider(store, metadata, clock)
}

func newOSSecretStore(dataDir string) (*osSecretStore, error) {
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return nil, fmt.Errorf("absolute data directory is required for Secret storage")
	}
	directory := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create Secret directory: %w", err)
	}
	if err := protectCurrentUserPath(directory, "Secret"); err != nil {
		return nil, err
	}
	canonical, err := CanonicalExistingPath(directory)
	if err != nil {
		return nil, err
	}
	return &osSecretStore{directory: canonical}, nil
}

func (store *osSecretStore) Load(ctx context.Context, key SecretKey) (protectedSecret, bool, error) {
	if err := ctx.Err(); err != nil {
		return protectedSecret{}, false, err
	}
	path := store.path(key)
	if err := store.validateFile(path); errors.Is(err, os.ErrNotExist) {
		return protectedSecret{}, false, nil
	} else if err != nil {
		return protectedSecret{}, false, err
	}
	protected, err := readBoundedSecretFile(path)
	if err != nil {
		return protectedSecret{}, false, err
	}
	defer zeroBytes(protected)
	plaintext, err := unprotectCurrentUserData(protected, secretEntropy, "Secret")
	if err != nil {
		return protectedSecret{}, false, err
	}
	defer zeroBytes(plaintext)
	record, err := decodeProtectedSecret(plaintext)
	if err != nil {
		return protectedSecret{}, false, err
	}
	if err := validateProtectedSecret(record, key); err != nil {
		zeroBytes(record.Value)
		return protectedSecret{}, false, err
	}
	return record, true, nil
}

func (store *osSecretStore) Save(ctx context.Context, record protectedSecret) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := SecretKey{SystemID: domain.SystemID(record.SystemID), Name: record.Name}
	if err := validateProtectedSecret(record, key); err != nil {
		return err
	}
	plaintext, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode protected Secret: %w", err)
	}
	defer zeroBytes(plaintext)
	protected, err := protectCurrentUserData(plaintext, secretEntropy, "Secret")
	if err != nil {
		return err
	}
	defer zeroBytes(protected)
	return store.saveProtected(record, protected)
}

func (store *osSecretStore) saveProtected(record protectedSecret, protected []byte) (err error) {
	file, err := os.CreateTemp(store.directory, ".secret-*.tmp")
	if err != nil {
		return fmt.Errorf("create protected Secret temporary file: %w", err)
	}
	temporary := file.Name()
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(temporary)
		}
	}()
	if err = protectCurrentUserPath(temporary, "Secret"); err != nil {
		return err
	}
	if err = writeProtectedSecret(file, protected); err != nil {
		return err
	}
	target := store.path(SecretKey{SystemID: domain.SystemID(record.SystemID), Name: record.Name})
	if err = replaceProtectedFile(temporary, target, "Secret"); err != nil {
		return err
	}
	return protectCurrentUserPath(target, "Secret")
}

func (store *osSecretStore) Delete(ctx context.Context, key SecretKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path := store.path(key)
	if err := store.validateFile(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("delete protected Secret: %w", err)
	}
	return nil
}

func (store *osSecretStore) path(key SecretKey) string {
	digest := sha256.Sum256([]byte(key.SystemID.String() + "\x00" + key.Name))
	return filepath.Join(store.directory, hex.EncodeToString(digest[:])+".dpapi")
}

func (store *osSecretStore) validateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("protected Secret path is not a regular file")
	}
	canonical, err := CanonicalExistingPath(path)
	if err != nil {
		return err
	}
	inside, err := PathWithinRoot(store.directory, canonical)
	if err != nil || !inside || !strings.EqualFold(filepath.Dir(canonical), store.directory) {
		return fmt.Errorf("protected Secret path escaped its directory")
	}
	return nil
}

func readBoundedSecretFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open protected Secret: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maximumProtectedSecretSize+1))
	if err != nil {
		return nil, fmt.Errorf("read protected Secret: %w", err)
	}
	if len(payload) == 0 || len(payload) > maximumProtectedSecretSize {
		return nil, fmt.Errorf("protected Secret size is invalid")
	}
	return payload, nil
}

func decodeProtectedSecret(payload []byte) (protectedSecret, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var record protectedSecret
	if err := decoder.Decode(&record); err != nil {
		return protectedSecret{}, fmt.Errorf("decode protected Secret: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		zeroBytes(record.Value)
		return protectedSecret{}, fmt.Errorf("protected Secret has extra JSON data")
	}
	return record, nil
}

func writeProtectedSecret(file *os.File, protected []byte) error {
	if _, err := file.Write(protected); err != nil {
		return fmt.Errorf("write protected Secret: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush protected Secret: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close protected Secret: %w", err)
	}
	return nil
}

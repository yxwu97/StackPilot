//go:build windows

package security

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maximumProtectedTokenSize = 16 * 1024

var tokenEntropy = []byte("StackPilot/local-auth/v1")

type osTokenStore struct {
	directory string
	path      string
}

// NewOSTokenStore constructs the current-user DPAPI token store below DATA_DIR/auth.
func NewOSTokenStore(dataDir string) (TokenStore, error) {
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return nil, fmt.Errorf("absolute data directory is required for token storage")
	}
	directory := filepath.Join(dataDir, "auth")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create auth directory: %w", err)
	}
	if err := protectCurrentUserPath(directory, "auth"); err != nil {
		return nil, err
	}
	return &osTokenStore{directory: directory, path: filepath.Join(directory, "token.dpapi")}, nil
}

func (store *osTokenStore) Load() ([]byte, bool, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open protected token: %w", err)
	}
	defer file.Close()
	protected, err := io.ReadAll(io.LimitReader(file, maximumProtectedTokenSize+1))
	if err != nil {
		return nil, false, fmt.Errorf("read protected token: %w", err)
	}
	if len(protected) == 0 || len(protected) > maximumProtectedTokenSize {
		return nil, false, fmt.Errorf("protected token size is invalid")
	}
	token, err := unprotectToken(protected)
	zeroBytes(protected)
	if err != nil {
		return nil, false, err
	}
	return token, true, nil
}

func (store *osTokenStore) Save(token []byte) (err error) {
	if len(token) < 32 || len(token) > 1024 {
		return fmt.Errorf("local token size is invalid")
	}
	protected, err := protectToken(token)
	if err != nil {
		return err
	}
	defer zeroBytes(protected)
	file, err := os.CreateTemp(store.directory, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("create protected token temporary file: %w", err)
	}
	temporary := file.Name()
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(temporary)
		}
	}()
	if err = protectCurrentUserPath(temporary, "auth"); err != nil {
		return err
	}
	if err = writeProtectedToken(file, protected); err != nil {
		return err
	}
	if err = replaceProtectedFile(temporary, store.path, "local token"); err != nil {
		return err
	}
	return protectCurrentUserPath(store.path, "auth")
}

func writeProtectedToken(file *os.File, protected []byte) error {
	if _, err := file.Write(protected); err != nil {
		return fmt.Errorf("write protected token: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush protected token: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close protected token: %w", err)
	}
	return nil
}

func replaceProtectedFile(fromPath, toPath, kind string) error {
	from, err := windows.UTF16PtrFromString(fromPath)
	if err != nil {
		return fmt.Errorf("encode temporary %s path: %w", kind, err)
	}
	to, err := windows.UTF16PtrFromString(toPath)
	if err != nil {
		return fmt.Errorf("encode %s path: %w", kind, err)
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("publish protected %s: %w", kind, err)
	}
	return nil
}

func protectToken(token []byte) ([]byte, error) {
	return protectCurrentUserData(token, tokenEntropy, "local token")
}

func unprotectToken(protected []byte) ([]byte, error) {
	return unprotectCurrentUserData(protected, tokenEntropy, "local token")
}

func protectCurrentUserData(value, entropyValue []byte, kind string) ([]byte, error) {
	input, entropy := dataBlob(value), dataBlob(entropyValue)
	var output windows.DataBlob
	if err := windows.CryptProtectData(&input, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, fmt.Errorf("protect %s with DPAPI: %w", kind, err)
	}
	return copyAndFreeBlob(output), nil
}

func unprotectCurrentUserData(value, entropyValue []byte, kind string) ([]byte, error) {
	input, entropy := dataBlob(value), dataBlob(entropyValue)
	var output windows.DataBlob
	if err := windows.CryptUnprotectData(&input, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output); err != nil {
		return nil, fmt.Errorf("unprotect %s with DPAPI: %w", kind, err)
	}
	return copyAndFreeBlob(output), nil
}

func dataBlob(value []byte) windows.DataBlob {
	if len(value) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(value)), Data: &value[0]}
}

func copyAndFreeBlob(blob windows.DataBlob) []byte {
	if blob.Data == nil || blob.Size == 0 {
		return nil
	}
	result := append([]byte(nil), unsafe.Slice(blob.Data, int(blob.Size))...)
	_, _ = windows.LocalFree(windows.Handle(uintptr(unsafe.Pointer(blob.Data))))
	return result
}

func protectCurrentUserPath(path, kind string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("open current token for %s ACL: %w", kind, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("read current account for %s ACL: %w", kind, err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		return fmt.Errorf("build %s file ACL: %w", kind, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read %s file ACL: %w", kind, err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("protect %s path ACL: %w", kind, err)
	}
	return nil
}

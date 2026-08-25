//go:build windows

package logs

import (
	"fmt"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDecodeLogLineUsesWindowsOEMCodePage(t *testing.T) {
	t.Parallel()

	want := "终止批处理操作"
	encoded, err := encodeWindowsOEM(want)
	if err != nil {
		t.Fatalf("encode OEM fixture: %v", err)
	}
	if got := decodeLogLine(encoded); got != want {
		t.Fatalf("decodeLogLine(OEM) = %q, want %q", got, want)
	}
}

func encodeWindowsOEM(value string) ([]byte, error) {
	wide := utf16.Encode([]rune(value))
	procedure := windows.NewLazySystemDLL("kernel32.dll").NewProc("WideCharToMultiByte")
	length, _, callErr := procedure.Call(windowsOEMCodePage, 0, uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)), 0, 0, 0, 0)
	if length == 0 {
		return nil, fmt.Errorf("size OEM output: %w", callErr)
	}
	encoded := make([]byte, length)
	written, _, callErr := procedure.Call(windowsOEMCodePage, 0, uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)), uintptr(unsafe.Pointer(&encoded[0])), length, 0, 0)
	if written != length {
		return nil, fmt.Errorf("write OEM output: wrote %d of %d: %w", written, length, callErr)
	}
	return encoded, nil
}

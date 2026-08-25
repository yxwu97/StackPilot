//go:build windows

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"

	"stackpilot/internal/security"
)

func readHiddenSecret(input io.Reader, prompt io.Writer) ([]byte, bool, error) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, false, nil
	}
	handle := windows.Handle(file.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) != nil {
		return nil, false, nil
	}
	if _, err := fmt.Fprint(prompt, "Secret: "); err != nil {
		return nil, true, err
	}
	if err := windows.SetConsoleMode(handle, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return nil, true, fmt.Errorf("disable console echo: %w", err)
	}
	defer windows.SetConsoleMode(handle, mode)
	value, err := bufio.NewReader(io.LimitReader(file, security.MaximumSecretValueSize+3)).ReadBytes('\n')
	_, _ = fmt.Fprintln(prompt)
	value = trimInputNewline(value)
	if err != nil && err != io.EOF {
		erase(value)
		return nil, true, fmt.Errorf("read hidden Secret: %w", err)
	}
	if len(value) == 0 || len(value) > security.MaximumSecretValueSize {
		erase(value)
		return nil, true, commandErrorf("Secret input must contain 1 through %d bytes", security.MaximumSecretValueSize)
	}
	return value, true, nil
}

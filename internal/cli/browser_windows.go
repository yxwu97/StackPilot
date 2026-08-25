//go:build windows

package cli

import (
	"fmt"
	"os/exec"
)

func openBrowser(target string) error {
	command := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", target)
	if err := command.Start(); err != nil {
		return fmt.Errorf("start system URL handler: %w", err)
	}
	return command.Process.Release()
}

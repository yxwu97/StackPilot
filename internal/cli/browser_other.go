//go:build !windows

package cli

import "fmt"

func openBrowser(string) error {
	return fmt.Errorf("opening the Web console is not enabled on this platform")
}

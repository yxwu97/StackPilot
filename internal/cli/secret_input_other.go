//go:build !windows

package cli

import (
	"io"
	"os"
)

func readHiddenSecret(input io.Reader, _ io.Writer) ([]byte, bool, error) {
	if _, ok := input.(*os.File); ok {
		return nil, true, commandErrorf("hidden interactive Secret input is not enabled on this platform; use redirected stdin")
	}
	return nil, false, nil
}

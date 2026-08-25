// Command supervisor-spike runs the P0-08 Windows supervision experiment.
package main

import (
	"context"
	"os"

	"stackpilot/internal/platform/windows/supervisorspike"
)

func main() {
	os.Exit(supervisorspike.Main(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

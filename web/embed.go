// Package web exposes the compiled web console for embedding in StackPilot.
package web

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed dist
var assets embed.FS

// Dist returns the embedded frontend distribution rooted at its public files.
func Dist() (fs.FS, error) {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, fmt.Errorf("open embedded web distribution: %w", err)
	}
	return dist, nil
}

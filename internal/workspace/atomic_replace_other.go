//go:build !windows

package workspace

import "os"

func atomicReplaceManifest(from, to string, _ bool) error { return os.Rename(from, to) }

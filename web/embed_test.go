package web

import (
	"io/fs"
	"testing"
)

func TestDistIncludesEmbedTarget(t *testing.T) {
	dist, err := Dist()
	if err != nil {
		t.Fatalf("Dist() error = %v", err)
	}

	if _, err := fs.Stat(dist, "embed-placeholder.txt"); err != nil {
		t.Fatalf("embedded placeholder missing: %v", err)
	}
}

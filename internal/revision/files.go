package revision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"stackpilot/internal/security"
)

const (
	DefaultMaxFiles      = 256
	DefaultMaxFileBytes  = 1 << 20
	DefaultMaxTotalBytes = 32 << 20
)

// FileCollector hashes only allowlisted regular files within a canonical workspace.
type FileCollector struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// NewFileCollector returns a collector using the accepted default limits.
func NewFileCollector() *FileCollector {
	return &FileCollector{MaxFiles: DefaultMaxFiles, MaxFileBytes: DefaultMaxFileBytes, MaxTotalBytes: DefaultMaxTotalBytes}
}

// Collect returns sorted digests for default and explicitly registered files.
func (collector *FileCollector) Collect(ctx context.Context, root string, explicit []string) ([]FileFact, error) {
	canonicalRoot, err := security.CanonicalExistingPath(root)
	if err != nil {
		return nil, fmt.Errorf("%w: workspace root", ErrSourceUnsafe)
	}
	candidates, err := collector.candidates(canonicalRoot, explicit)
	if err != nil {
		return nil, err
	}
	return collector.readCandidates(ctx, canonicalRoot, candidates)
}

func (collector *FileCollector) candidates(root string, explicit []string) ([]string, error) {
	set := make(map[string]struct{})
	for _, name := range []string{"pom.xml", "package.json", "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "pyproject.toml", "poetry.lock", "Pipfile.lock", "go.mod", "go.sum"} {
		set[name] = struct{}{}
	}
	patterns := []string{"requirements*.txt", "requirements*.lock"}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, fmt.Errorf("build revision file list: %w", err)
		}
		for _, match := range matches {
			relative, err := filepath.Rel(root, match)
			if err == nil {
				set[filepath.Clean(relative)] = struct{}{}
			}
		}
	}
	for _, relative := range explicit {
		clean, err := validateRelativeFile(relative)
		if err != nil {
			return nil, err
		}
		set[clean] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for relative := range set {
		result = append(result, relative)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	if len(result) > collector.maxFiles() {
		return nil, ErrSourceTooLarge
	}
	return result, nil
}

func (collector *FileCollector) readCandidates(ctx context.Context, root string, candidates []string) ([]FileFact, error) {
	result := make([]FileFact, 0, len(candidates))
	var total int64
	for _, relative := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fact, found, err := collector.readFile(root, relative)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		total += fact.Size
		if total > collector.maxTotalBytes() {
			return nil, ErrSourceTooLarge
		}
		result = append(result, fact)
	}
	return result, nil
}

func (collector *FileCollector) readFile(root, relative string) (FileFact, bool, error) {
	path := filepath.Join(root, relative)
	canonical, err := security.CanonicalExistingPath(path)
	if errors.Is(err, os.ErrNotExist) || os.IsNotExist(err) {
		return FileFact{}, false, nil
	}
	if err != nil {
		return FileFact{}, false, fmt.Errorf("%w: %s", ErrSourceUnsafe, relative)
	}
	inside, err := security.PathWithinRoot(root, canonical)
	if err != nil || !inside {
		return FileFact{}, false, fmt.Errorf("%w: %s", ErrSourceUnsafe, relative)
	}
	file, err := os.Open(canonical)
	if err != nil {
		return FileFact{}, false, fmt.Errorf("read revision file %s: %w", relative, err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > collector.maxFileBytes() {
		if before != nil && before.Size() > collector.maxFileBytes() {
			return FileFact{}, false, ErrSourceTooLarge
		}
		return FileFact{}, false, fmt.Errorf("%w: %s", ErrSourceUnsafe, relative)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, collector.maxFileBytes()+1))
	if err != nil {
		return FileFact{}, false, fmt.Errorf("read revision file %s: %w", relative, err)
	}
	if written != before.Size() {
		return FileFact{}, false, ErrSourceChanged
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return FileFact{}, false, ErrSourceChanged
	}
	return FileFact{Path: filepath.ToSlash(relative), Kind: fileKind(relative), Digest: hex.EncodeToString(hash.Sum(nil)), Size: written}, true, nil
}

func validateRelativeFile(relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", ErrSourceUnsafe
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrSourceUnsafe
	}
	if strings.EqualFold(filepath.Base(clean), ".env") || strings.HasPrefix(strings.ToLower(filepath.Base(clean)), ".env.") {
		return "", ErrSourceUnsafe
	}
	return clean, nil
}

func fileKind(relative string) string {
	name := strings.ToLower(filepath.Base(relative))
	switch {
	case name == "pom.xml":
		return "maven"
	case name == "package.json" || strings.Contains(name, "lock") || name == "npm-shrinkwrap.json":
		return "node"
	case strings.HasPrefix(name, "requirements") || name == "pyproject.toml" || name == "pipfile.lock":
		return "python"
	case name == "go.mod" || name == "go.sum":
		return "go"
	case strings.Contains(name, "compose") && (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")):
		return "compose"
	default:
		return "registered"
	}
}

func (collector *FileCollector) maxFiles() int {
	if collector != nil && collector.MaxFiles > 0 {
		return collector.MaxFiles
	}
	return DefaultMaxFiles
}

func (collector *FileCollector) maxFileBytes() int64 {
	if collector != nil && collector.MaxFileBytes > 0 {
		return collector.MaxFileBytes
	}
	return DefaultMaxFileBytes
}

func (collector *FileCollector) maxTotalBytes() int64 {
	if collector != nil && collector.MaxTotalBytes > 0 {
		return collector.MaxTotalBytes
	}
	return DefaultMaxTotalBytes
}

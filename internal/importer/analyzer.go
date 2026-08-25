package importer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"stackpilot/internal/manifest"
	"stackpilot/internal/security"
)

const (
	maxScriptBytes = 256 << 10
	maxSourceBytes = 1 << 20
	maxScanFiles   = 2000
	maxScanDepth   = 5
)

type Analyzer struct {
	loader    *manifest.Loader
	validator *manifest.Validator
	now       func() time.Time
}

func NewAnalyzer() (*Analyzer, error) {
	loader, err := manifest.NewLoader()
	if err != nil {
		return nil, err
	}
	return &Analyzer{loader: loader, validator: manifest.NewValidatorWithCapabilities("compose", "compose-build", "liveness", "auto-restart", "go"), now: time.Now}, nil
}

func (analyzer *Analyzer) Probe(ctx context.Context, path string) (*ProbeResult, error) {
	root, err := canonicalRoot(path)
	if err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(root, ".stackpilot", "system.yaml")
	if info, statErr := os.Stat(manifestPath); statErr == nil && info.Mode().IsRegular() {
		return &ProbeResult{State: StateReadyToRegister, Path: filepath.Clean(path), Candidates: []ScriptCandidate{}}, nil
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("%w: fixed manifest", ErrPathInvalid)
	}
	candidates, err := scanScripts(ctx, root)
	if err != nil {
		return nil, err
	}
	return &ProbeResult{State: StateInitializationNeeded, Path: filepath.Clean(path), Candidates: candidates}, nil
}

func (analyzer *Analyzer) Analyze(ctx context.Context, rootPath, scriptPath string) (*Draft, error) {
	root, err := canonicalRoot(rootPath)
	if err != nil {
		return nil, err
	}
	relative, absolute, err := resolveScript(root, scriptPath)
	if err != nil {
		return nil, err
	}
	contents, _, err := readScript(ctx, absolute)
	if err != nil {
		return nil, err
	}
	analysis, digest, err := analyzeBATGraph(ctx, root, relative, absolute, contents)
	if err != nil {
		return nil, err
	}
	identity := discoverIdentity(root)
	draft := &Draft{SystemID: identity.id, SystemName: identity.name, Description: identity.description,
		SourceScript: filepath.ToSlash(relative), SourceDigest: digest, AnalyzedAt: analyzer.now().UTC()}
	draft.Candidates, err = analyzer.buildCandidates(ctx, root, identity, analysis)
	if err != nil {
		return nil, err
	}
	return draft, nil
}

func (analyzer *Analyzer) VerifySource(ctx context.Context, rootPath, scriptPath, expectedDigest string) error {
	root, err := canonicalRoot(rootPath)
	if err != nil {
		return err
	}
	relative, absolute, err := resolveScript(root, scriptPath)
	if err != nil {
		return err
	}
	contents, _, err := readScript(ctx, absolute)
	if err != nil {
		return err
	}
	_, digest, err := analyzeBATGraph(ctx, root, relative, absolute, contents)
	if err != nil {
		return err
	}
	if digest != expectedDigest {
		return ErrSourceChanged
	}
	return nil
}

func analyzeBATGraph(ctx context.Context, root, relative, absolute string, contents []byte) (batAnalysis, string, error) {
	result := batAnalysis{}
	state := sourceGraphState{root: root, visiting: map[string]bool{}, digests: map[string]string{}, compose: map[string]bool{}}
	if err := walkBATGraph(ctx, relative, absolute, contents, ".", 0, &state, &result); err != nil {
		return batAnalysis{}, "", err
	}
	keys := make([]string, 0, len(state.digests))
	for key := range state.digests {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = io.WriteString(hash, key+"\x00"+state.digests[key]+"\n")
	}
	return result, fmt.Sprintf("%x", hash.Sum(nil)), nil
}

type sourceGraphState struct {
	root       string
	visiting   map[string]bool
	digests    map[string]string
	compose    map[string]bool
	totalBytes int
}

func walkBATGraph(ctx context.Context, relative, absolute string, contents []byte, initialWorking string, depth int, state *sourceGraphState, result *batAnalysis) error {
	if depth > 8 || len(state.digests) >= 32 {
		return ErrScriptTooLarge
	}
	key := strings.ToLower(filepath.Clean(absolute))
	if state.visiting[key] {
		return ErrReferenceCycle
	}
	if _, seen := state.digests[filepath.ToSlash(relative)]; seen {
		return nil
	}
	state.visiting[key] = true
	defer delete(state.visiting, key)
	if err := state.addSource(absolute, contents); err != nil {
		return err
	}
	analysis, err := parseBAT(filepath.ToSlash(relative), contents, initialWorking)
	if err != nil {
		return err
	}
	result.commands = append(result.commands, analysis.commands...)
	result.findings = append(result.findings, analysis.findings...)
	result.hasCocos = result.hasCocos || analysis.hasCocos
	for _, reference := range analysis.references {
		working, err := resolveWorkingDirectory(state.root, reference.workingDir)
		if err != nil {
			return err
		}
		target, err := resolveReference(state.root, working, reference.path)
		if err != nil {
			return err
		}
		nextContents, _, err := readScript(ctx, target)
		if err != nil {
			return err
		}
		nextRelative, err := filepath.Rel(state.root, target)
		if err != nil {
			return err
		}
		if reference.kind == referenceBAT {
			if err := walkBATGraph(ctx, nextRelative, target, nextContents, reference.workingDir, depth+1, state, result); err != nil {
				return err
			}
		} else if err := walkPS1Graph(ctx, filepath.ToSlash(nextRelative), target, nextContents, reference.workingDir, state, result); err != nil {
			return err
		}
	}
	return nil
}

func walkPS1Graph(ctx context.Context, relative, absolute string, contents []byte, workingDir string, state *sourceGraphState, result *batAnalysis) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.addSource(absolute, contents); err != nil {
		return err
	}
	calls, err := parsePS1(relative, contents, workingDir)
	if err != nil {
		return err
	}
	for _, call := range calls {
		if err := addComposeCall(ctx, call, state, result); err != nil {
			return err
		}
	}
	return nil
}

func addComposeCall(ctx context.Context, call composeCallFact, state *sourceGraphState, result *batAnalysis) error {
	working, err := resolveWorkingDirectory(state.root, call.workingDir)
	if err != nil {
		return err
	}
	target, err := resolveReference(state.root, working, call.file)
	if err != nil {
		return err
	}
	contents, err := readBoundedSource(ctx, target, maxComposeBytes)
	if err != nil {
		return err
	}
	if err := state.addSource(target, contents); err != nil {
		return err
	}
	key := strings.ToLower(filepath.Clean(target))
	if state.compose[key] || !call.build {
		return nil
	}
	relative, _ := filepath.Rel(state.root, target)
	project, err := parseComposeProject(state.root, relative, target, contents, call.build)
	if err != nil {
		return err
	}
	for _, path := range project.BuildFiles {
		contents, err := readBoundedSource(ctx, path, maxComposeBytes)
		if err != nil {
			return err
		}
		if err := state.addSource(path, contents); err != nil {
			return err
		}
	}
	state.compose[key] = true
	result.composeProjects = append(result.composeProjects, project)
	return nil
}

func (state *sourceGraphState) addSource(path string, contents []byte) error {
	relative, err := filepath.Rel(state.root, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return ErrScriptOutside
	}
	key := filepath.ToSlash(relative)
	if _, exists := state.digests[key]; exists {
		return nil
	}
	state.totalBytes += len(contents)
	if state.totalBytes > maxSourceBytes || len(state.digests) >= 32 {
		return ErrScriptTooLarge
	}
	digest := sha256.Sum256(contents)
	state.digests[key] = fmt.Sprintf("%x", digest[:])
	return nil
}

func readBoundedSource(ctx context.Context, path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrScriptNotFound
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > limit {
		return nil, ErrScriptTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	contents = trimBOM(contents)
	if !utf8.Valid(contents) {
		return nil, ErrScriptEncoding
	}
	return contents, nil
}

func canonicalRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrPathInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: absolute path", ErrPathInvalid)
	}
	root, err := manifest.CanonicalWorkspaceRoot(absolute)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPathInvalid, err)
	}
	return root, nil
}

func scanScripts(ctx context.Context, root string) ([]ScriptCandidate, error) {
	return scanScriptsWithLimits(ctx, root, maxScanFiles, maxScanDepth)
}

type scriptScanDirectory struct {
	path     string
	relative string
	depth    int
}

func scanScriptsWithLimits(ctx context.Context, root string, maxEntries, maxDepth int) ([]ScriptCandidate, error) {
	result := make([]ScriptCandidate, 0)
	queue := []scriptScanDirectory{{path: root}}
	visited := 1
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(current.path)
		if err != nil {
			return nil, fmt.Errorf("scan workspace scripts: %w", err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			visited++
			if visited > maxEntries {
				sortScriptCandidates(result)
				return result, nil
			}
			relative := filepath.Join(current.relative, entry.Name())
			if entry.IsDir() {
				if current.depth < maxDepth {
					queue = append(queue, scriptScanDirectory{path: filepath.Join(current.path, entry.Name()), relative: relative, depth: current.depth + 1})
				}
				continue
			}
			if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(entry.Name()), ".bat") {
				continue
			}
			info, err := entry.Info()
			if err == nil && info.Size() <= maxScriptBytes {
				result = append(result, ScriptCandidate{Path: filepath.ToSlash(relative), Size: info.Size()})
			}
		}
	}
	sortScriptCandidates(result)
	return result, nil
}

func sortScriptCandidates(candidates []ScriptCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i].Path) < strings.ToLower(candidates[j].Path)
	})
}

func resolveScript(root, path string) (string, string, error) {
	if filepath.IsAbs(path) || !strings.EqualFold(filepath.Ext(path), ".bat") {
		if filepath.IsAbs(path) {
			return "", "", ErrScriptOutside
		}
		return "", "", ErrScriptType
	}
	absolute, err := security.CanonicalExistingPath(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return "", "", ErrScriptNotFound
	}
	inside, err := security.PathWithinRoot(root, absolute)
	if err != nil || !inside {
		return "", "", ErrScriptOutside
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", ErrScriptNotFound
	}
	relative, err := filepath.Rel(root, absolute)
	return relative, absolute, err
}

func readScript(ctx context.Context, path string) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", ErrScriptNotFound
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxScriptBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(contents) > maxScriptBytes {
		return nil, "", ErrScriptTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	contents = trimBOM(contents)
	if !utf8.Valid(contents) {
		return nil, "", ErrScriptEncoding
	}
	digest := sha256.Sum256(contents)
	return contents, fmt.Sprintf("%x", digest[:]), nil
}

func trimBOM(value []byte) []byte {
	if len(value) >= 3 && value[0] == 0xef && value[1] == 0xbb && value[2] == 0xbf {
		return value[3:]
	}
	return value
}

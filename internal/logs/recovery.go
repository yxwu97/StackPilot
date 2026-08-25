package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"stackpilot/internal/security"
)

type recoveryState struct {
	lastSequence int64
	offsets      map[Stream]int64
	legacy       map[Stream]bool
}

type recoveredSegment struct {
	segment      Segment
	offsets      map[Stream]int64
	legacyOffset bool
}

func (manager *Manager) recoverSegments(ctx context.Context, scope Scope) (recoveryState, error) {
	state, registeredPaths, err := manager.registeredRecoveryState(ctx, scope)
	if err != nil {
		return state, err
	}
	paths, err := manager.recoveryPaths(scope)
	if err != nil {
		return state, err
	}
	for _, path := range paths {
		if err := manager.recoverPath(ctx, scope, path, &state, registeredPaths); err != nil {
			return state, err
		}
	}
	return state, nil
}

func (manager *Manager) registeredRecoveryState(ctx context.Context, scope Scope) (recoveryState, map[string]struct{}, error) {
	state := recoveryState{offsets: map[Stream]int64{StreamStdout: 0, StreamStderr: 0}, legacy: map[Stream]bool{}}
	registered, err := manager.index.ListAfter(ctx, scope.ServiceInstanceID, 0)
	if err != nil {
		return state, nil, fmt.Errorf("list registered log segments: %w", err)
	}
	registeredPaths := make(map[string]struct{}, len(registered))
	for _, segment := range registered {
		registeredPaths[filepath.ToSlash(segment.Path)] = struct{}{}
		if segment.LastSequence > state.lastSequence {
			state.lastSequence = segment.LastSequence
		}
	}
	return state, registeredPaths, nil
}

func (manager *Manager) recoverPath(ctx context.Context, scope Scope, path string, state *recoveryState, registered map[string]struct{}) error {
	recovered, finalPath, err := manager.recoverSegmentFile(scope, path)
	if err != nil || recovered == nil {
		return err
	}
	if recovered.segment.LastSequence > state.lastSequence {
		state.lastSequence = recovered.segment.LastSequence
	}
	stream := recovered.segment.Stream
	if recovered.offsets[stream] > state.offsets[stream] {
		state.offsets[stream] = recovered.offsets[stream]
	}
	state.legacy[stream] = state.legacy[stream] || recovered.legacyOffset
	relative, err := filepath.Rel(manager.dataDir, finalPath)
	if err != nil {
		return fmt.Errorf("resolve recovered log segment: %w", err)
	}
	key := filepath.ToSlash(relative)
	if _, found := registered[key]; found {
		return nil
	}
	if err := manager.index.RegisterClosed(ctx, recovered.segment); err != nil {
		return fmt.Errorf("register recovered log segment: %w", err)
	}
	registered[key] = struct{}{}
	return nil
}

func (manager *Manager) recoveryPaths(scope Scope) ([]string, error) {
	directory := filepath.Join(manager.logsDir, scope.InstanceID.String(), scope.ServiceID.String())
	if _, err := os.Stat(directory); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("inspect log recovery directory: %w", err)
	}
	canonical, err := security.CanonicalExistingPath(directory)
	if err != nil {
		return nil, fmt.Errorf("canonicalize log recovery directory: %w", err)
	}
	inside, err := security.PathWithinRoot(manager.logsDir, canonical)
	if err != nil || !inside {
		return nil, fmt.Errorf("log recovery directory escaped the log directory")
	}
	entries, err := os.ReadDir(canonical)
	if err != nil {
		return nil, fmt.Errorf("list log recovery directory: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && (strings.HasSuffix(entry.Name(), ".ndjson") || strings.HasSuffix(entry.Name(), ".ndjson.active")) {
			paths = append(paths, filepath.Join(canonical, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (manager *Manager) recoverSegmentFile(scope Scope, path string) (*recoveredSegment, string, error) {
	active := strings.HasSuffix(path, ".active")
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, path, fmt.Errorf("open recoverable log segment: %w", err)
	}
	recovered, validBytes, err := inspectSegment(file, scope, streamFromSegmentName(filepath.Base(path)))
	if err != nil {
		_ = file.Close()
		return nil, path, fmt.Errorf("inspect recoverable log segment %s: %w", filepath.Base(path), err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, path, fmt.Errorf("stat recoverable log segment: %w", err)
	}
	if !active && info.Size() != validBytes {
		_ = file.Close()
		return nil, path, fmt.Errorf("published log segment has an incomplete tail")
	}
	finalPath, err := repairAndPublishSegment(file, path, active, validBytes)
	if err != nil || finalPath == "" {
		return nil, path, err
	}
	return manager.completeRecoveredSegment(recovered, finalPath)
}

func repairAndPublishSegment(file *os.File, path string, active bool, validBytes int64) (string, error) {
	if active && validBytes == 0 {
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("close empty active log segment: %w", err)
		}
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("remove empty active log segment: %w", err)
		}
		return "", nil
	}
	if active {
		if err := file.Truncate(validBytes); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("truncate active log segment: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("flush repaired log segment: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close recovered log segment: %w", err)
	}
	finalPath := strings.TrimSuffix(path, ".active")
	if active {
		if err := os.Rename(path, finalPath); err != nil {
			return "", fmt.Errorf("publish repaired log segment: %w", err)
		}
	}
	return finalPath, nil
}

func (manager *Manager) completeRecoveredSegment(recovered *recoveredSegment, finalPath string) (*recoveredSegment, string, error) {
	info, err := os.Stat(finalPath)
	if err != nil {
		return nil, finalPath, fmt.Errorf("stat recovered log segment: %w", err)
	}
	relative, err := filepath.Rel(manager.dataDir, finalPath)
	if err != nil {
		return nil, finalPath, fmt.Errorf("resolve recovered log segment: %w", err)
	}
	recovered.segment.Path = filepath.ToSlash(relative)
	recovered.segment.SizeBytes = info.Size()
	recovered.segment.ClosedAt = manager.clock().UTC()
	return recovered, finalPath, nil
}

func inspectSegment(file *os.File, scope Scope, expectedStream Stream) (*recoveredSegment, int64, error) {
	if expectedStream != StreamStdout && expectedStream != StreamStderr {
		return nil, 0, fmt.Errorf("unrecognized stream in segment name")
	}
	reader := bufio.NewReaderSize(file, 64*1024)
	result := &recoveredSegment{offsets: map[Stream]int64{}}
	var validBytes int64
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > maximumNDJSONLine {
			return nil, validBytes, fmt.Errorf("log segment line exceeds limit")
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			entry, decodeErr := decodeRecoveredEntry(line[:len(line)-1], scope, expectedStream)
			if decodeErr != nil {
				return nil, validBytes, decodeErr
			}
			if result.segment.FirstSequence == 0 {
				result.segment = Segment{ServiceInstanceID: scope.ServiceInstanceID, Stream: expectedStream, FirstSequence: entry.Sequence, FirstTimestamp: entry.Timestamp.UTC()}
			}
			if entry.Sequence <= result.segment.LastSequence {
				return nil, validBytes, fmt.Errorf("log segment sequence is not increasing")
			}
			result.segment.LastSequence, result.segment.LastTimestamp = entry.Sequence, entry.Timestamp.UTC()
			if entry.SourceEnd == 0 {
				result.legacyOffset = true
			} else if entry.SourceEnd > result.offsets[expectedStream] {
				result.offsets[expectedStream] = entry.SourceEnd
			}
			validBytes += int64(len(line))
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, validBytes, fmt.Errorf("read log segment: %w", err)
		}
	}
	if result.segment.FirstSequence == 0 {
		return nil, validBytes, nil
	}
	return result, validBytes, nil
}

func decodeRecoveredEntry(line []byte, scope Scope, expectedStream Stream) (persistedEntry, error) {
	var entry persistedEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return entry, fmt.Errorf("decode complete log segment line: %w", err)
	}
	if entry.SystemID != scope.SystemID || entry.InstanceID != scope.InstanceID || entry.ServiceID != scope.ServiceID ||
		entry.Stream != expectedStream || entry.Sequence <= 0 || entry.Timestamp.IsZero() || entry.SourceEnd < 0 {
		return entry, fmt.Errorf("log segment entry does not match its recovery scope")
	}
	return entry, nil
}

func streamFromSegmentName(name string) Stream {
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".active"), ".ndjson")
	switch {
	case strings.HasSuffix(name, "-stdout"):
		return StreamStdout
	case strings.HasSuffix(name, "-stderr"):
		return StreamStderr
	default:
		return ""
	}
}

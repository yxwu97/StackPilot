package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"stackpilot/internal/security"
)

type segmentWriter struct {
	manager        *Manager
	scope          Scope
	stream         Stream
	file           *os.File
	activePath     string
	firstSequence  int64
	lastSequence   int64
	firstTimestamp time.Time
	lastTimestamp  time.Time
	size           int64
	day            string
}

func newSegmentWriter(manager *Manager, scope Scope, stream Stream) *segmentWriter {
	return &segmentWriter{manager: manager, scope: scope, stream: stream}
}

func (writer *segmentWriter) write(ctx context.Context, entry Entry, sourceEnd int64) error {
	encoded, err := json.Marshal(persistedEntry{Entry: entry, SourceEnd: sourceEnd})
	if err != nil {
		return fmt.Errorf("encode log entry: %w", err)
	}
	encoded = append(encoded, '\n')
	day := entry.Timestamp.UTC().Format("2006-01-02")
	if writer.file != nil && (writer.size+int64(len(encoded)) > writer.manager.segmentMaxBytes || day != writer.day) {
		if err := writer.close(ctx, entry.Timestamp); err != nil {
			return err
		}
	}
	if writer.file == nil {
		if err := writer.open(entry.Sequence, entry.Timestamp, day); err != nil {
			return err
		}
	}
	if err := writeAll(writer.file, encoded); err != nil {
		return fmt.Errorf("write active log segment: %w", err)
	}
	writer.size += int64(len(encoded))
	writer.lastSequence, writer.lastTimestamp = entry.Sequence, entry.Timestamp.UTC()
	return writer.manager.trackActive(writer)
}

type persistedEntry struct {
	Entry
	SourceEnd int64 `json:"_sourceEnd,omitempty"`
}

func (writer *segmentWriter) open(sequence int64, timestamp time.Time, day string) error {
	directory := filepath.Join(writer.manager.logsDir, writer.scope.InstanceID.String(), writer.scope.ServiceID.String())
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create log segment directory: %w", err)
	}
	canonicalDirectory, err := security.CanonicalExistingPath(directory)
	if err != nil {
		return fmt.Errorf("canonicalize log segment directory: %w", err)
	}
	inside, err := security.PathWithinRoot(writer.manager.logsDir, canonicalDirectory)
	if err != nil || !inside {
		return fmt.Errorf("log segment directory escaped the log directory")
	}
	name := fmt.Sprintf("%020d-%s.ndjson.active", sequence, writer.stream)
	path := filepath.Join(canonicalDirectory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create active log segment: %w", err)
	}
	writer.file, writer.activePath = file, path
	writer.firstSequence, writer.lastSequence = sequence, sequence
	writer.firstTimestamp, writer.lastTimestamp = timestamp.UTC(), timestamp.UTC()
	writer.size, writer.day = 0, day
	return nil
}

func (writer *segmentWriter) close(ctx context.Context, closedAt time.Time) (err error) {
	if writer.file == nil {
		return nil
	}
	writer.manager.activeMutex.Lock()
	defer writer.manager.activeMutex.Unlock()
	file, activePath := writer.file, writer.activePath
	writer.file = nil
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush active log segment: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close active log segment: %w", err)
	}
	finalPath := strings.TrimSuffix(activePath, ".active")
	if err := os.Rename(activePath, finalPath); err != nil {
		return fmt.Errorf("publish closed log segment: %w", err)
	}
	delete(writer.manager.activeSegments, activePath)
	segment, err := writer.metadata(finalPath, closedAt)
	if err != nil {
		return err
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := writer.manager.index.RegisterClosed(finalizeContext, segment); err != nil {
		return fmt.Errorf("index closed log segment: %w", err)
	}
	writer.reset()
	return nil
}

func (manager *Manager) trackActive(writer *segmentWriter) error {
	segment, err := writer.metadata(writer.activePath, writer.lastTimestamp)
	if err != nil {
		return err
	}
	manager.activeMutex.Lock()
	manager.activeSegments[writer.activePath] = segment
	manager.activeMutex.Unlock()
	return nil
}

func (writer *segmentWriter) metadata(finalPath string, closedAt time.Time) (Segment, error) {
	relative, err := filepath.Rel(writer.manager.dataDir, finalPath)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Segment{}, fmt.Errorf("closed log segment escaped the data directory")
	}
	return Segment{
		ServiceInstanceID: writer.scope.ServiceInstanceID, Stream: writer.stream, Path: filepath.ToSlash(relative),
		FirstSequence: writer.firstSequence, LastSequence: writer.lastSequence,
		FirstTimestamp: writer.firstTimestamp, LastTimestamp: writer.lastTimestamp,
		SizeBytes: writer.size, ClosedAt: closedAt.UTC(),
	}, nil
}

func (writer *segmentWriter) reset() {
	writer.activePath = ""
	writer.firstSequence, writer.lastSequence, writer.size = 0, 0, 0
	writer.firstTimestamp, writer.lastTimestamp = time.Time{}, time.Time{}
	writer.day = ""
}

func writeAll(file *os.File, contents []byte) error {
	for len(contents) > 0 {
		count, err := file.Write(contents)
		if err != nil {
			return err
		}
		if count == 0 {
			return errors.New("zero-progress log write")
		}
		contents = contents[count:]
	}
	return nil
}

package logs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"stackpilot/internal/domain"
)

func TestManagerCapturesRedactsChunksRotatesAndIndexes(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	stdout := "INFO first\r\nAuthorization: Bearer top-secret\n" + strings.Repeat("x", 70)
	stderr := `{"level":"error","message":"failed?token=query-secret"}` + "\n"
	writeFixture(t, stdoutPath, stdout)
	writeFixture(t, stderrPath, stderr)
	index := &memoryIndex{}
	manager := newTestManager(t, dataDir, index, Config{SegmentMaxBytes: 300, LineMaxBytes: 64})
	session, err := manager.Start(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: map[Stream]string{StreamStdout: stdoutPath, StreamStderr: stderrPath},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	entries := waitForEvents(t, session.Events(), 4)
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for entry := range session.Events() {
		entries = append(entries, entry)
	}
	assertCapturedEntries(t, entries)
	assertPersistedSegments(t, dataDir, index.snapshot(), entries)
	page, err := manager.Query(context.Background(), Query{Scope: testScope(), Limit: 2})
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Sequence != 1 || page.Entries[1].Sequence != 2 {
		t.Fatalf("Query(first page) = (%#v, %v)", page, err)
	}
	next, err := manager.Query(context.Background(), Query{Scope: testScope(), AfterSequence: page.LastSequence, Limit: 3})
	if err != nil || len(next.Entries) != 3 || next.Entries[0].Sequence != 3 {
		t.Fatalf("Query(next page) = (%#v, %v)", next, err)
	}
	latest, err := manager.QueryWindow(context.Background(), WindowQuery{Scope: testScope(), Limit: 2})
	if err != nil || len(latest.Entries) != 2 || latest.Entries[0].Sequence != 4 || latest.Entries[1].Sequence != 5 || !latest.HasMore || latest.NextCursor != 4 {
		t.Fatalf("QueryWindow(latest) = (%#v, %v)", latest, err)
	}
	older, err := manager.QueryWindow(context.Background(), WindowQuery{Scope: testScope(), Cursor: latest.NextCursor, Limit: 2})
	if err != nil || len(older.Entries) != 2 || older.Entries[0].Sequence != 2 || older.Entries[1].Sequence != 3 || !older.HasMore {
		t.Fatalf("QueryWindow(older) = (%#v, %v)", older, err)
	}
	errorsOnly, err := manager.Query(context.Background(), Query{Scope: testScope(), Level: "error"})
	if err != nil || len(errorsOnly.Entries) != 1 || errorsOnly.Entries[0].Level != "error" {
		t.Fatalf("Query(error level) = (%#v, %v)", errorsOnly, err)
	}
}

func TestQueryWindowIncludesActiveRedactedSegment(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	writeFixture(t, stdoutPath, "token=active-secret\n")
	writeFixture(t, stderrPath, "")
	index := &memoryIndex{}
	manager := newTestManager(t, dataDir, index, Config{})
	session, err := manager.Start(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: testSpools(stdoutPath, stderrPath), SecretValues: [][]byte{[]byte("active-secret")},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := waitForEvents(t, session.Events(), 1)
	window, err := manager.QueryWindow(context.Background(), WindowQuery{Scope: testScope(), Limit: 10})
	if err != nil || len(window.Entries) != 1 || window.Entries[0].Message != "token=[REDACTED:secret]" {
		t.Fatalf("active QueryWindow() = (%#v, %v), live=%#v", window, err, entries)
	}
	if len(index.snapshot()) != 0 {
		t.Fatal("active segment was unexpectedly finalized before Close")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRedactionFailureIsFailClosed(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	writeFixture(t, stdoutPath, "do-not-persist\n")
	writeFixture(t, stderrPath, "")
	failed := make(chan struct{}, 1)
	var safeLog bytes.Buffer
	manager := newTestManager(t, dataDir, &memoryIndex{}, Config{
		Redactor: failingRedactor{called: failed}, Logger: slog.New(slog.NewJSONHandler(&safeLog, nil)),
	})
	session, err := manager.Start(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: map[Stream]string{StreamStdout: stdoutPath, StreamStderr: stderrPath},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("redactor was not called")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if strings.Contains(safeLog.String(), "do-not-persist") {
		t.Fatalf("safe warning leaked message: %s", safeLog.String())
	}
	if entries := drainEvents(session.Events()); len(entries) != 0 {
		t.Fatalf("redaction failure published entries: %#v", entries)
	}
	assertNoFinalLogs(t, dataDir)
}

func TestManagerRedactsExactProcessSecretBeforePersistenceAndPublication(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	secret := []byte("exact-process-secret")
	writeFixture(t, stdoutPath, "child says exact-process-secret\n")
	writeFixture(t, stderrPath, "")
	index := &memoryIndex{}
	manager := newTestManager(t, dataDir, index, Config{})
	session, err := manager.Start(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: testSpools(stdoutPath, stderrPath), SecretValues: [][]byte{secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := waitForEvents(t, session.Events(), 1)
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Message != "child says [REDACTED:secret]" {
		t.Fatalf("redacted entries = %#v", entries)
	}
	segments := index.snapshot()
	if len(segments) != 1 {
		t.Fatalf("closed segments = %d, want 1", len(segments))
	}
	contents, err := os.ReadFile(filepath.Join(dataDir, segments[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(contents, secret) {
		t.Fatal("persisted segment contains plaintext Secret")
	}
}

func TestManagerSlowLiveConsumerDoesNotBlockPersistence(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	var contents strings.Builder
	for index := 0; index < 1000; index++ {
		fmt.Fprintf(&contents, "INFO line-%04d\n", index)
	}
	writeFixture(t, stdoutPath, contents.String())
	writeFixture(t, stderrPath, "")
	index := &memoryIndex{}
	processed := make(chan struct{})
	redactor, _ := NewDefaultRedactor(nil)
	manager := newTestManager(t, dataDir, index, Config{
		SegmentMaxBytes: 4096, Redactor: &notifyingRedactor{delegate: redactor, target: "line-0999", done: processed},
	})
	session, err := manager.Start(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: map[Stream]string{StreamStdout: stdoutPath, StreamStderr: stderrPath},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	select {
	case <-processed:
	case <-time.After(10 * time.Second):
		t.Fatal("log writer did not process all input")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if count := index.countEntries(); count != 1000 {
		t.Fatalf("persisted entry range count = %d, want 1000", count)
	}
	if live := len(drainEvents(session.Events())); live != eventBufferSize {
		t.Fatalf("bounded live events = %d, want %d", live, eventBufferSize)
	}
}

func TestManagerResumeContinuesDurableOffsetsAndSequence(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	writeFixture(t, stdoutPath, "before restart\n")
	writeFixture(t, stderrPath, "")
	index := &memoryIndex{}
	manager := newTestManager(t, dataDir, index, Config{})
	first, err := manager.Start(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: testSpools(stdoutPath, stderrPath),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForEvents(t, first.Events(), 1)
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	appendFixture(t, stdoutPath, "while control plane was down\n")
	resumed, err := manager.Resume(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: testSpools(stdoutPath, stderrPath),
	})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	entries := waitForEvents(t, resumed.Events(), 1)
	if entries[0].Sequence != 2 || entries[0].Message != "while control plane was down" {
		t.Fatalf("resumed entry = %#v", entries[0])
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("resumed Close() error = %v", err)
	}
}

func TestManagerResumeRepairsActiveSegmentTail(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	writeFixture(t, stdoutPath, "persisted\nreplayed after crash\n")
	writeFixture(t, stderrPath, "")
	segmentPath := writeRecoverySegment(t, dataDir, persistedEntry{
		Entry: Entry{
			Timestamp: time.Now().UTC(), SystemID: testScope().SystemID, InstanceID: testScope().InstanceID,
			ServiceID: testScope().ServiceID, Stream: StreamStdout, Message: "persisted", Sequence: 1,
		},
		SourceEnd: int64(len("persisted\n")),
	}, true)
	appendFixture(t, segmentPath, `{"timestamp":`)
	index := &memoryIndex{}
	manager := newTestManager(t, dataDir, index, Config{})
	resumed, err := manager.Resume(context.Background(), CaptureRequest{
		Scope: testScope(), Spools: testSpools(stdoutPath, stderrPath),
	})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	entry := waitForEvents(t, resumed.Events(), 1)[0]
	if entry.Sequence != 2 || entry.Message != "replayed after crash" {
		t.Fatalf("replayed entry = %#v", entry)
	}
	if err := resumed.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := os.Stat(segmentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active segment still exists: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(segmentPath, ".active")); err != nil {
		t.Fatalf("repaired final segment missing: %v", err)
	}
	if len(index.snapshot()) != 2 {
		t.Fatalf("registered segments = %d, want repaired plus resumed", len(index.snapshot()))
	}
}

func TestManagerResumeRegistersPublishedSegmentIdempotently(t *testing.T) {
	dataDir, stdoutPath, stderrPath := spoolFixture(t)
	writeFixture(t, stdoutPath, "already persisted\n")
	writeFixture(t, stderrPath, "")
	writeRecoverySegment(t, dataDir, persistedEntry{
		Entry: Entry{
			Timestamp: time.Now().UTC(), SystemID: testScope().SystemID, InstanceID: testScope().InstanceID,
			ServiceID: testScope().ServiceID, Stream: StreamStdout, Message: "already persisted", Sequence: 1,
		},
		SourceEnd: int64(len("already persisted\n")),
	}, false)
	index := &memoryIndex{}
	manager := newTestManager(t, dataDir, index, Config{})
	for attempt := 0; attempt < 2; attempt++ {
		session, err := manager.Resume(context.Background(), CaptureRequest{
			Scope: testScope(), Spools: testSpools(stdoutPath, stderrPath),
		})
		if err != nil {
			t.Fatalf("Resume(attempt %d) error = %v", attempt, err)
		}
		if err := session.Close(); err != nil {
			t.Fatalf("Close(attempt %d) error = %v", attempt, err)
		}
	}
	if len(index.snapshot()) != 1 {
		t.Fatalf("registered segments after repeated recovery = %d, want 1", len(index.snapshot()))
	}
}

func TestDefaultRedactorAndLevelDetection(t *testing.T) {
	redactor, err := NewDefaultRedactor([]RedactionRule{{Pattern: `(account=)[^ ]+`, Type: "account"}})
	if err != nil {
		t.Fatalf("NewDefaultRedactor() error = %v", err)
	}
	message := "Authorization: Bearer abc Cookie=session=xyz url?api_key=key Password=pwd; account=alice"
	redacted, err := redactor.Redact(message)
	if err != nil {
		t.Fatalf("Redact() error = %v", err)
	}
	for _, secret := range []string{"abc", "xyz", "key", "pwd", "alice"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted message %q contains %q", redacted, secret)
		}
	}
	custom, _ := redactor.Redact("account=alice")
	if strings.Contains(custom, "alice") {
		t.Fatalf("custom redaction rule did not apply: %q", custom)
	}
	if detectLevel(`[WARN] capacity`) != "warn" || detectLevel(`{"level":"ERROR"}`) != "error" || detectLevel("plain") != "unknown" {
		t.Fatal("level detection did not preserve conservative semantics")
	}
	if _, err := NewDefaultRedactor([]RedactionRule{{Pattern: "[", Type: "bad"}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid redaction rule error = %v", err)
	}
}

func TestManagerRejectsExpiredSequenceCursor(t *testing.T) {
	dataDir := t.TempDir()
	index := &memoryIndex{segments: []Segment{{
		ServiceInstanceID: testScope().ServiceInstanceID, Stream: StreamStdout,
		Path: "logs/missing.ndjson", FirstSequence: 10, LastSequence: 20,
	}}}
	manager := newTestManager(t, dataDir, index, Config{})
	_, err := manager.Query(context.Background(), Query{Scope: testScope(), AfterSequence: 1})
	if !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("Query(expired cursor) error = %v", err)
	}
}

func newTestManager(t *testing.T, dataDir string, index SegmentIndex, overrides Config) *Manager {
	t.Helper()
	overrides.DataDir, overrides.Index = dataDir, index
	overrides.PollInterval = 5 * time.Millisecond
	if overrides.Clock == nil {
		var mu sync.Mutex
		current := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
		overrides.Clock = func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			current = current.Add(time.Microsecond)
			return current
		}
	}
	manager, err := NewManager(overrides)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return manager
}

func testScope() Scope {
	return Scope{
		SystemID: "btc", InstanceID: "si_01ARZ3NDEKTSV4RRFFQ69G5FAV", ServiceID: "backend",
		ServiceInstanceID: "svi_01ARZ3NDEKTSV4RRFFQ69G5FAV", OperationID: "op_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}
}

func spoolFixture(t *testing.T) (string, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	directory := filepath.Join(dataDir, "runtime", "services", "backend")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create spool fixture: %v", err)
	}
	return dataDir, filepath.Join(directory, "stdout.spool"), filepath.Join(directory, "stderr.spool")
}

func writeFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write spool fixture: %v", err)
	}
}

func appendFixture(t *testing.T, path, contents string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append fixture: %v", err)
	}
	if _, err := file.WriteString(contents); err != nil {
		_ = file.Close()
		t.Fatalf("append fixture: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close append fixture: %v", err)
	}
}

func testSpools(stdoutPath, stderrPath string) map[Stream]string {
	return map[Stream]string{StreamStdout: stdoutPath, StreamStderr: stderrPath}
}

func writeRecoverySegment(t *testing.T, dataDir string, entry persistedEntry, active bool) string {
	t.Helper()
	directory := filepath.Join(dataDir, "logs", testScope().InstanceID.String(), testScope().ServiceID.String())
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create recovery segment directory: %v", err)
	}
	name := "00000000000000000001-stdout.ndjson"
	if active {
		name += ".active"
	}
	path := filepath.Join(directory, name)
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encode recovery segment: %v", err)
	}
	writeFixture(t, path, string(encoded)+"\n")
	return path
}

func waitForEvents(t *testing.T, events <-chan Entry, count int) []Entry {
	t.Helper()
	result := make([]Entry, 0, count)
	deadline := time.After(5 * time.Second)
	for len(result) < count {
		select {
		case entry := <-events:
			result = append(result, entry)
		case <-deadline:
			t.Fatalf("received %d events, want %d", len(result), count)
		}
	}
	return result
}

func drainEvents(events <-chan Entry) []Entry {
	var result []Entry
	for entry := range events {
		result = append(result, entry)
	}
	return result
}

func assertCapturedEntries(t *testing.T, entries []Entry) {
	t.Helper()
	sort.Slice(entries, func(left, right int) bool { return entries[left].Sequence < entries[right].Sequence })
	if len(entries) != 5 {
		t.Fatalf("captured entries = %d, want 5", len(entries))
	}
	truncated := 0
	for index, entry := range entries {
		if entry.Sequence != int64(index+1) {
			t.Fatalf("entry sequence = %d at index %d", entry.Sequence, index)
		}
		if strings.Contains(entry.Message, "top-secret") || strings.Contains(entry.Message, "query-secret") {
			t.Fatalf("entry leaked secret: %#v", entry)
		}
		if entry.Truncated {
			truncated++
		}
	}
	if truncated != 2 {
		t.Fatalf("truncated entry count = %d, want 2", truncated)
	}
}

func assertPersistedSegments(t *testing.T, dataDir string, segments []Segment, entries []Entry) {
	t.Helper()
	if len(segments) < 2 {
		t.Fatalf("closed segments = %d, want rotation/multiple streams", len(segments))
	}
	var persisted []Entry
	for _, segment := range segments {
		if filepath.Ext(segment.Path) != ".ndjson" || segment.SizeBytes <= 0 {
			t.Fatalf("invalid segment metadata: %#v", segment)
		}
		file, err := os.Open(filepath.Join(dataDir, filepath.FromSlash(segment.Path)))
		if err != nil {
			t.Fatalf("open segment: %v", err)
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			var entry Entry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				t.Fatalf("decode segment entry: %v", err)
			}
			persisted = append(persisted, entry)
		}
		_ = file.Close()
	}
	sort.Slice(persisted, func(left, right int) bool { return persisted[left].Sequence < persisted[right].Sequence })
	if len(persisted) != len(entries) {
		t.Fatalf("persisted entries = %d, live entries = %d", len(persisted), len(entries))
	}
}

func assertNoFinalLogs(t *testing.T, dataDir string) {
	t.Helper()
	count := 0
	_ = filepath.WalkDir(filepath.Join(dataDir, "logs"), func(_ string, entry os.DirEntry, _ error) error {
		if entry != nil && !entry.IsDir() {
			count++
		}
		return nil
	})
	if count != 0 {
		t.Fatalf("redaction failure created %d final log files", count)
	}
}

type memoryIndex struct {
	mu       sync.Mutex
	segments []Segment
}

func (index *memoryIndex) RegisterClosed(_ context.Context, segment Segment) error {
	index.mu.Lock()
	defer index.mu.Unlock()
	index.segments = append(index.segments, segment)
	return nil
}

func (index *memoryIndex) ListAfter(_ context.Context, _ domain.ServiceInstanceID, after int64) ([]Segment, error) {
	index.mu.Lock()
	defer index.mu.Unlock()
	var result []Segment
	for _, segment := range index.segments {
		if segment.LastSequence > after {
			result = append(result, segment)
		}
	}
	return result, nil
}

func (index *memoryIndex) SequenceBounds(_ context.Context, _ domain.ServiceInstanceID) (int64, int64, bool, error) {
	index.mu.Lock()
	defer index.mu.Unlock()
	if len(index.segments) == 0 {
		return 0, 0, false, nil
	}
	first, last := index.segments[0].FirstSequence, index.segments[0].LastSequence
	for _, segment := range index.segments[1:] {
		if segment.FirstSequence < first {
			first = segment.FirstSequence
		}
		if segment.LastSequence > last {
			last = segment.LastSequence
		}
	}
	return first, last, true, nil
}

func (index *memoryIndex) snapshot() []Segment {
	index.mu.Lock()
	defer index.mu.Unlock()
	return append([]Segment(nil), index.segments...)
}

func (index *memoryIndex) countEntries() int {
	index.mu.Lock()
	defer index.mu.Unlock()
	total := int64(0)
	for _, segment := range index.segments {
		total += segment.LastSequence - segment.FirstSequence + 1
	}
	return int(total)
}

type failingRedactor struct{ called chan<- struct{} }

func (redactor failingRedactor) Redact(string) (string, error) {
	select {
	case redactor.called <- struct{}{}:
	default:
	}
	return "", errors.New("redaction unavailable")
}

type notifyingRedactor struct {
	delegate Redactor
	target   string
	done     chan struct{}
	once     sync.Once
}

func (redactor *notifyingRedactor) Redact(message string) (string, error) {
	result, err := redactor.delegate.Redact(message)
	if strings.Contains(message, redactor.target) {
		redactor.once.Do(func() { close(redactor.done) })
	}
	return result, err
}

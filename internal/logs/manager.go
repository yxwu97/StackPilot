package logs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

const (
	defaultSegmentMaxBytes = 20 * 1024 * 1024
	defaultLineMaxBytes    = 256 * 1024
	defaultPollInterval    = 50 * time.Millisecond
	recordBufferSize       = 256
	eventBufferSize        = 256
)

// Config controls bounded capture and segment rotation.
type Config struct {
	DataDir         string
	SegmentMaxBytes int64
	LineMaxBytes    int
	PollInterval    time.Duration
	Index           SegmentIndex
	Redactor        Redactor
	Logger          *slog.Logger
	Clock           func() time.Time
	Publisher       Publisher
}

// CaptureRequest starts one shared sequence over the two process spools.
type CaptureRequest struct {
	Scope           Scope
	Spools          map[Stream]string
	InitialSequence int64
	InitialOffsets  map[Stream]int64
	SecretValues    [][]byte
}

// Manager owns Log Manager configuration and safe data paths.
type Manager struct {
	dataDir         string
	logsDir         string
	segmentMaxBytes int64
	lineMaxBytes    int
	pollInterval    time.Duration
	index           SegmentIndex
	redactor        Redactor
	logger          *slog.Logger
	clock           func() time.Time
	publisher       Publisher
	activeMutex     sync.RWMutex
	activeSegments  map[string]Segment
}

// Redact applies the manager's persistence-boundary redactor to already captured text.
func (manager *Manager) Redact(value string) (string, error) {
	return manager.redactor.Redact(value)
}

// Session is one owned capture lifecycle.
type Session struct {
	cancel context.CancelFunc
	done   chan struct{}
	events chan Entry
	err    error
}

type rawRecord struct {
	stream    Stream
	message   string
	truncated bool
	sourceEnd int64
	err       error
}

// NewManager validates paths and constructs a bounded Log Manager.
func NewManager(config Config) (*Manager, error) {
	if config.Index == nil || config.DataDir == "" || !filepath.IsAbs(config.DataDir) {
		return nil, fmt.Errorf("%w: data directory and segment index are required", ErrInvalidConfig)
	}
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create log data directory: %w", err)
	}
	dataDir, err := security.CanonicalExistingPath(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize log data directory: %w", err)
	}
	logsDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create final log directory: %w", err)
	}
	logsDir, err = security.CanonicalExistingPath(logsDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize final log directory: %w", err)
	}
	inside, err := security.PathWithinRoot(dataDir, logsDir)
	if err != nil || !inside {
		return nil, fmt.Errorf("%w: final log directory escaped data directory", ErrInvalidConfig)
	}
	return configuredManager(config, dataDir, logsDir)
}

func configuredManager(config Config, dataDir, logsDir string) (*Manager, error) {
	segmentMax := config.SegmentMaxBytes
	if segmentMax <= 0 {
		segmentMax = defaultSegmentMaxBytes
	}
	lineMax := config.LineMaxBytes
	if lineMax <= 0 {
		lineMax = defaultLineMaxBytes
	}
	if segmentMax < 128 || lineMax > defaultLineMaxBytes {
		return nil, fmt.Errorf("%w: size bounds", ErrInvalidConfig)
	}
	poll := config.PollInterval
	if poll <= 0 {
		poll = defaultPollInterval
	}
	redactor := config.Redactor
	if redactor == nil {
		redactor, _ = NewDefaultRedactor(nil)
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Manager{
		dataDir: dataDir, logsDir: logsDir, segmentMaxBytes: segmentMax, lineMaxBytes: lineMax,
		pollInterval: poll, index: config.Index, redactor: redactor, logger: logger, clock: clock,
		publisher: config.Publisher, activeSegments: make(map[string]Segment),
	}, nil
}

// Start begins tailing both registered spool files until the context or Session is closed.
func (manager *Manager) Start(ctx context.Context, request CaptureRequest) (*Session, error) {
	if err := validateCaptureRequest(request); err != nil {
		return nil, err
	}
	redactor, err := newCaptureRedactor(manager.redactor, request.SecretValues)
	if err != nil {
		return nil, err
	}
	sources, err := manager.openSpools(request)
	if err != nil {
		redactor.clear()
		return nil, err
	}
	captureContext, cancel := context.WithCancel(ctx)
	session := &Session{cancel: cancel, done: make(chan struct{}), events: make(chan Entry, eventBufferSize)}
	records := make(chan rawRecord, recordBufferSize)
	go manager.readSources(captureContext, sources, records)
	go func() {
		defer redactor.clear()
		session.err = manager.writeRecords(captureContext, request, redactor, records, session.events)
		close(session.events)
		close(session.done)
	}()
	return session, nil
}

// Resume repairs persisted segments and continues capture after the last durable spool offsets.
func (manager *Manager) Resume(ctx context.Context, request CaptureRequest) (*Session, error) {
	if err := validateCaptureRequest(request); err != nil {
		return nil, err
	}
	recovery, err := manager.recoverSegments(ctx, request.Scope)
	if err != nil {
		return nil, err
	}
	for stream, legacy := range recovery.legacy {
		if !legacy {
			continue
		}
		info, statErr := os.Stat(request.Spools[stream])
		if statErr != nil {
			return nil, fmt.Errorf("inspect legacy log spool: %w", statErr)
		}
		recovery.offsets[stream] = info.Size()
	}
	request.InitialSequence = recovery.lastSequence
	request.InitialOffsets = recovery.offsets
	return manager.Start(ctx, request)
}

// Events returns a best-effort bounded live feed; segment persistence never waits for it.
func (session *Session) Events() <-chan Entry { return session.events }

// Close stops capture, flushes partial lines, and closes all active segments.
func (session *Session) Close() error {
	session.cancel()
	return session.Wait()
}

// Wait waits for owned readers and writers to exit.
func (session *Session) Wait() error {
	<-session.done
	return session.err
}

func validateCaptureRequest(request CaptureRequest) error {
	if err := validateScope(request.Scope); err != nil {
		return err
	}
	if request.InitialSequence < 0 || len(request.Spools) != 2 || request.Spools[StreamStdout] == "" || request.Spools[StreamStderr] == "" {
		return fmt.Errorf("%w: spool set or sequence", ErrInvalidScope)
	}
	if len(request.InitialOffsets) != 0 && len(request.InitialOffsets) != 2 {
		return fmt.Errorf("%w: spool offsets", ErrInvalidScope)
	}
	for _, stream := range []Stream{StreamStdout, StreamStderr} {
		if request.InitialOffsets[stream] < 0 {
			return fmt.Errorf("%w: spool offset", ErrInvalidScope)
		}
	}
	return nil
}

func validateScope(scope Scope) error {
	if _, err := domain.ParseSystemID(scope.SystemID.String()); err != nil {
		return fmt.Errorf("%w: system ID", ErrInvalidScope)
	}
	if _, err := domain.ParseSystemInstanceID(scope.InstanceID.String()); err != nil {
		return fmt.Errorf("%w: instance ID", ErrInvalidScope)
	}
	if _, err := domain.ParseServiceID(scope.ServiceID.String()); err != nil {
		return fmt.Errorf("%w: service ID", ErrInvalidScope)
	}
	if _, err := domain.ParseServiceInstanceID(scope.ServiceInstanceID.String()); err != nil {
		return fmt.Errorf("%w: service instance ID", ErrInvalidScope)
	}
	return nil
}

func (manager *Manager) openSpools(request CaptureRequest) (map[Stream]*os.File, error) {
	result := make(map[Stream]*os.File, 2)
	for _, stream := range []Stream{StreamStdout, StreamStderr} {
		file, err := manager.openSpool(request.Spools[stream], request.InitialOffsets[stream])
		if err != nil {
			for _, opened := range result {
				_ = opened.Close()
			}
			return nil, err
		}
		result[stream] = file
	}
	return result, nil
}

func (manager *Manager) openSpool(path string, offset int64) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: spool path must be absolute", ErrInvalidScope)
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return nil, fmt.Errorf("canonicalize log spool: %w", err)
	}
	inside, err := security.PathWithinRoot(manager.dataDir, canonical)
	if err != nil || !inside {
		return nil, fmt.Errorf("%w: spool path is outside data directory", ErrInvalidScope)
	}
	file, err := os.Open(canonical)
	if err != nil {
		return nil, fmt.Errorf("open log spool: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: spool is not a regular file", ErrInvalidScope)
	}
	if offset > info.Size() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: spool offset exceeds file size", ErrInvalidScope)
	}
	if _, err := file.Seek(offset, 0); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("seek log spool: %w", err)
	}
	return file, nil
}

func (manager *Manager) readSources(ctx context.Context, sources map[Stream]*os.File, records chan<- rawRecord) {
	var readers sync.WaitGroup
	for stream, file := range sources {
		readers.Add(1)
		go func(stream Stream, file *os.File) {
			defer readers.Done()
			defer func() {
				if err := file.Close(); err != nil {
					records <- rawRecord{stream: stream, err: fmt.Errorf("close %s spool: %w", stream, err)}
				}
			}()
			offset, err := file.Seek(0, 1)
			if err != nil {
				records <- rawRecord{stream: stream, err: fmt.Errorf("inspect %s spool offset: %w", stream, err)}
				return
			}
			readSpool(ctx, file, stream, offset, manager.lineMaxBytes, manager.pollInterval, records)
		}(stream, file)
	}
	readers.Wait()
	close(records)
}

func (manager *Manager) writeRecords(ctx context.Context, request CaptureRequest, redactor Redactor, records <-chan rawRecord, events chan<- Entry) error {
	writers := make(map[Stream]*segmentWriter, 2)
	nextSequence := request.InitialSequence
	var result error
	for record := range records {
		if record.err != nil {
			result = errors.Join(result, record.err)
			continue
		}
		message, err := redactor.Redact(record.message)
		if err != nil {
			manager.logger.Error("log redaction failed", "service_id", request.Scope.ServiceID.String(), "error_code", "LOG_REDACTION_FAILED")
			continue
		}
		nextSequence++
		entry := newEntry(request.Scope, record, message, nextSequence, manager.clock().UTC())
		writer := writers[record.stream]
		if writer == nil {
			writer = newSegmentWriter(manager, request.Scope, record.stream)
			writers[record.stream] = writer
		}
		if err := writer.write(ctx, entry, record.sourceEnd); err != nil {
			result = errors.Join(result, err)
			continue
		}
		if manager.publisher != nil {
			manager.publisher.Publish(request.Scope.ServiceInstanceID, entry)
		}
		select {
		case events <- entry:
		default:
		}
	}
	for _, writer := range writers {
		result = errors.Join(result, writer.close(ctx, manager.clock().UTC()))
	}
	return result
}

func newEntry(scope Scope, record rawRecord, message string, sequence int64, timestamp time.Time) Entry {
	return Entry{
		Timestamp: timestamp, SystemID: scope.SystemID, InstanceID: scope.InstanceID,
		ServiceID: scope.ServiceID, Stream: record.stream, Level: detectLevel(record.message), Message: message,
		OperationID: scope.OperationID, Sequence: sequence, Truncated: record.truncated,
	}
}

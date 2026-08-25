package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/security"
)

const (
	defaultQueryLimit = 500
	maximumQueryLimit = 5000
	maximumNDJSONLine = defaultLineMaxBytes + 64*1024
)

// Query selects a bounded historical window after a stable sequence cursor.
type Query struct {
	Scope         Scope
	AfterSequence int64
	Limit         int
	Level         string
	From          *time.Time
	To            *time.Time
}

// Page is one ordered historical result and its last sequence cursor.
type Page struct {
	Entries      []Entry
	LastSequence int64
}

// SequenceBounds exposes retained closed-segment bounds without file paths.
func (manager *Manager) SequenceBounds(ctx context.Context, id domain.ServiceInstanceID) (int64, int64, bool, error) {
	return manager.index.SequenceBounds(ctx, id)
}

// Query reads registered closed segments without trusting their stored paths blindly.
func (manager *Manager) Query(ctx context.Context, query Query) (Page, error) {
	if err := validateHistoryQuery(query); err != nil {
		return Page{}, err
	}
	limit := query.Limit
	if limit == 0 {
		limit = defaultQueryLimit
	}
	first, _, found, err := manager.index.SequenceBounds(ctx, query.Scope.ServiceInstanceID)
	if err != nil {
		return Page{}, err
	}
	if found && query.AfterSequence > 0 && query.AfterSequence < first-1 {
		return Page{}, ErrCursorExpired
	}
	segments, err := manager.index.ListAfter(ctx, query.Scope.ServiceInstanceID, query.AfterSequence)
	if err != nil {
		return Page{}, err
	}
	entries := make([]Entry, 0, limit)
	for _, segment := range segments {
		if err := manager.readSegment(ctx, query, segment, limit, &entries); err != nil {
			return Page{}, err
		}
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Sequence < entries[right].Sequence })
	page := Page{Entries: entries, LastSequence: query.AfterSequence}
	if len(entries) > 0 {
		page.LastSequence = entries[len(entries)-1].Sequence
	}
	return page, nil
}

func validateHistoryQuery(query Query) error {
	if err := validateScope(query.Scope); err != nil {
		return err
	}
	if query.AfterSequence < 0 || query.Limit < 0 || query.Limit > maximumQueryLimit {
		return fmt.Errorf("%w: query bounds", ErrInvalidScope)
	}
	if query.Level != "" && normalizeLevel(query.Level) == "unknown" {
		return fmt.Errorf("%w: level", ErrInvalidScope)
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return fmt.Errorf("%w: time range", ErrInvalidScope)
	}
	return nil
}

func (manager *Manager) readSegment(ctx context.Context, query Query, segment Segment, limit int, entries *[]Entry) (err error) {
	file, err := manager.openRegisteredSegment(segment.Path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close registered log segment: %w", closeErr))
		}
	}()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maximumNDJSONLine)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var entry Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return fmt.Errorf("decode registered log segment: %w", err)
		}
		if !matchesQuery(query, entry) {
			continue
		}
		*entries = insertBounded(*entries, entry, limit)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read registered log segment: %w", err)
	}
	return nil
}

func (manager *Manager) openRegisteredSegment(relative string) (*os.File, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("registered log segment path is unsafe")
	}
	candidate := filepath.Join(manager.dataDir, clean)
	canonical, err := security.CanonicalExistingPath(candidate)
	if err != nil {
		return nil, fmt.Errorf("canonicalize registered log segment: %w", err)
	}
	inside, err := security.PathWithinRoot(manager.logsDir, canonical)
	if err != nil || !inside {
		return nil, fmt.Errorf("registered log segment escaped the log directory")
	}
	file, err := os.Open(canonical)
	if err != nil {
		return nil, fmt.Errorf("open registered log segment: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("registered log segment is not a regular file")
	}
	return file, nil
}

func matchesQuery(query Query, entry Entry) bool {
	if entry.SystemID != query.Scope.SystemID || entry.InstanceID != query.Scope.InstanceID || entry.ServiceID != query.Scope.ServiceID {
		return false
	}
	if entry.Sequence <= query.AfterSequence || (query.Level != "" && entry.Level != normalizeLevel(query.Level)) {
		return false
	}
	if query.From != nil && entry.Timestamp.Before(query.From.UTC()) {
		return false
	}
	return query.To == nil || !entry.Timestamp.After(query.To.UTC())
}

func insertBounded(entries []Entry, entry Entry, limit int) []Entry {
	index := sort.Search(len(entries), func(index int) bool { return entries[index].Sequence >= entry.Sequence })
	if index < len(entries) && entries[index].Sequence == entry.Sequence {
		return entries
	}
	if len(entries) == limit && index == len(entries) {
		return entries
	}
	entries = append(entries, Entry{})
	copy(entries[index+1:], entries[index:])
	entries[index] = entry
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

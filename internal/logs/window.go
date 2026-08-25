package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"stackpilot/internal/domain"
)

// WindowQuery selects the newest bounded historical page before a sequence cursor.
type WindowQuery struct {
	Scope  Scope
	Cursor int64
	Limit  int
	Level  string
	From   *time.Time
	To     *time.Time
}

// Window is an ascending display page and the cursor for the next older page.
type Window struct {
	Entries    []Entry
	NextCursor int64
	HasMore    bool
}

// QueryWindow returns the latest matching entries, or the latest entries older than Cursor.
func (manager *Manager) QueryWindow(ctx context.Context, query WindowQuery) (Window, error) {
	if err := validateWindowQuery(query); err != nil {
		return Window{}, err
	}
	limit := query.Limit
	if limit == 0 {
		limit = defaultQueryLimit
	}
	manager.activeMutex.RLock()
	defer manager.activeMutex.RUnlock()
	first, last, found, err := manager.index.SequenceBounds(ctx, query.Scope.ServiceInstanceID)
	if err != nil {
		return Window{}, err
	}
	active := manager.activeWindowSegments(query.Scope.ServiceInstanceID)
	first, last, found = mergeWindowBounds(first, last, found, active)
	if !found {
		return Window{}, nil
	}
	if query.Cursor > last+1 {
		return Window{}, fmt.Errorf("%w: future cursor", ErrInvalidScope)
	}
	if query.Cursor > 0 && query.Cursor < first {
		return Window{}, ErrCursorExpired
	}
	segments, err := manager.index.ListAfter(ctx, query.Scope.ServiceInstanceID, 0)
	if err != nil {
		return Window{}, err
	}
	entries, matches := make([]Entry, 0, limit), 0
	for _, segment := range segments {
		if err := manager.readWindowSegment(ctx, query, segment, limit, &entries, &matches); err != nil {
			return Window{}, err
		}
	}
	for _, segment := range active {
		if err := manager.readWindowSegment(ctx, query, segment, limit, &entries, &matches); err != nil {
			return Window{}, err
		}
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Sequence < entries[right].Sequence })
	window := Window{Entries: entries, HasMore: matches > len(entries)}
	if window.HasMore && len(entries) > 0 {
		window.NextCursor = entries[0].Sequence
	}
	return window, nil
}

func (manager *Manager) activeWindowSegments(id domain.ServiceInstanceID) []Segment {
	result := make([]Segment, 0, 2)
	for _, segment := range manager.activeSegments {
		if segment.ServiceInstanceID == id {
			result = append(result, segment)
		}
	}
	return result
}

func mergeWindowBounds(first, last int64, found bool, segments []Segment) (int64, int64, bool) {
	for _, segment := range segments {
		if !found || segment.FirstSequence < first {
			first = segment.FirstSequence
		}
		if !found || segment.LastSequence > last {
			last = segment.LastSequence
		}
		found = true
	}
	return first, last, found
}

func validateWindowQuery(query WindowQuery) error {
	if query.Cursor < 0 {
		return fmt.Errorf("%w: cursor", ErrInvalidScope)
	}
	return validateHistoryQuery(Query{
		Scope: query.Scope, Limit: query.Limit, Level: query.Level, From: query.From, To: query.To,
	})
}

func (manager *Manager) readWindowSegment(ctx context.Context, query WindowQuery, segment Segment, limit int, entries *[]Entry, matches *int) (err error) {
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
		if !matchesWindow(query, entry) {
			continue
		}
		*matches++
		*entries = insertLatest(*entries, entry, limit)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read registered log segment: %w", err)
	}
	return nil
}

func matchesWindow(query WindowQuery, entry Entry) bool {
	if entry.SystemID != query.Scope.SystemID || entry.InstanceID != query.Scope.InstanceID || entry.ServiceID != query.Scope.ServiceID {
		return false
	}
	if query.Cursor > 0 && entry.Sequence >= query.Cursor {
		return false
	}
	if query.Level != "" && entry.Level != normalizeLevel(query.Level) {
		return false
	}
	if query.From != nil && entry.Timestamp.Before(query.From.UTC()) {
		return false
	}
	return query.To == nil || !entry.Timestamp.After(query.To.UTC())
}

func insertLatest(entries []Entry, entry Entry, limit int) []Entry {
	index := sort.Search(len(entries), func(index int) bool { return entries[index].Sequence >= entry.Sequence })
	if index < len(entries) && entries[index].Sequence == entry.Sequence {
		return entries
	}
	if len(entries) == limit && index == 0 {
		return entries
	}
	entries = append(entries, Entry{})
	copy(entries[index+1:], entries[index:])
	entries[index] = entry
	if len(entries) > limit {
		entries = entries[1:]
	}
	return entries
}

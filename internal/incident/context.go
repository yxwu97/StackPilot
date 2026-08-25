package incident

import (
	"fmt"
	"time"
)

const (
	DefaultBeforeWindow = 2 * time.Minute
	DefaultAfterWindow  = time.Minute
	MaximumLogLines     = 500
	MaximumLogBytes     = 256 * 1024
)

// Redactor removes secrets before incident material can be persisted.
type Redactor interface {
	Redact(string) (string, error)
}

// BuildContext bounds, redacts, and folds a context assembled from durable evidence queries.
func BuildContext(base Context, occurredAt time.Time, lines []LogLine, redactor Redactor) (Context, error) {
	if occurredAt.IsZero() || !occurredAt.Equal(occurredAt.UTC()) || redactor == nil {
		return Context{}, fmt.Errorf("invalid incident context input")
	}
	base.SchemaVersion = "1"
	base.WindowStart = occurredAt.Add(-DefaultBeforeWindow)
	base.WindowEnd = occurredAt.Add(DefaultAfterWindow)
	base.Logs = make([]LogLine, 0, min(len(lines), MaximumLogLines))
	used := 0
	for _, line := range lines {
		if line.Timestamp.Before(base.WindowStart) || line.Timestamp.After(base.WindowEnd) || line.Sequence < 1 {
			continue
		}
		message, err := redactor.Redact(line.Message)
		if err != nil {
			return Context{}, fmt.Errorf("redact incident log: %w", err)
		}
		if len(message) > MaximumLogBytes-used || len(base.Logs) >= MaximumLogLines {
			break
		}
		line.Message = message
		if foldRepeatedLine(&base, line) {
			continue
		}
		base.Logs = append(base.Logs, line)
		used += len(message)
	}
	return base, nil
}

func foldRepeatedLine(context *Context, line LogLine) bool {
	if len(context.Logs) == 0 {
		return false
	}
	previous := &context.Logs[len(context.Logs)-1]
	if previous.ServiceInstanceID != line.ServiceInstanceID || previous.Stream != line.Stream || previous.Message != line.Message {
		return false
	}
	if previous.RepeatCount == 0 {
		previous.RepeatCount = 1
	}
	previous.RepeatCount++
	return true
}

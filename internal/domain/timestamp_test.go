package domain

import (
	"errors"
	"testing"
	"time"
)

func TestNewTimestampNormalizesUTC(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	value := time.Date(2026, time.August, 17, 21, 30, 45, 123456789, location)

	timestamp, err := NewTimestamp(value)
	if err != nil {
		t.Fatalf("NewTimestamp() error = %v", err)
	}
	if !timestamp.Valid() || timestamp.Time().Location() != time.UTC {
		t.Fatalf("timestamp = %v, want valid UTC value", timestamp.Time())
	}
	if got, want := timestamp.String(), "2026-08-17T13:30:45.123456789Z"; got != want {
		t.Fatalf("Timestamp.String() = %q, want %q", got, want)
	}
}

func TestParseTimestampAcceptsOnlyUTC(t *testing.T) {
	for _, value := range []string{"2026-08-17T13:30:45Z", "2026-08-17T13:30:45.123456789+00:00"} {
		if _, err := ParseTimestamp(value); err != nil {
			t.Fatalf("ParseTimestamp(%q) error = %v", value, err)
		}
	}

	for _, value := range []string{"", "2026-08-17", "2026-08-17T21:30:45+08:00"} {
		_, err := ParseTimestamp(value)
		if !errors.Is(err, ErrInvalidTimestamp) {
			t.Fatalf("ParseTimestamp(%q) error = %v, want ErrInvalidTimestamp", value, err)
		}
	}
}

func TestTimestampRejectsZeroValue(t *testing.T) {
	if _, err := NewTimestamp(time.Time{}); !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatalf("NewTimestamp(zero) error = %v, want ErrInvalidTimestamp", err)
	}
	if (Timestamp{}).Valid() || (Timestamp{}).String() != "" {
		t.Fatal("zero Timestamp must be invalid and stringify to empty")
	}
}

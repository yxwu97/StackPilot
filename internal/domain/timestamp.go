package domain

import "time"

// Timestamp is an immutable UTC value intended for persistent domain time.
// Transport and storage layers must map it explicitly at their boundaries.
type Timestamp struct {
	value time.Time
}

// NewTimestamp normalizes a non-zero time to UTC and removes its monotonic component.
func NewTimestamp(value time.Time) (Timestamp, error) {
	if value.IsZero() {
		return Timestamp{}, newInvalidValue("timestamp", value.String(), ErrInvalidTimestamp)
	}
	return Timestamp{value: value.UTC().Round(0)}, nil
}

// ParseTimestamp parses an RFC 3339 timestamp and requires a UTC offset.
func ParseTimestamp(value string) (Timestamp, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return Timestamp{}, newInvalidValue("timestamp", value, ErrInvalidTimestamp)
	}
	_, offset := parsed.Zone()
	if offset != 0 {
		return Timestamp{}, newInvalidValue("timestamp", value, ErrInvalidTimestamp)
	}
	return NewTimestamp(parsed)
}

// Time returns a copy of the normalized UTC time.
func (timestamp Timestamp) Time() time.Time {
	return timestamp.value
}

// String returns the canonical UTC RFC3339Nano representation.
func (timestamp Timestamp) String() string {
	if timestamp.value.IsZero() {
		return ""
	}
	return timestamp.value.Format(time.RFC3339Nano)
}

// Valid reports whether the timestamp contains a non-zero UTC value.
func (timestamp Timestamp) Valid() bool {
	return !timestamp.value.IsZero() && timestamp.value.Location() == time.UTC
}

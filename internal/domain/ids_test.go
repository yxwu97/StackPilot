package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestParseManifestIDs(t *testing.T) {
	validMaximum := "a" + strings.Repeat("0", 62)
	for _, value := range []string{"a", "btc", "backend-api", validMaximum} {
		if id, err := ParseSystemID(value); err != nil || id.String() != value {
			t.Fatalf("ParseSystemID(%q) = (%q, %v), want valid ID", value, id, err)
		}
		if id, err := ParseServiceID(value); err != nil || id.String() != value {
			t.Fatalf("ParseServiceID(%q) = (%q, %v), want valid ID", value, id, err)
		}
	}
}

func TestParseManifestIDsRejectsInvalidValues(t *testing.T) {
	invalid := []string{"", "BTC", "1backend", "backend_api", "a" + strings.Repeat("0", 63)}
	for _, value := range invalid {
		_, err := ParseSystemID(value)
		assertErrorCategory(t, err, ErrInvalidIdentifier)
	}
}

func TestParsePrefixedULIDs(t *testing.T) {
	ulid := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	tests := []struct {
		name  string
		value string
		parse func(string) error
	}{
		{name: "workspace", value: "ws_" + ulid, parse: func(value string) error { _, err := ParseWorkspaceID(value); return err }},
		{name: "instance", value: "si_" + ulid, parse: func(value string) error { _, err := ParseSystemInstanceID(value); return err }},
		{name: "service instance", value: "svi_" + ulid, parse: func(value string) error { _, err := ParseServiceInstanceID(value); return err }},
		{name: "operation", value: "op_" + ulid, parse: func(value string) error { _, err := ParseOperationID(value); return err }},
		{name: "incident", value: "inc_" + ulid, parse: func(value string) error { _, err := ParseIncidentID(value); return err }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.parse(test.value); err != nil {
				t.Fatalf("parse(%q) error = %v", test.value, err)
			}
		})
	}
}

func TestParsePrefixedULIDsRejectsNonCanonicalValues(t *testing.T) {
	invalid := []string{
		"ws_01arz3ndektsv4rrffq69g5fav",
		"ws_81ARZ3NDEKTSV4RRFFQ69G5FAV",
		"ws_01ARZ3NDEKTSV4RRFFQ69G5FAI",
		"op_01ARZ3NDEKTSV4RRFFQ69G5FA",
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}
	for _, value := range invalid {
		var err error
		if strings.HasPrefix(value, "op_") {
			_, err = ParseOperationID(value)
		} else {
			_, err = ParseWorkspaceID(value)
		}
		assertErrorCategory(t, err, ErrInvalidIdentifier)
	}
}

func TestNewEventID(t *testing.T) {
	if id, err := NewEventID(1); err != nil || id != 1 {
		t.Fatalf("NewEventID(1) = (%d, %v), want (1, nil)", id, err)
	}
	for _, value := range []int64{0, -1} {
		_, err := NewEventID(value)
		assertErrorCategory(t, err, ErrInvalidIdentifier)
	}
}

func assertErrorCategory(t *testing.T, err, category error) {
	t.Helper()
	if !errors.Is(err, category) {
		t.Fatalf("error = %v, want errors.Is(_, %v)", err, category)
	}
	var invalid *InvalidValueError
	if !errors.As(err, &invalid) || invalid.Field == "" {
		t.Fatalf("error = %v, want InvalidValueError with field", err)
	}
}

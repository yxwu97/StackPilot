package domain

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewWorkspaceIDGeneratesCanonicalULID(t *testing.T) {
	now := time.Date(2026, time.August, 18, 12, 0, 0, 123000000, time.UTC)
	id, err := NewWorkspaceID(now, bytes.NewReader(bytes.Repeat([]byte{0xab}, 10)))
	if err != nil {
		t.Fatalf("NewWorkspaceID() error = %v", err)
	}
	if _, err := ParseWorkspaceID(id.String()); err != nil {
		t.Fatalf("generated ID %q is not canonical: %v", id, err)
	}
	if !strings.HasPrefix(id.String(), "ws_") || len(id.String()) != 29 {
		t.Fatalf("generated ID = %q", id)
	}
}

func TestNewOperationIDGeneratesCanonicalULID(t *testing.T) {
	now := time.Date(2026, time.August, 18, 12, 0, 0, 123000000, time.UTC)
	id, err := NewOperationID(now, bytes.NewReader(bytes.Repeat([]byte{0xcd}, 10)))
	if err != nil {
		t.Fatalf("NewOperationID() error = %v", err)
	}
	if _, err := ParseOperationID(id.String()); err != nil {
		t.Fatalf("generated ID %q is not canonical: %v", id, err)
	}
}

func TestNewRuntimeIDsGenerateCanonicalULIDs(t *testing.T) {
	now := time.Date(2026, time.August, 18, 12, 0, 0, 123000000, time.UTC)
	system, err := NewSystemInstanceID(now, bytes.NewReader(bytes.Repeat([]byte{0x11}, 10)))
	if err != nil {
		t.Fatalf("NewSystemInstanceID() error = %v", err)
	}
	service, err := NewServiceInstanceID(now, bytes.NewReader(bytes.Repeat([]byte{0x22}, 10)))
	if err != nil {
		t.Fatalf("NewServiceInstanceID() error = %v", err)
	}
	if _, err := ParseSystemInstanceID(system.String()); err != nil {
		t.Fatalf("generated system instance ID = %q: %v", system, err)
	}
	if _, err := ParseServiceInstanceID(service.String()); err != nil {
		t.Fatalf("generated service instance ID = %q: %v", service, err)
	}
}

func TestNewPlanningIDsGenerateCanonicalULIDs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	revision, err := NewRevisionID(now, bytes.NewReader(bytes.Repeat([]byte{0x33}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewChangePlanID(now, bytes.NewReader(bytes.Repeat([]byte{0x44}, 10)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRevisionID(revision.String()); err != nil || len(revision.String()) != 30 {
		t.Fatalf("generated revision ID = %q: %v", revision, err)
	}
	if _, err := ParseChangePlanID(plan.String()); err != nil || len(plan.String()) != 31 {
		t.Fatalf("generated change-plan ID = %q: %v", plan, err)
	}
}

func TestNewWorkspaceIDRejectsInvalidInputs(t *testing.T) {
	if _, err := NewWorkspaceID(time.Now(), nil); err == nil {
		t.Fatal("NewWorkspaceID(nil entropy) unexpectedly succeeded")
	}
	readerError := errors.New("entropy unavailable")
	if _, err := NewWorkspaceID(time.Now(), errorReader{err: readerError}); !errors.Is(err, readerError) {
		t.Fatalf("NewWorkspaceID(error reader) error = %v", err)
	}
	if _, err := NewWorkspaceID(time.UnixMilli(-1), bytes.NewReader(make([]byte, 10))); err == nil {
		t.Fatal("NewWorkspaceID(negative timestamp) unexpectedly succeeded")
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

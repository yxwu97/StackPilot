//go:build windows

package supervisorspike

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestPipeFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	want := pipeRequest{Type: "inspect-service"}
	if err := writeFrame(&buffer, want); err != nil {
		t.Fatalf("writeFrame() error = %v", err)
	}
	var got pipeRequest
	if err := readFrame(&buffer, &got); err != nil {
		t.Fatalf("readFrame() error = %v", err)
	}
	if got != want {
		t.Fatalf("pipe request = %#v, want %#v", got, want)
	}
}

func TestPipeFrameRejectsInvalidLength(t *testing.T) {
	input := bytes.NewReader([]byte{1, 0, 16, 0})
	if err := readFrame(input, &pipeRequest{}); err == nil {
		t.Fatal("oversized frame unexpectedly accepted")
	}
}

func TestWriteAllHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{}
	if err := writeAll(writer, []byte("stackpilot")); err != nil {
		t.Fatalf("writeAll() error = %v", err)
	}
	if writer.buffer.String() != "stackpilot" {
		t.Fatalf("written value = %q", writer.buffer.String())
	}
	if err := writeAll(zeroWriter{}, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress error = %v, want io.ErrShortWrite", err)
	}
}

func TestAtomicIdentityFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "identity.json")
	want := launchRecord{SupervisorPID: 42}
	if err := writeJSONAtomic(path, want); err != nil {
		t.Fatalf("writeJSONAtomic() error = %v", err)
	}
	var got launchRecord
	if err := readJSON(path, &got); err != nil {
		t.Fatalf("readJSON() error = %v", err)
	}
	if got != want {
		t.Fatalf("identity = %#v, want %#v", got, want)
	}
	if fileExists(path + ".tmp") {
		t.Fatal("temporary identity file remains after publish")
	}
}

func TestTreeProfileRequirements(t *testing.T) {
	if !treeMatchesProfile("generic", map[uint32]string{1: "supervisor-spike.exe", 2: "supervisor-spike.exe"}) {
		t.Fatal("generic profile did not require two descendant generations")
	}
	if !treeMatchesProfile("npm", map[uint32]string{1: "node.exe", 2: "NODE.EXE"}) {
		t.Fatal("npm profile did not require two Node descendants")
	}
	if !treeMatchesProfile("maven", map[uint32]string{1: "java.exe"}) {
		t.Fatal("Maven profile did not require a Java descendant")
	}
}

type shortWriter struct {
	buffer bytes.Buffer
}

func (w *shortWriter) Write(value []byte) (int, error) {
	if len(value) > 2 {
		value = value[:2]
	}
	return w.buffer.Write(value)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

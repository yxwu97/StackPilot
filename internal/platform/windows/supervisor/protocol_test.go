package supervisor

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRequestFrameAndPayloadRoundTrip(t *testing.T) {
	payload := StartServiceRequest{
		ServiceID: "backend", Executable: `C:\tools\java.exe`, Arguments: []string{"-jar", "app.jar"},
		WorkingDirectory: `E:\workspace\backend`, Environment: map[string]string{"SERVER_PORT": "8081"},
		CommandDigest: strings.Repeat("a", 64), StdoutPath: `E:\data\stdout.spool`, StderrPath: `E:\data\stderr.spool`,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	request := Request{Version: ProtocolVersion, RequestID: "req_01", Type: MessageStartService, Payload: encoded}
	var framed bytes.Buffer
	if err := WriteRequest(&framed, request); err != nil {
		t.Fatalf("WriteRequest() error = %v", err)
	}
	decoded, err := ReadRequest(&framed)
	if err != nil {
		t.Fatalf("ReadRequest() error = %v", err)
	}
	value, err := decoded.DecodePayload()
	if err != nil {
		t.Fatalf("DecodePayload() error = %v", err)
	}
	start, ok := value.(*StartServiceRequest)
	if !ok || start.ServiceID != payload.ServiceID || start.Environment["SERVER_PORT"] != "8081" {
		t.Fatalf("decoded payload = %#v", value)
	}
}

func TestAllRegisteredMessagePayloads(t *testing.T) {
	tests := []struct {
		message MessageType
		value   any
	}{
		{MessageHello, HelloRequest{ClientPID: 42}},
		{MessageStartService, validStartRequest()},
		{MessageInspectService, ServiceRequest{ServiceID: "backend"}},
		{MessageStopService, StopServiceRequest{ServiceID: "backend", GracefulTimeoutMillis: 15_000}},
		{MessageShutdownIfEmpty, struct{}{}},
	}
	for _, test := range tests {
		t.Run(string(test.message), func(t *testing.T) {
			payload, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			request := Request{Version: ProtocolVersion, RequestID: "request-1", Type: test.message, Payload: payload}
			if _, err := request.DecodePayload(); err != nil {
				t.Fatalf("DecodePayload() error = %v", err)
			}
		})
	}
}

func TestRequestRejectsUnknownFieldsAndMessageTypes(t *testing.T) {
	unknownEnvelope := framedJSON(t, `{"version":1,"requestId":"r1","type":"hello","payload":{"clientPid":1},"extra":true}`)
	if _, err := ReadRequest(unknownEnvelope); err == nil {
		t.Fatal("unknown envelope field unexpectedly accepted")
	}
	request := Request{
		Version: ProtocolVersion, RequestID: "r1", Type: MessageHello,
		Payload: json.RawMessage(`{"clientPid":1,"extra":true}`),
	}
	if _, err := request.DecodePayload(); !errors.Is(err, errInvalidMessage) {
		t.Fatalf("unknown payload field error = %v, want invalid message", err)
	}
	request.Type = MessageType("execute-command")
	request.Payload = json.RawMessage(`{}`)
	if _, err := request.DecodePayload(); !errors.Is(err, errInvalidMessage) {
		t.Fatalf("unknown message error = %v, want invalid message", err)
	}
}

func TestRequestRejectsVersionMismatchAndUnsafeStart(t *testing.T) {
	request := Request{Version: ProtocolVersion + 1, RequestID: "r1", Type: MessageHello, Payload: json.RawMessage(`{"clientPid":1}`)}
	if _, err := request.DecodePayload(); !errors.Is(err, errVersionMismatch) {
		t.Fatalf("version mismatch error = %v", err)
	}
	response := ErrorResponse(request.RequestID, errVersionMismatch)
	if response.Error == nil || response.Error.Code != ErrorVersionMismatch || response.OK {
		t.Fatalf("version error response = %#v", response)
	}

	start := validStartRequest()
	start.Executable = `..\outside.exe`
	payload, _ := json.Marshal(start)
	request = Request{Version: ProtocolVersion, RequestID: "r1", Type: MessageStartService, Payload: payload}
	if _, err := request.DecodePayload(); !errors.Is(err, errInvalidMessage) {
		t.Fatalf("unsafe start error = %v", err)
	}
}

func TestStartRequestRejectsCaseInsensitiveEnvironmentDuplicates(t *testing.T) {
	start := validStartRequest()
	start.Environment = map[string]string{"PATH": `C:\tools`, "Path": `C:\other`}
	payload, err := json.Marshal(start)
	if err != nil {
		t.Fatalf("marshal start request: %v", err)
	}
	request := Request{Version: ProtocolVersion, RequestID: "r1", Type: MessageStartService, Payload: payload}
	if _, err := request.DecodePayload(); !errors.Is(err, errInvalidMessage) {
		t.Fatalf("duplicate environment error = %v, want invalid message", err)
	}
}

func TestStartRequestValidatesSecretEnvironmentNames(t *testing.T) {
	request := validStartRequest()
	request.Environment["TOKEN"] = "sensitive"
	request.SecretEnvironmentNames = []string{"TOKEN"}
	if err := validateStartRequest(request); err != nil {
		t.Fatalf("valid Secret environment error = %v", err)
	}
	request.SecretEnvironmentNames = []string{"MISSING"}
	if err := validateStartRequest(request); err == nil {
		t.Fatal("missing Secret environment unexpectedly accepted")
	}
	request.SecretEnvironmentNames = []string{"TOKEN", "token"}
	if err := validateStartRequest(request); err == nil {
		t.Fatal("duplicate Secret environment unexpectedly accepted")
	}
}

func TestFrameBoundsPartialDataAndShortWrites(t *testing.T) {
	for _, size := range []uint32{0, MaxMessageSize + 1} {
		header := make([]byte, 4)
		binary.LittleEndian.PutUint32(header, size)
		if _, err := ReadRequest(bytes.NewReader(header)); err == nil {
			t.Errorf("frame size %d unexpectedly accepted", size)
		}
	}
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, 10)
	if _, err := ReadRequest(bytes.NewReader(append(header, []byte("short")...))); err == nil {
		t.Fatal("partial frame unexpectedly accepted")
	}
	if err := WriteRequest(zeroWriter{}, Request{Version: 1}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress writer error = %v, want io.ErrShortWrite", err)
	}
}

func TestSuccessResponseRoundTrip(t *testing.T) {
	want, err := SuccessResponse("r1", HelloResponse{SupervisorPID: 42, ServiceCount: 1})
	if err != nil {
		t.Fatalf("SuccessResponse() error = %v", err)
	}
	var buffer bytes.Buffer
	if err := WriteResponse(&buffer, want); err != nil {
		t.Fatalf("WriteResponse() error = %v", err)
	}
	got, err := ReadResponse(&buffer)
	if err != nil || !got.OK || got.RequestID != want.RequestID || got.Error != nil {
		t.Fatalf("ReadResponse() = (%#v, %v)", got, err)
	}
}

func FuzzReadRequestFrameNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{2, 0, 0, 0, '{', '}'})
	f.Add([]byte{255, 255, 255, 255})
	f.Fuzz(func(t *testing.T, value []byte) {
		_, _ = ReadRequest(bytes.NewReader(value))
	})
}

func validStartRequest() StartServiceRequest {
	return StartServiceRequest{
		ServiceID: "backend", Executable: `C:\tools\java.exe`, Arguments: []string{"-version"},
		WorkingDirectory: `E:\workspace`, Environment: map[string]string{}, CommandDigest: strings.Repeat("a", 64),
		StdoutPath: `E:\data\stdout.spool`, StderrPath: `E:\data\stderr.spool`,
	}
}

func framedJSON(t *testing.T, value string) io.Reader {
	t.Helper()
	payload := []byte(value)
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, uint32(len(payload)))
	return bytes.NewReader(append(header, payload...))
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

// Package supervisor implements the private Windows Supervisor protocol.
package supervisor

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"stackpilot/internal/domain"
)

const (
	// ProtocolVersion is the current private Supervisor wire version.
	ProtocolVersion = 2
	// MinimumProtocolVersion is the oldest lifecycle-compatible Supervisor version.
	MinimumProtocolVersion = 1
	// ResourceProtocolVersion introduced Job Object resource observations.
	ResourceProtocolVersion = 2
	// MaxMessageSize bounds every framed request and response.
	MaxMessageSize = 1 << 20
	maxEnvironment = 512
	maxArguments   = 256
)

// MessageType is one closed protocol operation.
type MessageType string

const (
	MessageHello           MessageType = "hello"
	MessageStartService    MessageType = "start-service"
	MessageInspectService  MessageType = "inspect-service"
	MessageObserveService  MessageType = "observe-service"
	MessageStopService     MessageType = "stop-service"
	MessageShutdownIfEmpty MessageType = "shutdown-if-empty"
)

// ErrorCode is a stable private protocol failure category.
type ErrorCode string

const (
	ErrorInvalidMessage     ErrorCode = "invalid-message"
	ErrorVersionMismatch    ErrorCode = "version-mismatch"
	ErrorServiceExists      ErrorCode = "service-exists"
	ErrorServiceNotFound    ErrorCode = "service-not-found"
	ErrorIdentityMismatch   ErrorCode = "identity-mismatch"
	ErrorSupervisorNotEmpty ErrorCode = "supervisor-not-empty"
	ErrorInternal           ErrorCode = "internal"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Request is the strict outer request envelope.
type Request struct {
	Version   int             `json:"version"`
	RequestID string          `json:"requestId"`
	Type      MessageType     `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// Response is the strict outer response envelope.
type Response struct {
	Version   int             `json:"version"`
	RequestID string          `json:"requestId"`
	OK        bool            `json:"ok"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Error     *ProtocolError  `json:"error,omitempty"`
}

// ProtocolError is safe to return across the local private channel.
type ProtocolError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// HelloRequest identifies the connecting control-plane process.
type HelloRequest struct {
	ClientPID uint32 `json:"clientPid"`
}

// HelloResponse reports the Supervisor process and protocol identity.
type HelloResponse struct {
	SupervisorPID uint32 `json:"supervisorPid"`
	ServiceCount  int    `json:"serviceCount"`
}

// StartServiceRequest is the only process creation message shape.
type StartServiceRequest struct {
	ServiceID              string            `json:"serviceId"`
	Executable             string            `json:"executable"`
	Arguments              []string          `json:"arguments"`
	WorkingDirectory       string            `json:"workingDirectory"`
	Environment            map[string]string `json:"environment"`
	SecretEnvironmentNames []string          `json:"secretEnvironmentNames,omitempty"`
	CommandDigest          string            `json:"commandDigest"`
	StdoutPath             string            `json:"stdoutPath"`
	StderrPath             string            `json:"stderrPath"`
}

// ServiceRequest selects one already supervised service.
type ServiceRequest struct {
	ServiceID string `json:"serviceId"`
}

// StopServiceRequest requests bounded graceful then forced termination.
type StopServiceRequest struct {
	ServiceID             string `json:"serviceId"`
	GracefulTimeoutMillis int64  `json:"gracefulTimeoutMillis"`
}

// ProcessIdentity contains all persisted values required before control actions.
type ProcessIdentity struct {
	PID             uint32    `json:"pid"`
	CreatedAt       time.Time `json:"createdAt"`
	ExecutablePath  string    `json:"executablePath"`
	AccountSID      string    `json:"accountSid"`
	CommandDigest   string    `json:"commandDigest"`
	ProtocolVersion int       `json:"protocolVersion"`
}

// ServiceStatus is returned by start, inspect, and stop operations.
type ServiceStatus struct {
	ServiceID string           `json:"serviceId"`
	State     string           `json:"state"`
	Identity  *ProcessIdentity `json:"identity,omitempty"`
	ExitCode  *uint32          `json:"exitCode,omitempty"`
	Forced    bool             `json:"forced,omitempty"`
}

// ResourceObservation is one safe full-Job resource counter snapshot.
type ResourceObservation struct {
	ServiceID       string           `json:"serviceId"`
	ObservedAt      time.Time        `json:"observedAt"`
	CPUTotalMillis  int64            `json:"cpuTotalMillis"`
	MemoryBytes     uint64           `json:"memoryBytes"`
	ActiveProcesses uint32           `json:"activeProcesses"`
	Identity        *ProcessIdentity `json:"identity"`
}

// DecodePayload strictly decodes the request payload for its registered type.
func (request Request) DecodePayload() (any, error) {
	if request.Version < MinimumProtocolVersion || request.Version > ProtocolVersion {
		return nil, fmt.Errorf("%w: got %d", errVersionMismatch, request.Version)
	}
	if !requestIDPattern.MatchString(request.RequestID) {
		return nil, fmt.Errorf("%w: invalid request ID", errInvalidMessage)
	}
	var target any
	switch request.Type {
	case MessageHello:
		target = &HelloRequest{}
	case MessageStartService:
		target = &StartServiceRequest{}
	case MessageInspectService:
		target = &ServiceRequest{}
	case MessageObserveService:
		if request.Version < ResourceProtocolVersion {
			return nil, fmt.Errorf("%w: resource observation requires version %d", errVersionMismatch, ResourceProtocolVersion)
		}
		target = &ServiceRequest{}
	case MessageStopService:
		target = &StopServiceRequest{}
	case MessageShutdownIfEmpty:
		target = &struct{}{}
	default:
		return nil, fmt.Errorf("%w: unsupported message type", errInvalidMessage)
	}
	if err := strictJSON(request.Payload, target); err != nil {
		return nil, fmt.Errorf("%w: payload: %v", errInvalidMessage, err)
	}
	if err := validatePayload(target); err != nil {
		return nil, err
	}
	return target, nil
}

var (
	errInvalidMessage   = errors.New("invalid Supervisor message")
	errVersionMismatch  = errors.New("Supervisor protocol version mismatch")
	errIdentityMismatch = errors.New("Supervisor identity mismatch")
)

// ErrorResponse maps a decoding/validation failure to a safe response.
func ErrorResponse(requestID string, err error) Response {
	return errorResponseForVersion(ProtocolVersion, requestID, err)
}

func errorResponseForVersion(version int, requestID string, err error) Response {
	code, message := ErrorInvalidMessage, "The Supervisor request is invalid."
	if errors.Is(err, errVersionMismatch) {
		code, message = ErrorVersionMismatch, "The Supervisor protocol version is incompatible."
	}
	return Response{Version: version, RequestID: requestID, Error: &ProtocolError{Code: code, Message: message}}
}

// SuccessResponse encodes a typed payload in a successful envelope.
func SuccessResponse(requestID string, value any) (Response, error) {
	return successResponseForVersion(ProtocolVersion, requestID, value)
}

func successResponseForVersion(version int, requestID string, value any) (Response, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return Response{}, fmt.Errorf("encode Supervisor response payload: %w", err)
	}
	return Response{Version: version, RequestID: requestID, OK: true, Payload: payload}, nil
}

// ReadRequest reads one bounded frame and strictly decodes its envelope.
func ReadRequest(reader io.Reader) (Request, error) {
	payload, err := readFrame(reader)
	if err != nil {
		return Request{}, err
	}
	var request Request
	if err := strictJSON(payload, &request); err != nil {
		return Request{}, fmt.Errorf("decode Supervisor request: %w", err)
	}
	return request, nil
}

// WriteResponse writes one bounded response frame.
func WriteResponse(writer io.Writer, response Response) error {
	return writeFrame(writer, response)
}

// WriteRequest writes one bounded request frame for a Supervisor client.
func WriteRequest(writer io.Writer, request Request) error {
	return writeFrame(writer, request)
}

// ReadResponse reads one bounded strict response frame.
func ReadResponse(reader io.Reader) (Response, error) {
	payload, err := readFrame(reader)
	if err != nil {
		return Response{}, err
	}
	var response Response
	if err := strictJSON(payload, &response); err != nil {
		return Response{}, fmt.Errorf("decode Supervisor response: %w", err)
	}
	return response, nil
}

func validatePayload(value any) error {
	switch payload := value.(type) {
	case *HelloRequest:
		if payload.ClientPID == 0 {
			return fmt.Errorf("%w: client PID", errInvalidMessage)
		}
	case *StartServiceRequest:
		return validateStartRequest(*payload)
	case *ServiceRequest:
		return validateServiceID(payload.ServiceID)
	case *StopServiceRequest:
		if err := validateServiceID(payload.ServiceID); err != nil {
			return err
		}
		if payload.GracefulTimeoutMillis < 0 || payload.GracefulTimeoutMillis > int64((10*time.Minute)/time.Millisecond) {
			return fmt.Errorf("%w: graceful timeout", errInvalidMessage)
		}
	}
	return nil
}

func validateStartRequest(request StartServiceRequest) error {
	if err := validateServiceID(request.ServiceID); err != nil {
		return err
	}
	if !filepath.IsAbs(request.Executable) || !filepath.IsAbs(request.WorkingDirectory) ||
		!filepath.IsAbs(request.StdoutPath) || !filepath.IsAbs(request.StderrPath) {
		return fmt.Errorf("%w: process paths must be absolute", errInvalidMessage)
	}
	if len(request.Arguments) > maxArguments || len(request.Environment) > maxEnvironment || !isDigest(request.CommandDigest) {
		return fmt.Errorf("%w: process bounds or digest", errInvalidMessage)
	}
	seenEnvironment := make(map[string]struct{}, len(request.Environment))
	for name, value := range request.Environment {
		if !environmentNamePattern.MatchString(name) || strings.IndexByte(value, 0) >= 0 {
			return fmt.Errorf("%w: environment", errInvalidMessage)
		}
		normalized := strings.ToUpper(name)
		if _, exists := seenEnvironment[normalized]; exists {
			return fmt.Errorf("%w: duplicate environment", errInvalidMessage)
		}
		seenEnvironment[normalized] = struct{}{}
	}
	seenSecrets := make(map[string]struct{}, len(request.SecretEnvironmentNames))
	for _, name := range request.SecretEnvironmentNames {
		normalized := strings.ToUpper(name)
		value, exists := request.Environment[name]
		if !exists || value == "" || !environmentNamePattern.MatchString(name) {
			return fmt.Errorf("%w: Secret environment", errInvalidMessage)
		}
		if _, duplicate := seenSecrets[normalized]; duplicate {
			return fmt.Errorf("%w: duplicate Secret environment", errInvalidMessage)
		}
		seenSecrets[normalized] = struct{}{}
	}
	for _, argument := range request.Arguments {
		if strings.IndexByte(argument, 0) >= 0 {
			return fmt.Errorf("%w: argument", errInvalidMessage)
		}
	}
	return nil
}

func validateServiceID(value string) error {
	if _, err := domain.ParseServiceID(value); err != nil {
		return fmt.Errorf("%w: service ID", errInvalidMessage)
	}
	return nil
}

func isDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func strictJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("multiple JSON values")
	}
	return nil
}

func writeFrame(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode Supervisor frame: %w", err)
	}
	if len(payload) == 0 || len(payload) > MaxMessageSize {
		return fmt.Errorf("Supervisor frame exceeds %d bytes", MaxMessageSize)
	}
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, uint32(len(payload)))
	if err := writeAll(writer, header); err != nil {
		return fmt.Errorf("write Supervisor frame header: %w", err)
	}
	if err := writeAll(writer, payload); err != nil {
		return fmt.Errorf("write Supervisor frame payload: %w", err)
	}
	return nil
}

func readFrame(reader io.Reader) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, fmt.Errorf("read Supervisor frame header: %w", err)
	}
	size := binary.LittleEndian.Uint32(header)
	if size == 0 || size > MaxMessageSize {
		return nil, fmt.Errorf("invalid Supervisor frame size %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("read Supervisor frame payload: %w", err)
	}
	return payload, nil
}

func writeAll(writer io.Writer, contents []byte) error {
	for len(contents) > 0 {
		written, err := writer.Write(contents)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		contents = contents[written:]
	}
	return nil
}

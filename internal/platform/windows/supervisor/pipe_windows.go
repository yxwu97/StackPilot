//go:build windows

package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const pipeIOTimeout = 15 * time.Second

// NewPipeName returns an unguessable local Supervisor pipe name.
func NewPipeName() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate Supervisor pipe name: %w", err)
	}
	return `\\.\pipe\stackpilot-` + hex.EncodeToString(value), nil
}

func validPipeName(value string) bool {
	const prefix = `\\.\pipe\stackpilot-`
	if len(value) != len(prefix)+32 || value[:len(prefix)] != prefix {
		return false
	}
	_, err := hex.DecodeString(value[len(prefix):])
	return err == nil
}

func listenPipe(pipeName string) (net.Listener, string, error) {
	if !validPipeName(pipeName) {
		return nil, "", fmt.Errorf("invalid Supervisor pipe name")
	}
	sddl, accountSID, err := currentAccountPipeSDDL()
	if err != nil {
		return nil, "", err
	}
	listener, err := winio.ListenPipe(pipeName, &winio.PipeConfig{
		SecurityDescriptor: sddl, MessageMode: true,
		InputBufferSize: 64 * 1024, OutputBufferSize: 64 * 1024,
	})
	if err != nil {
		return nil, "", fmt.Errorf("listen on Supervisor pipe: %w", err)
	}
	return listener, accountSID, nil
}

func currentAccountPipeSDDL() (string, string, error) {
	identity, err := processIdentity(windows.CurrentProcess(), uint32(os.Getpid()), "")
	if err != nil {
		return "", "", err
	}
	sid := identity.AccountSID
	return "O:" + sid + "G:" + sid + "D:P(A;;FA;;;SY)(A;;FA;;;" + sid + ")", sid, nil
}

func pipePeerPID(connection net.Conn, client bool) (uint32, error) {
	handleProvider, ok := connection.(interface{ Fd() uintptr })
	if !ok {
		return 0, fmt.Errorf("named pipe connection does not expose a handle")
	}
	var pid uint32
	var err error
	if client {
		err = windows.GetNamedPipeServerProcessId(windows.Handle(handleProvider.Fd()), &pid)
	} else {
		err = windows.GetNamedPipeClientProcessId(windows.Handle(handleProvider.Fd()), &pid)
	}
	if err != nil {
		return 0, fmt.Errorf("read named pipe peer PID: %w", err)
	}
	return pid, nil
}

// PipeAllowedSIDs reads the live allow-only DACL for integration verification.
func PipeAllowedSIDs(pipeName string) ([]string, error) {
	descriptor, err := windows.GetNamedSecurityInfo(pipeName, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("read Supervisor pipe security: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return nil, fmt.Errorf("read Supervisor pipe DACL: %w", err)
	}
	sids := make([]string, 0, dacl.AceCount)
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return nil, fmt.Errorf("read Supervisor pipe ACE: %w", err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return nil, fmt.Errorf("Supervisor pipe DACL is not allow-only")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		sids = append(sids, sid.String())
	}
	sort.Strings(sids)
	return sids, nil
}

// Client is a verified request client for one Supervisor.
type Client struct {
	identity SupervisorIdentity
}

// RemoteError is a stable failure returned by the private Supervisor.
type RemoteError struct {
	Code ErrorCode
}

func (failure *RemoteError) Error() string {
	return "Supervisor rejected request: " + string(failure.Code)
}

// NewClient verifies the persisted process identity before returning a client.
func NewClient(identity SupervisorIdentity) (*Client, error) {
	if err := VerifySupervisorIdentity(identity); err != nil {
		return nil, err
	}
	return &Client{identity: identity}, nil
}

// Connect verifies the Supervisor and performs the required client hello.
func Connect(ctx context.Context, identity SupervisorIdentity) (*Client, error) {
	client, err := NewClient(identity)
	if err != nil {
		return nil, err
	}
	var response HelloResponse
	if err := client.Exchange(ctx, MessageHello, HelloRequest{ClientPID: uint32(os.Getpid())}, &response); err != nil {
		return nil, err
	}
	if response.SupervisorPID != identity.PID {
		return nil, fmt.Errorf("Supervisor hello identity mismatch")
	}
	return client, nil
}

// Exchange sends one typed private request over a fresh verified connection.
func (client *Client) Exchange(ctx context.Context, messageType MessageType, payload, target any) error {
	if err := VerifySupervisorIdentity(client.identity); err != nil {
		return err
	}
	connection, err := winio.DialPipeContext(ctx, client.identity.PipeName)
	if err != nil {
		return fmt.Errorf("connect to Supervisor pipe: %w", err)
	}
	defer connection.Close()
	serverPID, err := pipePeerPID(connection, true)
	if err != nil || serverPID != client.identity.PID {
		return fmt.Errorf("Supervisor pipe server identity mismatch")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else {
		_ = connection.SetDeadline(time.Now().Add(exchangeTimeout(payload)))
	}
	return exchangeMessage(connection, client.identity.ProtocolVersion, messageType, payload, target)
}

func exchangeTimeout(payload any) time.Duration {
	timeout := pipeIOTimeout
	switch request := payload.(type) {
	case StopServiceRequest:
		timeout += time.Duration(request.GracefulTimeoutMillis) * time.Millisecond
	case *StopServiceRequest:
		timeout += time.Duration(request.GracefulTimeoutMillis) * time.Millisecond
	}
	return timeout
}

func exchangeMessage(connection net.Conn, version int, messageType MessageType, payload, target any) error {
	requestID, err := newRequestID()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode Supervisor request payload: %w", err)
	}
	request := Request{Version: version, RequestID: requestID, Type: messageType, Payload: encoded}
	if err := WriteRequest(connection, request); err != nil {
		return err
	}
	response, err := ReadResponse(connection)
	if err != nil {
		return err
	}
	if response.Version != version || response.RequestID != requestID {
		return fmt.Errorf("Supervisor response correlation mismatch")
	}
	if !response.OK {
		if response.Error == nil {
			return fmt.Errorf("Supervisor rejected request without an error")
		}
		return &RemoteError{Code: response.Error.Code}
	}
	if target != nil {
		if err := strictJSON(response.Payload, target); err != nil {
			return fmt.Errorf("decode Supervisor response payload: %w", err)
		}
	}
	return nil
}

func newRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate Supervisor request ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

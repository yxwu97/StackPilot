//go:build windows

package usertask

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const maximumControlMessage = 4096

type controlRequest struct {
	Protocol int    `json:"protocol"`
	Action   string `json:"action"`
}

type controlResponse struct {
	Protocol int    `json:"protocol"`
	OK       bool   `json:"ok"`
	State    string `json:"state,omitempty"`
	PID      uint32 `json:"pid,omitempty"`
	Version  string `json:"version,omitempty"`
	Error    string `json:"error,omitempty"`
}

type controlServer struct {
	listener net.Listener
	record   installRecord
	cancel   context.CancelCauseFunc
	done     chan error
}

func startControlServer(ctx context.Context, record installRecord, cancel context.CancelCauseFunc) (*controlServer, error) {
	securityDescriptor, err := currentUserPipeSDDL()
	if err != nil {
		return nil, err
	}
	listener, err := winio.ListenPipe(controlPipeName(record), &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor, MessageMode: true,
		InputBufferSize: maximumControlMessage, OutputBufferSize: maximumControlMessage,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on user-task control pipe: %w", err)
	}
	server := &controlServer{listener: listener, record: record, cancel: cancel, done: make(chan error, 1)}
	go server.serve(ctx)
	return server, nil
}

func (server *controlServer) serve(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = server.listener.Close()
	}()
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				server.done <- nil
				return
			}
			server.done <- fmt.Errorf("accept user-task control connection: %w", err)
			return
		}
		server.handle(connection)
	}
}

func (server *controlServer) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	var request controlRequest
	if err := decodeControl(connection, &request); err != nil {
		_ = encodeControl(connection, controlResponse{Protocol: controlProtocol, Error: "invalid request"})
		return
	}
	if request.Protocol != controlProtocol || !validControlAction(request.Action) {
		_ = encodeControl(connection, controlResponse{Protocol: controlProtocol, Error: "unsupported request"})
		return
	}
	response := controlResponse{
		Protocol: controlProtocol, OK: true, State: "running", PID: uint32(os.Getpid()), Version: server.record.Version,
	}
	if err := encodeControl(connection, response); err == nil {
		if cause := controlCancellationCause(request.Action); cause != nil {
			server.cancel(cause)
		}
	}
}

func validControlAction(action string) bool {
	return action == "status" || action == "stop" || action == "upgrade"
}

func controlCancellationCause(action string) error {
	switch action {
	case "stop":
		return errControlStop
	case "upgrade":
		return errUpgradeStop
	default:
		return nil
	}
}

func (server *controlServer) closeAndWait() error {
	_ = server.listener.Close()
	return <-server.done
}

func exchangeControl(ctx context.Context, record installRecord, action string) (controlResponse, error) {
	connection, err := winio.DialPipeContext(ctx, controlPipeName(record))
	if err != nil {
		return controlResponse{}, fmt.Errorf("%w: %w", ErrNotRunning, err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	if err := encodeControl(connection, controlRequest{Protocol: controlProtocol, Action: action}); err != nil {
		return controlResponse{}, err
	}
	var response controlResponse
	if err := decodeControl(connection, &response); err != nil {
		return controlResponse{}, err
	}
	if response.Protocol != controlProtocol || !response.OK {
		return controlResponse{}, fmt.Errorf("user-task control request was rejected")
	}
	return response, nil
}

func encodeControl(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode user-task control message: %w", err)
	}
	if len(payload) > maximumControlMessage {
		return fmt.Errorf("user-task control message is too large")
	}
	payload = append(payload, '\n')
	if _, err := writer.Write(payload); err != nil {
		return fmt.Errorf("write user-task control message: %w", err)
	}
	return nil
}

func decodeControl(reader io.Reader, target any) error {
	payload, err := bufio.NewReader(io.LimitReader(reader, maximumControlMessage+1)).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read user-task control message: %w", err)
	}
	if len(payload) == 0 || len(payload) > maximumControlMessage {
		return fmt.Errorf("user-task control message size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode user-task control message: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("user-task control message has extra JSON data")
	}
	return nil
}

func controlPipeName(record installRecord) string {
	return `\\.\pipe\stackpilot-user-task-` + record.InstallationID
}

func currentUserPipeSDDL() (string, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return "", fmt.Errorf("open current token for user-task pipe: %w", err)
	}
	defer token.Close()
	identity, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current account for user-task pipe: %w", err)
	}
	sid := identity.User.Sid.String()
	return "O:" + sid + "G:" + sid + "D:P(A;;FA;;;SY)(A;;FA;;;" + sid + ")", nil
}

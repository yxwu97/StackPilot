//go:build windows

package supervisorspike

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sort"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const maxPipeMessageSize = 1 << 20

func currentUserPipeSDDL() (string, string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return "", "", fmt.Errorf("open current process token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", "", fmt.Errorf("read current process user: %w", err)
	}
	userSID := user.User.Sid.String()
	sddl := "O:" + userSID + "G:" + userSID + "D:P(A;;FA;;;SY)(A;;FA;;;" + userSID + ")"
	return sddl, userSID, nil
}

func listenPipe(pipeName string) (net.Listener, string, error) {
	sddl, userSID, err := currentUserPipeSDDL()
	if err != nil {
		return nil, "", err
	}
	listener, err := winio.ListenPipe(pipeName, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		MessageMode:        true,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	})
	if err != nil {
		return nil, "", fmt.Errorf("listen on named pipe: %w", err)
	}
	return listener, userSID, nil
}

func servePipe(listener net.Listener, supervisorPID, workerPID uint32) error {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept named pipe connection: %w", err)
		}
		request := pipeRequest{}
		if err := readFrame(connection, &request); err != nil {
			connection.Close()
			continue
		}
		response := handlePipeRequest(request, supervisorPID, workerPID)
		writeErr := writeFrame(connection, response)
		closeErr := connection.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return fmt.Errorf("close named pipe connection: %w", closeErr)
		}
	}
}

func handlePipeRequest(request pipeRequest, supervisorPID, workerPID uint32) pipeResponse {
	response := pipeResponse{ProtocolVersion: protocolVersion, SupervisorPID: supervisorPID, WorkerPID: workerPID}
	switch request.Type {
	case "hello", "inspect-service":
		response.OK = true
	default:
		response.Error = "unsupported message type"
	}
	return response
}

func exchange(pipeName, messageType string) (pipeResponse, error) {
	timeout := 3 * time.Second
	connection, err := winio.DialPipe(pipeName, &timeout)
	if err != nil {
		return pipeResponse{}, fmt.Errorf("connect to named pipe: %w", err)
	}
	defer connection.Close()
	if err := writeFrame(connection, pipeRequest{Type: messageType}); err != nil {
		return pipeResponse{}, err
	}
	var response pipeResponse
	if err := readFrame(connection, &response); err != nil {
		return pipeResponse{}, err
	}
	if !response.OK {
		return response, fmt.Errorf("supervisor rejected %s: %s", messageType, response.Error)
	}
	return response, nil
}

func writeFrame(writer io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode pipe message: %w", err)
	}
	if len(payload) > maxPipeMessageSize {
		return fmt.Errorf("pipe message exceeds %d bytes", maxPipeMessageSize)
	}
	header := make([]byte, 4)
	binary.LittleEndian.PutUint32(header, uint32(len(payload)))
	if err := writeAll(writer, append(header, payload...)); err != nil {
		return fmt.Errorf("write pipe message: %w", err)
	}
	return nil
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

func readFrame(reader io.Reader, target any) error {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil {
		return fmt.Errorf("read pipe message length: %w", err)
	}
	size := binary.LittleEndian.Uint32(header)
	if size == 0 || size > maxPipeMessageSize {
		return fmt.Errorf("invalid pipe message size %d", size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return fmt.Errorf("read pipe message: %w", err)
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode pipe message: %w", err)
	}
	return nil
}

func namedPipeAllowedSIDs(pipeName string) ([]string, error) {
	descriptor, err := windows.GetNamedSecurityInfo(pipeName, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, fmt.Errorf("read named pipe security: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return nil, fmt.Errorf("read named pipe DACL: %w", err)
	}
	if dacl == nil {
		return nil, fmt.Errorf("named pipe has no DACL")
	}
	sids := make([]string, 0, dacl.AceCount)
	for index := uint16(0); index < dacl.AceCount; index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(index), &ace); err != nil {
			return nil, fmt.Errorf("read named pipe ACE %d: %w", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return nil, fmt.Errorf("named pipe ACE %d is not allow-only", index)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		sids = append(sids, sid.String())
	}
	sort.Strings(sids)
	return sids, nil
}

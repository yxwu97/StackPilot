//go:build windows

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"stackpilot/internal/security"
)

const maxConcurrentConnections = 16

// Config defines one private per-instance Supervisor runtime.
type Config struct {
	InstanceDir string
	PipeName    string
	Logger      *slog.Logger
}

type serviceEntry struct {
	mu      sync.Mutex
	service *managedService
}

type server struct {
	instanceDir string
	identity    SupervisorIdentity
	lock        windows.Handle
	lockPath    string
	logger      *slog.Logger
	mu          sync.Mutex
	consoleMu   sync.Mutex
	services    map[string]*serviceEntry
	clients     map[uint32]ProcessIdentity
	shutdown    chan struct{}
	shutdownOne sync.Once
}

// Serve runs the private Supervisor until cancellation or shutdown-if-empty.
func Serve(ctx context.Context, config Config) error {
	runtime, listener, identityPath, err := prepareServer(config)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer runtime.releaseInstanceLock()
	stopped := make(chan struct{})
	go closeListenerOnStop(ctx, runtime.shutdown, listener, stopped)
	err = runtime.acceptConnections(listener)
	close(stopped)
	cleanupErr := runtime.closeServices()
	if shutdownRequested(runtime.shutdown) {
		cleanupErr = errors.Join(cleanupErr, removeIdentityFile(identityPath), runtime.removeInstanceLock())
	}
	if ctx.Err() != nil || shutdownRequested(runtime.shutdown) {
		return cleanupErr
	}
	return errors.Join(err, cleanupErr)
}

func prepareServer(config Config) (*server, net.Listener, string, error) {
	instanceDir, err := canonicalInstanceDirectory(config.InstanceDir)
	if err != nil {
		return nil, nil, "", err
	}
	lock, lockPath, err := acquireInstanceLock(instanceDir)
	if err != nil {
		return nil, nil, "", err
	}
	pipeName := config.PipeName
	if pipeName == "" {
		pipeName, err = NewPipeName()
		if err != nil {
			_ = windows.CloseHandle(lock)
			return nil, nil, "", err
		}
	}
	listener, _, err := listenPipe(pipeName)
	if err != nil {
		_ = windows.CloseHandle(lock)
		return nil, nil, "", err
	}
	identity, err := currentSupervisorIdentity(pipeName)
	if err != nil {
		_ = listener.Close()
		_ = windows.CloseHandle(lock)
		return nil, nil, "", err
	}
	identityPath := filepath.Join(instanceDir, "supervisor.json")
	if err := writeIdentityAtomic(identityPath, identity); err != nil {
		_ = listener.Close()
		_ = windows.CloseHandle(lock)
		return nil, nil, "", err
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &server{
		instanceDir: instanceDir, identity: identity, lock: lock, lockPath: lockPath, logger: logger,
		services: make(map[string]*serviceEntry), clients: make(map[uint32]ProcessIdentity), shutdown: make(chan struct{}),
	}, listener, identityPath, nil
}

func acquireInstanceLock(instanceDir string) (windows.Handle, string, error) {
	path := filepath.Join(instanceDir, ".supervisor.lock")
	encoded, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, "", fmt.Errorf("encode Supervisor lock path: %w", err)
	}
	handle, err := windows.CreateFile(encoded, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_HIDDEN|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, "", fmt.Errorf("acquire Supervisor instance lock: %w", err)
	}
	return handle, path, nil
}

func canonicalInstanceDirectory(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("Supervisor instance directory must be absolute")
	}
	canonical, err := security.CanonicalExistingPath(path)
	if err != nil {
		return "", fmt.Errorf("canonicalize Supervisor instance directory: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("Supervisor instance directory is not a directory")
	}
	return canonical, nil
}

func closeListenerOnStop(ctx context.Context, shutdown <-chan struct{}, listener net.Listener, stopped <-chan struct{}) {
	select {
	case <-ctx.Done():
		_ = listener.Close()
	case <-shutdown:
		_ = listener.Close()
	case <-stopped:
	}
}

func (runtime *server) acceptConnections(listener net.Listener) error {
	limit := make(chan struct{}, maxConcurrentConnections)
	var handlers sync.WaitGroup
	defer handlers.Wait()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if shutdownRequested(runtime.shutdown) {
				return nil
			}
			return fmt.Errorf("accept Supervisor connection: %w", err)
		}
		limit <- struct{}{}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-limit }()
			runtime.handleConnection(connection)
		}()
	}
}

func (runtime *server) handleConnection(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetReadDeadline(time.Now().Add(pipeIOTimeout))
	peerPID, err := pipePeerPID(connection, false)
	if err != nil {
		runtime.logConnectionError(err)
		return
	}
	request, err := ReadRequest(connection)
	if err != nil {
		runtime.logConnectionError(err)
		return
	}
	payload, err := request.DecodePayload()
	runtime.setResponseDeadline(connection, payload)
	if err == nil {
		err = runtime.authorize(peerPID, request.Type, payload)
	}
	response, shutdown := runtime.responseFor(request, payload, err)
	if err := WriteResponse(connection, response); err != nil {
		runtime.logConnectionError(err)
		return
	}
	if shutdown {
		runtime.shutdownOne.Do(func() { close(runtime.shutdown) })
	}
}

func (runtime *server) setResponseDeadline(connection net.Conn, payload any) {
	timeout := pipeIOTimeout
	if request, ok := payload.(*StopServiceRequest); ok {
		timeout += time.Duration(request.GracefulTimeoutMillis) * time.Millisecond
	}
	_ = connection.SetWriteDeadline(time.Now().Add(timeout))
}

func (runtime *server) authorize(peerPID uint32, messageType MessageType, payload any) error {
	if messageType == MessageHello {
		hello := payload.(*HelloRequest)
		if hello.ClientPID != peerPID {
			return errIdentityMismatch
		}
		identity, err := inspectPeerProcess(peerPID)
		if err != nil || !runtime.trustedControlIdentity(identity) {
			return errIdentityMismatch
		}
		runtime.mu.Lock()
		runtime.clients[peerPID] = identity
		runtime.mu.Unlock()
		return nil
	}
	runtime.mu.Lock()
	expected, exists := runtime.clients[peerPID]
	runtime.mu.Unlock()
	if !exists {
		return errIdentityMismatch
	}
	actual, err := inspectPeerProcess(peerPID)
	if err != nil || !sameProcessIdentity(actual, expected) {
		return errIdentityMismatch
	}
	return nil
}

func inspectPeerProcess(pid uint32) (ProcessIdentity, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	defer windows.CloseHandle(handle)
	return processIdentity(handle, pid, "")
}

func (runtime *server) trustedControlIdentity(identity ProcessIdentity) bool {
	if identity.AccountSID != runtime.identity.AccountSID {
		return false
	}
	return strings.EqualFold(identity.ExecutablePath, runtime.identity.ExecutablePath) ||
		trustedInstalledControl(runtime.identity.ExecutablePath, identity.ExecutablePath)
}

func sameProcessIdentity(actual, expected ProcessIdentity) bool {
	return actual.PID == expected.PID && actual.CreatedAt.Equal(expected.CreatedAt) &&
		strings.EqualFold(actual.ExecutablePath, expected.ExecutablePath) && actual.AccountSID == expected.AccountSID
}

func (runtime *server) responseFor(request Request, payload any, requestErr error) (Response, bool) {
	if requestErr != nil {
		if errors.Is(requestErr, errInvalidMessage) || errors.Is(requestErr, errVersionMismatch) {
			return errorResponseForVersion(request.Version, request.RequestID, requestErr), false
		}
		return failureResponse(request.Version, request.RequestID, ErrorIdentityMismatch), false
	}
	value, code, shutdown := runtime.dispatch(request.Type, payload)
	if code != "" {
		return failureResponse(request.Version, request.RequestID, code), false
	}
	response, err := successResponseForVersion(request.Version, request.RequestID, value)
	if err != nil {
		return failureResponse(request.Version, request.RequestID, ErrorInternal), false
	}
	return response, shutdown
}

func (runtime *server) dispatch(messageType MessageType, payload any) (any, ErrorCode, bool) {
	switch messageType {
	case MessageHello:
		return HelloResponse{SupervisorPID: runtime.identity.PID, ServiceCount: runtime.serviceCount()}, "", false
	case MessageStartService:
		status, code := runtime.startService(*payload.(*StartServiceRequest))
		return status, code, false
	case MessageInspectService:
		status, code := runtime.inspectService(payload.(*ServiceRequest).ServiceID)
		return status, code, false
	case MessageObserveService:
		observation, code := runtime.observeService(payload.(*ServiceRequest).ServiceID)
		return observation, code, false
	case MessageStopService:
		request := payload.(*StopServiceRequest)
		status, code := runtime.stopService(request.ServiceID, time.Duration(request.GracefulTimeoutMillis)*time.Millisecond)
		return status, code, false
	case MessageShutdownIfEmpty:
		if runtime.serviceCount() != 0 {
			return nil, ErrorSupervisorNotEmpty, false
		}
		return struct{}{}, "", true
	default:
		return nil, ErrorInvalidMessage, false
	}
}

func (runtime *server) observeService(serviceID string) (ResourceObservation, ErrorCode) {
	entry := runtime.service(serviceID)
	if entry == nil {
		return ResourceObservation{}, ErrorServiceNotFound
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	observation, err := entry.service.resources()
	if err != nil {
		runtime.logServiceError(serviceID, err)
		return ResourceObservation{}, ErrorInternal
	}
	return observation, ""
}

func (runtime *server) startService(request StartServiceRequest) (ServiceStatus, ErrorCode) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if _, exists := runtime.services[request.ServiceID]; exists {
		return ServiceStatus{}, ErrorServiceExists
	}
	service, err := startManagedService(runtime.instanceDir, request)
	if err != nil {
		runtime.logServiceError(request.ServiceID, err)
		return ServiceStatus{}, ErrorInternal
	}
	entry := &serviceEntry{service: service}
	runtime.services[request.ServiceID] = entry
	status, err := service.status()
	if err != nil {
		return ServiceStatus{}, ErrorInternal
	}
	return status, ""
}

func (runtime *server) inspectService(serviceID string) (ServiceStatus, ErrorCode) {
	entry := runtime.service(serviceID)
	if entry == nil {
		return ServiceStatus{}, ErrorServiceNotFound
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	status, err := entry.service.status()
	if err != nil {
		runtime.logServiceError(serviceID, err)
		return ServiceStatus{}, ErrorInternal
	}
	return status, ""
}

func (runtime *server) stopService(serviceID string, timeout time.Duration) (ServiceStatus, ErrorCode) {
	entry := runtime.service(serviceID)
	if entry == nil {
		return ServiceStatus{}, ErrorServiceNotFound
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	status, err := entry.service.stop(timeout, runtime.sendGracefulBreak)
	if err != nil {
		if errors.Is(err, errIdentityMismatch) {
			return ServiceStatus{}, ErrorIdentityMismatch
		}
		runtime.logServiceError(serviceID, err)
		return ServiceStatus{}, ErrorInternal
	}
	runtime.removeService(serviceID, entry)
	if err := entry.service.close(); err != nil {
		runtime.logServiceError(serviceID, err)
		return ServiceStatus{}, ErrorInternal
	}
	return status, ""
}

func (runtime *server) sendGracefulBreak(pid uint32) error {
	runtime.consoleMu.Lock()
	defer runtime.consoleMu.Unlock()
	return sendConsoleBreak(pid)
}

func (runtime *server) service(serviceID string) *serviceEntry {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.services[serviceID]
}

func (runtime *server) removeService(serviceID string, expected *serviceEntry) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.services[serviceID] == expected {
		delete(runtime.services, serviceID)
	}
}

func (runtime *server) serviceCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return len(runtime.services)
}

func (runtime *server) closeServices() error {
	runtime.mu.Lock()
	entries := make([]*serviceEntry, 0, len(runtime.services))
	for _, entry := range runtime.services {
		entries = append(entries, entry)
	}
	runtime.services = make(map[string]*serviceEntry)
	runtime.mu.Unlock()
	var err error
	for _, entry := range entries {
		entry.mu.Lock()
		err = errors.Join(err, entry.service.close())
		entry.mu.Unlock()
	}
	return err
}

func (runtime *server) releaseInstanceLock() {
	if runtime.lock != 0 {
		_ = windows.CloseHandle(runtime.lock)
		runtime.lock = 0
	}
}

func (runtime *server) removeInstanceLock() error {
	runtime.releaseInstanceLock()
	return removeIdentityFile(runtime.lockPath)
}

func failureResponse(version int, requestID string, code ErrorCode) Response {
	messages := map[ErrorCode]string{
		ErrorInvalidMessage: "The Supervisor request is invalid.", ErrorVersionMismatch: "The Supervisor protocol version is incompatible.",
		ErrorServiceExists: "The service is already supervised.", ErrorServiceNotFound: "The service is not supervised.",
		ErrorIdentityMismatch: "The process identity could not be verified.", ErrorSupervisorNotEmpty: "The Supervisor still owns services.",
		ErrorInternal: "The Supervisor could not complete the request.",
	}
	return Response{Version: version, RequestID: requestID, Error: &ProtocolError{Code: code, Message: messages[code]}}
}

func (runtime *server) logConnectionError(err error) {
	runtime.logger.Error("Supervisor connection failed", "error", err)
}

func (runtime *server) logServiceError(serviceID string, err error) {
	runtime.logger.Error("Supervisor service action failed", "service_id", serviceID, "error", err)
}

func shutdownRequested(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func removeIdentityFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove Supervisor identity: %w", err)
	}
	return nil
}

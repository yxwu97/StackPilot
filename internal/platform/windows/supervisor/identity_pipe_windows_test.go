//go:build windows

package supervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestSupervisorIdentityAtomicRoundTripAndLiveVerification(t *testing.T) {
	pipeName, err := NewPipeName()
	if err != nil {
		t.Fatalf("NewPipeName() error = %v", err)
	}
	want, err := currentSupervisorIdentity(pipeName)
	if err != nil {
		t.Fatalf("currentSupervisorIdentity() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "runtime", "supervisor.json")
	if err := writeIdentityAtomic(path, want); err != nil {
		t.Fatalf("writeIdentityAtomic() error = %v", err)
	}
	got, err := ReadSupervisorIdentity(path)
	if err != nil {
		t.Fatalf("ReadSupervisorIdentity() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identity = %#v, want %#v", got, want)
	}
	if err := VerifySupervisorIdentity(got); err != nil {
		t.Fatalf("VerifySupervisorIdentity() error = %v", err)
	}
	got.CreatedAt = got.CreatedAt.Add(time.Nanosecond)
	if err := VerifySupervisorIdentity(got); err == nil {
		t.Fatal("mismatched creation time unexpectedly verified")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read identity directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "supervisor.json" {
		t.Fatalf("identity directory entries = %#v", entries)
	}
}

func TestVerifySupervisorIdentityClassifiesMissingProcess(t *testing.T) {
	pipeName, err := NewPipeName()
	if err != nil {
		t.Fatal(err)
	}
	identity := SupervisorIdentity{
		PID: ^uint32(0), CreatedAt: time.Now().UTC(), ExecutablePath: `C:\fixture\stackpilot.exe`,
		AccountSID: "S-1-5-21-fixture", PipeName: pipeName, ProtocolVersion: ProtocolVersion,
	}
	if err := VerifySupervisorIdentity(identity); !errors.Is(err, ErrSupervisorProcessNotFound) {
		t.Fatalf("VerifySupervisorIdentity(missing) error = %v, want process not found", err)
	}
}

func TestNamedPipePeerIdentityAndRestrictedDACL(t *testing.T) {
	pipeName, err := NewPipeName()
	if err != nil {
		t.Fatalf("NewPipeName() error = %v", err)
	}
	listener, accountSID, err := listenPipe(pipeName)
	if err != nil {
		t.Fatalf("listenPipe() error = %v", err)
	}
	defer listener.Close()
	serverResult := make(chan struct {
		pid uint32
		err error
	}, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- struct {
				pid uint32
				err error
			}{err: err}
			return
		}
		pid, err := pipePeerPID(connection, false)
		_ = connection.Close()
		serverResult <- struct {
			pid uint32
			err error
		}{pid: pid, err: err}
		securityConnection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = securityConnection.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, err := dialPipeForTest(ctx, pipeName)
	if err != nil {
		t.Fatalf("dial Supervisor pipe: %v", err)
	}
	serverPID, err := pipePeerPID(connection, true)
	_ = connection.Close()
	if err != nil || serverPID != uint32(os.Getpid()) {
		t.Fatalf("server peer identity = (%d, %v), want %d", serverPID, err, os.Getpid())
	}
	client := <-serverResult
	if client.err != nil || client.pid != uint32(os.Getpid()) {
		t.Fatalf("client peer identity = (%d, %v), want %d", client.pid, client.err, os.Getpid())
	}
	sids, err := PipeAllowedSIDs(pipeName)
	if err != nil {
		t.Fatalf("PipeAllowedSIDs() error = %v", err)
	}
	wantSIDs := []string{accountSID, "S-1-5-18"}
	sort.Strings(wantSIDs)
	if !reflect.DeepEqual(sids, wantSIDs) {
		t.Fatalf("pipe allowed SIDs = %v, want %v", sids, wantSIDs)
	}
}

func TestExchangeTimeoutIncludesStopGracePeriod(t *testing.T) {
	if got := exchangeTimeout(ServiceRequest{ServiceID: "backend"}); got != pipeIOTimeout {
		t.Fatalf("regular exchange timeout = %v, want %v", got, pipeIOTimeout)
	}
	want := pipeIOTimeout + 15*time.Second
	if got := exchangeTimeout(StopServiceRequest{ServiceID: "backend", GracefulTimeoutMillis: 15_000}); got != want {
		t.Fatalf("stop exchange timeout = %v, want %v", got, want)
	}
}

func dialPipeForTest(ctx context.Context, pipeName string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, pipeName)
}

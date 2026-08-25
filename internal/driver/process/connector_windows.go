//go:build windows

package process

import (
	"context"

	"stackpilot/internal/platform/windows/supervisor"
)

type windowsConnector struct{}

func platformConnector() connector { return windowsConnector{} }

func platformCheck() error { return nil }

func (windowsConnector) connect(ctx context.Context, instanceDir string) (supervisorClient, supervisor.SupervisorIdentity, error) {
	return supervisor.Launch(ctx, instanceDir)
}

func connectPersistedSupervisor(ctx context.Context, identity supervisor.SupervisorIdentity) (supervisorClient, error) {
	return supervisor.Connect(ctx, identity)
}

package ports

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

func listenTCPProbe(ctx context.Context, host string, port int) (io.Closer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	return listener, nil
}

// ReleaseProbe closes the probe immediately before its service process is created.
func (plan *Plan) ReleaseProbe(logicalName string) error {
	plan.mutex.Lock()
	defer plan.mutex.Unlock()
	probe, exists := plan.probes[logicalName]
	if !exists {
		return fmt.Errorf("%w: no owned probe for %s", ErrInvalidInput, logicalName)
	}
	delete(plan.probes, logicalName)
	if err := probe.Close(); err != nil {
		return fmt.Errorf("close port probe %s: %w", logicalName, err)
	}
	return nil
}

// Close releases every probe still owned by the plan.
func (plan *Plan) Close() error {
	plan.mutex.Lock()
	defer plan.mutex.Unlock()
	var result error
	for logicalName, probe := range plan.probes {
		if err := probe.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("close port probe %s: %w", logicalName, err))
		}
		delete(plan.probes, logicalName)
	}
	return result
}

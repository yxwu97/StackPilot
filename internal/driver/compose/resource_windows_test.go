//go:build windows

package compose

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDecodeResourceStatsAggregatesExactContainers(t *testing.T) {
	idA := strings.Repeat("a", 64)
	idB := strings.Repeat("b", 64)
	value := []byte(fmt.Sprintf("{\"ID\":%q,\"CPUPerc\":\"%d%%\",\"MemUsage\":\"1MiB / 2GiB\"}\n{\"ID\":%q,\"CPUPerc\":\"%d%%\",\"MemUsage\":\"2MiB / 2GiB\"}\n", idA, runtime.NumCPU(), idB, runtime.NumCPU()*2))
	observed, err := decodeResourceStats(value, map[string]string{idA: "database", idB: "cache"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("decodeResourceStats() error = %v", err)
	}
	if observed.CPUPercent != 3 || observed.MemoryBytes != 3*(1<<20) || len(observed.Containers) != 2 {
		t.Fatalf("resource observation = %#v", observed)
	}
}

func TestDecodeResourceStatsRejectsUnexpectedOrMissingContainer(t *testing.T) {
	id := strings.Repeat("a", 64)
	unexpected := []byte(`{"ID":"unexpected-id","CPUPerc":"1%","MemUsage":"1MiB / 2GiB"}`)
	if _, err := decodeResourceStats(unexpected, map[string]string{id: "api"}, time.Now().UTC()); err != ErrProjectIdentityMismatch {
		t.Fatalf("unexpected container error = %v", err)
	}
	if _, err := decodeResourceStats(nil, map[string]string{id: "api"}, time.Now().UTC()); err != ErrResourceStatsUnavailable {
		t.Fatalf("missing container error = %v", err)
	}
}

func TestParseDockerBytesUsesReportedUnits(t *testing.T) {
	for value, want := range map[string]int64{"0B": 0, "1.5kB": 1500, "2MiB": 2 << 20, "1GiB": 1 << 30} {
		got, err := parseDockerBytes(value)
		if err != nil || got != want {
			t.Errorf("parseDockerBytes(%q) = %d, %v; want %d", value, got, err, want)
		}
	}
}

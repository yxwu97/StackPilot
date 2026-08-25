package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func (run runner) write(output string, value any, table func()) int {
	if output == "json" {
		if err := json.NewEncoder(run.stdout).Encode(value); err != nil {
			return run.report(err)
		}
		return 0
	}
	table()
	return 0
}

func (run runner) writeOperation(output string, operation operationDTO) int {
	code := 0
	if operation.State != "succeeded" {
		code = 4
	}
	if output == "json" {
		if err := json.NewEncoder(run.stdout).Encode(operation); err != nil {
			return run.report(err)
		}
	} else {
		fmt.Fprintf(run.stdout, "%s\t%s", operation.ID, operation.State)
		if operation.ErrorCode != "" {
			fmt.Fprintf(run.stdout, "\t%s", operation.ErrorCode)
		}
		fprintln(run.stdout, "")
	}
	return code
}

func (run runner) report(err error) int {
	if err != nil {
		fmt.Fprintln(run.stderr, err)
	}
	return exitCodeFor(err)
}

func (run runner) reportAuth(err error) int {
	if err != nil {
		fmt.Fprintf(run.stderr, "local authentication unavailable: %v\n", err)
	} else {
		fprintln(run.stderr, "local authentication token is unavailable")
	}
	return 5
}

func (run runner) reportWait(err error) int {
	if errors.Is(err, context.Canceled) {
		fprintln(run.stderr, "Stopped waiting; the Operation was not cancelled.")
		return 0
	}
	return run.report(err)
}

func writeStatusTable(output io.Writer, status runtimeStatusDTO) {
	fmt.Fprintf(output, "%s\t%s\n", status.SystemID, status.State)
	for _, service := range status.Services {
		pid := "-"
		if service.PID != nil {
			pid = fmt.Sprint(*service.PID)
		}
		fmt.Fprintf(output, "service\t%s\t%s\t%s\n", service.ServiceID, service.State, pid)
	}
	for _, port := range status.Ports {
		fmt.Fprintf(output, "port\t%s\t%d\t%s\n", port.LogicalName, port.Port, port.Source)
	}
}

func writeLogEntries(output io.Writer, format string, entries []logEntryDTO) {
	encoder := json.NewEncoder(output)
	for _, entry := range entries {
		if format == "json" {
			_ = encoder.Encode(entry)
			continue
		}
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\n", entry.Timestamp, entry.Level, entry.Stream, entry.Message)
	}
}

func fprintln(output io.Writer, value string) { _, _ = fmt.Fprintln(output, value) }

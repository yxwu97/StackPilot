package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func waitForOperation(ctx context.Context, client *apiClient, id string, progress io.Writer) (operationDTO, error) {
	operation, err := getOperation(ctx, client, id)
	if err != nil || terminalOperation(operation.State) {
		return operation, err
	}
	operation, err = waitViaEvents(ctx, client, id, progress)
	if err == nil || ctx.Err() != nil {
		return operation, err
	}
	fprintln(progress, "Event stream unavailable; polling Operation state.")
	return waitViaPolling(ctx, client, id, progress)
}

func waitViaEvents(ctx context.Context, client *apiClient, id string, progress io.Writer) (operationDTO, error) {
	response, err := client.Stream(ctx, "/api/v1/events?cursor=0")
	if err != nil {
		return operationDTO{}, err
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	lastProgress := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event domainEventDTO
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil || event.OperationID != id {
			continue
		}
		operation, err := getOperation(ctx, client, id)
		if err != nil {
			return operationDTO{}, err
		}
		lastProgress = writeOperationProgress(progress, operation, lastProgress)
		if terminalOperation(operation.State) {
			return operation, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return operationDTO{}, err
	}
	return operationDTO{}, fmt.Errorf("event stream closed before Operation completed")
}

func waitViaPolling(ctx context.Context, client *apiClient, id string, progress io.Writer) (operationDTO, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	lastProgress := ""
	for {
		operation, err := getOperation(ctx, client, id)
		if err != nil {
			return operationDTO{}, err
		}
		lastProgress = writeOperationProgress(progress, operation, lastProgress)
		if terminalOperation(operation.State) {
			return operation, nil
		}
		select {
		case <-ctx.Done():
			return operation, ctx.Err()
		case <-ticker.C:
		}
	}
}

func getOperation(ctx context.Context, client *apiClient, id string) (operationDTO, error) {
	var operation operationDTO
	err := client.JSON(ctx, http.MethodGet, "/api/v1/operations/"+id, nil, &operation)
	return operation, err
}

func terminalOperation(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

func writeOperationProgress(output io.Writer, operation operationDTO, previous string) string {
	current := ""
	for _, step := range operation.Steps {
		if step.State == "running" || step.State == "failed" {
			current = step.State + "\t" + step.Key
			break
		}
	}
	if current != "" && current != previous {
		fprintln(output, current)
	}
	return current
}

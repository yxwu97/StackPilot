package events

import (
	"encoding/json"
	"fmt"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/orchestrator"
)

const (
	TypeOperationCreated      = "operation.created"
	TypeOperationStateChanged = "operation.state.changed"
	TypeOperationStepChanged  = "operation.step.changed"
)

// OperationCreated builds the event committed with a newly queued Operation.
func OperationCreated(operation orchestrator.Operation) (Event, error) {
	data, err := json.Marshal(struct {
		OperationType string `json:"operationType"`
		State         string `json:"state"`
	}{OperationType: string(operation.Type), State: string(operation.State)})
	if err != nil {
		return Event{}, fmt.Errorf("encode Operation created event: %w", err)
	}
	return operationEvent(operation, TypeOperationCreated, operation.CreatedAt, data), nil
}

// OperationStateChanged builds one event for a validated Operation transition.
func OperationStateChanged(operation orchestrator.Operation, target domain.OperationState, code string, at time.Time) (Event, error) {
	data, err := json.Marshal(struct {
		From      string `json:"from"`
		To        string `json:"to"`
		ErrorCode string `json:"errorCode,omitempty"`
	}{From: string(operation.State), To: string(target), ErrorCode: code})
	if err != nil {
		return Event{}, fmt.Errorf("encode Operation state event: %w", err)
	}
	return operationEvent(operation, TypeOperationStateChanged, at, data), nil
}

// OperationStepChanged builds one event for a validated structured-step transition.
func OperationStepChanged(operation orchestrator.Operation, step orchestrator.Step, target domain.OperationStepState, code string, at time.Time) (Event, error) {
	data, err := json.Marshal(struct {
		StepNumber int    `json:"stepNumber"`
		StepKey    string `json:"stepKey"`
		From       string `json:"from"`
		To         string `json:"to"`
		ErrorCode  string `json:"errorCode,omitempty"`
	}{StepNumber: step.Number, StepKey: step.Key, From: string(step.State), To: string(target), ErrorCode: code})
	if err != nil {
		return Event{}, fmt.Errorf("encode Operation step event: %w", err)
	}
	return operationEvent(operation, TypeOperationStepChanged, at, data), nil
}

func operationEvent(operation orchestrator.Operation, eventType string, at time.Time, data json.RawMessage) Event {
	return Event{
		Type: eventType, OccurredAt: at.UTC(), WorkspaceID: operation.WorkspaceID,
		SystemID: operation.SystemID, OperationID: operation.ID, Data: data,
	}
}

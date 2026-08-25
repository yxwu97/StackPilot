package events

import (
	"encoding/json"
	"fmt"
	"time"

	"stackpilot/internal/domain"
)

const (
	TypeSystemInstanceCreated  = "system.instance.created"
	TypeSystemStateChanged     = "system.state.changed"
	TypeServiceInstanceCreated = "service.instance.created"
	TypeServiceStateChanged    = "service.state.changed"
)

// SystemInstanceCreated builds the event committed with a new runtime instance.
func SystemInstanceCreated(operationID domain.OperationID, instance domain.SystemInstance) (Event, error) {
	data, err := json.Marshal(struct {
		State string `json:"state"`
	}{State: string(instance.State)})
	if err != nil {
		return Event{}, fmt.Errorf("encode system instance event: %w", err)
	}
	return runtimeEvent(operationID, instance, "", TypeSystemInstanceCreated, instance.StartedAt, data), nil
}

// SystemStateChanged builds an aggregate-state transition event.
func SystemStateChanged(operationID domain.OperationID, instance domain.SystemInstance, target domain.SystemState, at time.Time) (Event, error) {
	data, err := stateChangeData(string(instance.State), string(target), "")
	if err != nil {
		return Event{}, err
	}
	return runtimeEvent(operationID, instance, "", TypeSystemStateChanged, at, data), nil
}

// ServiceInstanceCreated builds the event committed with a new service runtime.
func ServiceInstanceCreated(operationID domain.OperationID, system domain.SystemInstance, service domain.ServiceInstance) (Event, error) {
	data, err := json.Marshal(struct {
		ServiceID string `json:"serviceId"`
		State     string `json:"state"`
	}{ServiceID: service.ServiceID.String(), State: string(service.State)})
	if err != nil {
		return Event{}, fmt.Errorf("encode service instance event: %w", err)
	}
	return runtimeEvent(operationID, system, service.ID, TypeServiceInstanceCreated, service.CreatedAt, data), nil
}

// ServiceStateChanged builds one optimistic service-state transition event.
func ServiceStateChanged(operationID domain.OperationID, system domain.SystemInstance, service domain.ServiceInstance, target domain.ServiceState, code string, at time.Time) (Event, error) {
	data, err := stateChangeData(string(service.State), string(target), code)
	if err != nil {
		return Event{}, err
	}
	return runtimeEvent(operationID, system, service.ID, TypeServiceStateChanged, at, data), nil
}

func stateChangeData(from, to, code string) (json.RawMessage, error) {
	data, err := json.Marshal(struct {
		From      string `json:"from"`
		To        string `json:"to"`
		ErrorCode string `json:"errorCode,omitempty"`
	}{From: from, To: to, ErrorCode: code})
	if err != nil {
		return nil, fmt.Errorf("encode runtime state event: %w", err)
	}
	return data, nil
}

func runtimeEvent(operationID domain.OperationID, system domain.SystemInstance, serviceID domain.ServiceInstanceID, eventType string, at time.Time, data json.RawMessage) Event {
	return Event{
		Type: eventType, OccurredAt: at.UTC(), WorkspaceID: system.WorkspaceID, SystemID: system.SystemID,
		InstanceID: system.ID, ServiceInstanceID: serviceID, OperationID: operationID, Data: data,
	}
}

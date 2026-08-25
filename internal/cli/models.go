package cli

type workspaceDTO struct {
	ID             string `json:"id"`
	SystemID       string `json:"systemId"`
	SystemName     string `json:"systemName"`
	Path           string `json:"path"`
	ManifestStatus string `json:"manifestStatus"`
	ManifestDigest string `json:"manifestDigest"`
	LastErrorCode  string `json:"lastErrorCode,omitempty"`
	ServiceCount   int    `json:"serviceCount"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

type workspaceListDTO struct {
	Items []workspaceDTO `json:"items"`
}

type workspaceProbeDTO struct {
	State      string `json:"state"`
	Path       string `json:"path"`
	Candidates []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	} `json:"candidates"`
	HandoffURL string `json:"handoffUrl,omitempty"`
}

type operationRefDTO struct {
	OperationID string `json:"operationId"`
	State       string `json:"state"`
}

type operationDTO struct {
	ID          string             `json:"id"`
	WorkspaceID string             `json:"workspaceId"`
	SystemID    string             `json:"systemId"`
	Type        string             `json:"type"`
	State       string             `json:"state"`
	ErrorCode   string             `json:"errorCode,omitempty"`
	CreatedAt   string             `json:"createdAt"`
	Cancellable bool               `json:"cancellable"`
	Steps       []operationStepDTO `json:"steps"`
}

type operationStepDTO struct {
	Number    int    `json:"number"`
	Key       string `json:"key"`
	State     string `json:"state"`
	ErrorCode string `json:"errorCode,omitempty"`
}

type runtimeStatusDTO struct {
	SystemID    string              `json:"systemId"`
	WorkspaceID string              `json:"workspaceId"`
	State       string              `json:"state"`
	InstanceID  string              `json:"instanceId,omitempty"`
	Services    []serviceRuntimeDTO `json:"services"`
	Ports       []portRuntimeDTO    `json:"ports"`
}

type serviceRuntimeDTO struct {
	ServiceID         string                       `json:"serviceId"`
	ServiceInstanceID string                       `json:"serviceInstanceId"`
	Driver            string                       `json:"driver"`
	Mode              string                       `json:"mode"`
	State             string                       `json:"state"`
	PID               *int                         `json:"pid,omitempty"`
	Containers        []composeContainerRuntimeDTO `json:"containers"`
}

type composeContainerRuntimeDTO struct {
	Service  string `json:"service"`
	State    string `json:"state"`
	Health   string `json:"health"`
	ExitCode int    `json:"exitCode"`
}

type portRuntimeDTO struct {
	LogicalName string `json:"logicalName"`
	Port        int    `json:"port"`
	Source      string `json:"source"`
}

type logPageDTO struct {
	Items []logEntryDTO `json:"items"`
}

type logEntryDTO struct {
	Timestamp   string `json:"timestamp"`
	SystemID    string `json:"systemId"`
	InstanceID  string `json:"instanceId"`
	ServiceID   string `json:"serviceId"`
	Stream      string `json:"stream"`
	Level       string `json:"level"`
	Message     string `json:"message"`
	OperationID string `json:"operationId,omitempty"`
	Sequence    int64  `json:"sequence"`
	Truncated   bool   `json:"truncated"`
}

type domainEventDTO struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	OperationID string `json:"operationId"`
}

type secretMetadataDTO struct {
	ID        string `json:"id"`
	SystemID  string `json:"systemId"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Version   int64  `json:"version"`
	UpdatedAt string `json:"updatedAt"`
}

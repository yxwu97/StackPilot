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
	DetailRef string `json:"detailRef,omitempty"`
}

type revisionSummaryDTO struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	SystemID    string `json:"systemId"`
	Kind        string `json:"kind"`
	Digest      string `json:"digest"`
	CreatedAt   string `json:"createdAt"`
}

type changePlanItemDTO struct {
	Kind    string `json:"kind"`
	Change  string `json:"change"`
	Risk    string `json:"risk"`
	Key     string `json:"key"`
	Summary string `json:"summary"`
}

type changePlanDTO struct {
	ID                   string              `json:"id"`
	WorkspaceID          string              `json:"workspaceId"`
	SystemID             string              `json:"systemId"`
	CreatedByOperationID string              `json:"createdByOperationId"`
	FromRevision         revisionSummaryDTO  `json:"fromRevision"`
	ToRevision           revisionSummaryDTO  `json:"toRevision"`
	RuleVersion          string              `json:"ruleVersion"`
	State                string              `json:"state"`
	Risk                 string              `json:"risk"`
	ItemCount            int                 `json:"itemCount"`
	BlockedCount         int                 `json:"blockedCount"`
	Items                []changePlanItemDTO `json:"items"`
	CreatedAt            string              `json:"createdAt"`
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

type metricPointDTO struct {
	ObservedAt     string   `json:"observedAt"`
	Status         string   `json:"status"`
	CPUPercent     *float64 `json:"cpuPercent,omitempty"`
	MemoryBytes    *int64   `json:"memoryBytes,omitempty"`
	ProcessCount   *int64   `json:"processCount,omitempty"`
	ContainerCount *int64   `json:"containerCount,omitempty"`
	ReasonCode     string   `json:"reasonCode,omitempty"`
}

type metricSeriesDTO struct {
	ServiceID string           `json:"serviceId"`
	Source    string           `json:"source"`
	Points    []metricPointDTO `json:"points"`
}

type metricSeriesListDTO struct {
	From        string            `json:"from"`
	To          string            `json:"to"`
	Granularity string            `json:"granularity"`
	Series      []metricSeriesDTO `json:"series"`
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

package manifest

// Document is a structurally valid v1alpha1 manifest and its JSON representation.
type Document struct {
	Manifest Manifest
	JSON     []byte
}

// Manifest is the YAML boundary model for a StackPilot System.
type Manifest struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

// Metadata describes stable user-facing system identity.
type Metadata struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// Spec contains policies, logical ports, and service declarations.
type Spec struct {
	Capabilities map[string]bool    `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Policies     Policies           `yaml:"policies,omitempty" json:"policies,omitempty"`
	Ports        map[string]Port    `yaml:"ports,omitempty" json:"ports,omitempty"`
	Services     map[string]Service `yaml:"services" json:"services"`
}

// Policies contains system-level orchestration policy inputs.
type Policies struct {
	FailFast          *bool  `yaml:"failFast,omitempty" json:"failFast,omitempty"`
	CleanupOnFailure  *bool  `yaml:"cleanupOnFailure,omitempty" json:"cleanupOnFailure,omitempty"`
	KeepReadyServices *bool  `yaml:"keepReadyServices,omitempty" json:"keepReadyServices,omitempty"`
	StickyPorts       *bool  `yaml:"stickyPorts,omitempty" json:"stickyPorts,omitempty"`
	StartTimeout      string `yaml:"startTimeout,omitempty" json:"startTimeout,omitempty"`
	StopTimeout       string `yaml:"stopTimeout,omitempty" json:"stopTimeout,omitempty"`
}

// Port declares one logical local port.
type Port struct {
	Protocol       string `yaml:"protocol" json:"protocol"`
	Preferred      *int   `yaml:"preferred,omitempty" json:"preferred,omitempty"`
	FallbackRange  string `yaml:"fallbackRange,omitempty" json:"fallbackRange,omitempty"`
	ConflictPolicy string `yaml:"conflictPolicy,omitempty" json:"conflictPolicy,omitempty"`
	Exposure       string `yaml:"exposure,omitempty" json:"exposure,omitempty"`
}

// Service declares one managed service definition.
type Service struct {
	DisplayName        string            `yaml:"displayName,omitempty" json:"displayName,omitempty"`
	Required           *bool             `yaml:"required,omitempty" json:"required,omitempty"`
	Driver             string            `yaml:"driver" json:"driver"`
	Mode               string            `yaml:"mode,omitempty" json:"mode,omitempty"`
	Runner             string            `yaml:"runner,omitempty" json:"runner"`
	VirtualEnvironment string            `yaml:"virtualEnvironment,omitempty" json:"virtualEnvironment,omitempty"`
	Compose            *ComposeService   `yaml:"compose,omitempty" json:"compose,omitempty"`
	WorkingDirectory   string            `yaml:"workingDirectory,omitempty" json:"workingDirectory"`
	Arguments          []string          `yaml:"arguments,omitempty" json:"arguments"`
	Environment        map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
	DependsOn          map[string]string `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	Readiness          *HealthCheck      `yaml:"readiness,omitempty" json:"readiness,omitempty"`
	Liveness           *HealthCheck      `yaml:"liveness,omitempty" json:"liveness,omitempty"`
	Stop               Stop              `yaml:"stop,omitempty" json:"stop,omitempty"`
	Restart            Restart           `yaml:"restart,omitempty" json:"restart,omitempty"`
}

// ComposeService references a fixed workspace Compose file and its managed services.
type ComposeService struct {
	File        string                       `yaml:"file" json:"file"`
	Services    []string                     `yaml:"services" json:"services"`
	BuildPolicy string                       `yaml:"buildPolicy,omitempty" json:"buildPolicy,omitempty"`
	Readiness   map[string]string            `yaml:"readiness,omitempty" json:"readiness,omitempty"`
	Ports       map[string]ComposePort       `yaml:"ports,omitempty" json:"ports,omitempty"`
	Environment map[string]map[string]string `yaml:"environment,omitempty" json:"environment,omitempty"`
}

// ComposePort maps one planned loopback host port to a fixed container target.
type ComposePort struct {
	Service string `yaml:"service" json:"service"`
	Target  int    `yaml:"target" json:"target"`
}

// HealthCheck contains structurally known readiness/liveness inputs.
type HealthCheck struct {
	Type             string `yaml:"type" json:"type"`
	URL              string `yaml:"url,omitempty" json:"url,omitempty"`
	Host             string `yaml:"host,omitempty" json:"host,omitempty"`
	Port             any    `yaml:"port,omitempty" json:"port,omitempty"`
	Timeout          string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Interval         string `yaml:"interval,omitempty" json:"interval,omitempty"`
	SuccessThreshold *int   `yaml:"successThreshold,omitempty" json:"successThreshold,omitempty"`
	FailureThreshold *int   `yaml:"failureThreshold,omitempty" json:"failureThreshold,omitempty"`
}

// Stop contains service stop policy inputs.
type Stop struct {
	GracefulTimeout string `yaml:"gracefulTimeout,omitempty" json:"gracefulTimeout,omitempty"`
}

// Restart contains bounded automatic restart policy inputs.
type Restart struct {
	Policy         string `yaml:"policy,omitempty" json:"policy,omitempty"`
	InitialBackoff string `yaml:"initialBackoff,omitempty" json:"initialBackoff,omitempty"`
	MaxBackoff     string `yaml:"maxBackoff,omitempty" json:"maxBackoff,omitempty"`
	MaxAttempts    *int   `yaml:"maxAttempts,omitempty" json:"maxAttempts,omitempty"`
	StableWindow   string `yaml:"stableWindow,omitempty" json:"stableWindow,omitempty"`
}

// Package changeplan builds deterministic, immutable comparisons of persisted revisions.
package changeplan

import (
	"errors"
	"time"

	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
)

const (
	RuleVersion         = "change-risk/v1"
	ResultSchemaVersion = "change-plan-result/v1"
	MaximumItems        = 10000
)

var (
	ErrInvalidInput = errors.New("change plan input is invalid")
	ErrNotFound     = errors.New("change plan was not found")
)

// Change identifies how one comparison key changed.
type Change string

const (
	ChangeAdded   Change = "added"
	ChangeRemoved Change = "removed"
	ChangeChanged Change = "changed"
)

// Item is one stable, safe comparison finding.
type Item struct {
	Kind    domain.ChangeItemKind `json:"kind"`
	Change  Change                `json:"change"`
	Risk    domain.ChangeRisk     `json:"risk"`
	Key     string                `json:"key"`
	Summary string                `json:"summary"`
}

// Result is the canonical persisted comparison result.
type Result struct {
	SchemaVersion string                 `json:"schemaVersion"`
	FromDigest    string                 `json:"fromDigest"`
	ToDigest      string                 `json:"toDigest"`
	RuleVersion   string                 `json:"ruleVersion"`
	State         domain.ChangePlanState `json:"state"`
	Risk          domain.ChangeRisk      `json:"risk"`
	BlockedCount  int                    `json:"blockedCount"`
	Items         []Item                 `json:"items"`
}

// Record is one immutable persisted ChangePlan.
type Record struct {
	ID                   domain.ChangePlanID
	CreatedByOperationID domain.OperationID
	WorkspaceID          domain.WorkspaceID
	SystemID             domain.SystemID
	FromSnapshotID       domain.RevisionID
	ToSnapshotID         domain.RevisionID
	RuleVersion          string
	State                domain.ChangePlanState
	Risk                 domain.ChangeRisk
	ItemCount            int
	BlockedCount         int
	ResultSchemaVersion  string
	ResultDigest         string
	ResultJSON           []byte
	CreatedAt            time.Time
}

// Plan combines a record with its immutable revisions and decoded safe result.
type Plan struct {
	Record Record
	From   revision.Record
	To     revision.Record
	Result Result
}

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"stackpilot/internal/changeplan"
	"stackpilot/internal/domain"
	"stackpilot/internal/revision"
	"stackpilot/internal/workspace"
)

var changePlanStepKeys = []string{"collect-running", "collect-workspace", "compare", "classify-risk", "persist-plan"}

// ChangePlanInput identifies one fixed registered workspace for read-only planning.
type ChangePlanInput struct {
	WorkspaceID        domain.WorkspaceID
	SystemID           domain.SystemID
	IdempotencySubject string
	IdempotencyKey     string
	Request            []byte
}

// SubmitChangePlan creates a read-only asynchronous comparison Operation.
func (service *SingleService) SubmitChangePlan(ctx context.Context, input ChangePlanInput) (*CreateResult, error) {
	if service.config.Revisions == nil || service.config.ChangePlans == nil {
		return nil, ErrInvalidInput
	}
	record, err := service.config.Workspaces.Get(ctx, input.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if record.SystemID != input.SystemID {
		return nil, workspace.ErrNotFound
	}
	result, err := service.config.Operations.Create(ctx, CreateInput{
		WorkspaceID: input.WorkspaceID, SystemID: input.SystemID, Type: domain.OperationChangePlan,
		IdempotencySubject: input.IdempotencySubject, RouteKey: "change-plan:create", IdempotencyKey: input.IdempotencyKey,
		Request: input.Request, Cancellable: true, StepKeys: append([]string(nil), changePlanStepKeys...),
	})
	if err != nil || !result.Created {
		return result, err
	}
	service.launch(result.Operation.ID, func(worker context.Context) { service.runChangePlan(worker, result.Operation) })
	return result, nil
}

// GetChangePlan returns one immutable hydrated plan.
func (service *SingleService) GetChangePlan(ctx context.Context, id domain.ChangePlanID) (*changeplan.Plan, error) {
	if service.config.ChangePlans == nil {
		return nil, ErrInvalidInput
	}
	return service.config.ChangePlans.Get(ctx, id)
}

// GetRevision returns one immutable revision record.
func (service *SingleService) GetRevision(ctx context.Context, id domain.RevisionID) (*revision.Record, error) {
	if service.config.Revisions == nil {
		return nil, ErrInvalidInput
	}
	return service.config.Revisions.Get(ctx, id)
}

// ListRevisions returns the newest running and workspace revisions in stable order.
func (service *SingleService) ListRevisions(ctx context.Context, workspaceID domain.WorkspaceID, limit int) ([]revision.Record, error) {
	if service.config.Revisions == nil || limit < 1 || limit > 100 {
		return nil, ErrInvalidInput
	}
	running, err := service.config.Revisions.ListLatest(ctx, workspaceID, domain.RevisionRunning, limit)
	if err != nil {
		return nil, err
	}
	workspaceValues, err := service.config.Revisions.ListLatest(ctx, workspaceID, domain.RevisionWorkspace, limit)
	if err != nil {
		return nil, err
	}
	values := append(running, workspaceValues...)
	sort.Slice(values, func(left, right int) bool {
		if !values[left].CreatedAt.Equal(values[right].CreatedAt) {
			return values[left].CreatedAt.After(values[right].CreatedAt)
		}
		return values[left].ID > values[right].ID
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (service *SingleService) runChangePlan(ctx context.Context, operation Operation) {
	if _, err := service.config.Operations.Start(ctx, operation.ID); err != nil {
		return
	}
	current := 1
	var from, to *revision.Record
	var result changeplan.Result
	err := service.runStep(ctx, operation.ID, current, func() error {
		var collectErr error
		from, collectErr = service.config.Revisions.Collect(ctx, operation.WorkspaceID, domain.RevisionRunning)
		return collectErr
	})
	if err == nil {
		current = 2
		err = service.runStep(ctx, operation.ID, current, func() error {
			var collectErr error
			to, collectErr = service.config.Revisions.Collect(ctx, operation.WorkspaceID, domain.RevisionWorkspace)
			return collectErr
		})
	}
	if err == nil {
		current = 3
		err = service.runStep(ctx, operation.ID, current, func() error {
			var left, right revision.Snapshot
			if json.Unmarshal(from.JSON, &left) != nil || json.Unmarshal(to.JSON, &right) != nil {
				return changeplan.ErrInvalidInput
			}
			var compareErr error
			result, compareErr = changeplan.Compare(left, right, from.Digest, to.Digest)
			return compareErr
		})
	}
	if err == nil {
		current = 4
		err = service.runStep(ctx, operation.ID, current, func() error { return validateChangePlanResult(result) })
	}
	if err == nil {
		current = 5
		var plan *changeplan.Record
		err = service.runStepDetail(ctx, operation.ID, current, func() (string, error) {
			var persistErr error
			plan, persistErr = service.config.ChangePlans.Persist(ctx, operation.ID, *from, *to, result)
			if persistErr != nil {
				return "", persistErr
			}
			return plan.ID.String(), nil
		})
	}
	service.finishChangePlan(ctx, operation, current, err)
}

func validateChangePlanResult(result changeplan.Result) error {
	if result.SchemaVersion != changeplan.ResultSchemaVersion || result.RuleVersion != changeplan.RuleVersion ||
		result.State.Validate() != nil || result.Risk.Validate() != nil || len(result.Items) > changeplan.MaximumItems {
		return changeplan.ErrInvalidInput
	}
	return nil
}

func (service *SingleService) finishChangePlan(ctx context.Context, operation Operation, current int, failure error) {
	if failure == nil {
		_, _ = service.config.Operations.Succeed(ctx, operation.ID)
		return
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workerFinalizationTimeout)
	defer cancel()
	if errors.Is(context.Cause(ctx), errUserCancellation) {
		service.cancelStepSet(finalCtx, operation.ID, current, len(changePlanStepKeys))
		_, _ = service.config.Operations.CompleteCancellation(finalCtx, operation.ID)
		return
	}
	mappedFailure := failure
	if cause := context.Cause(ctx); cause != nil {
		mappedFailure = errors.Join(failure, cause)
	}
	code := changePlanErrorCode(mappedFailure)
	service.failStep(finalCtx, operation.ID, current, code)
	service.skipStepsAfter(finalCtx, operation, current)
	if _, err := service.config.Operations.Fail(finalCtx, operation.ID, code); err != nil {
		service.logWorkerError(operation.ID, code, err)
	}
}

func (service *SingleService) runStepDetail(ctx context.Context, id domain.OperationID, number int, action func() (string, error)) error {
	if _, err := service.config.Operations.TransitionStep(ctx, id, number, domain.OperationStepRunning, "", ""); err != nil {
		return err
	}
	detail, err := action()
	if err != nil {
		return err
	}
	_, err = service.config.Operations.TransitionStep(ctx, id, number, domain.OperationStepSucceeded, "", detail)
	return err
}

func changePlanErrorCode(err error) string {
	switch {
	case errors.Is(err, revision.ErrSourceChanged):
		return "CHANGE_PLAN_STALE"
	case errors.Is(err, revision.ErrSourceUnsafe):
		return "REVISION_SOURCE_UNSAFE"
	case errors.Is(err, revision.ErrSourceTooLarge):
		return "REVISION_SOURCE_TOO_LARGE"
	case errors.Is(err, revision.ErrSourceUnavailable):
		return "REVISION_SOURCE_UNAVAILABLE"
	case errors.Is(err, context.DeadlineExceeded):
		return "REVISION_SOURCE_UNAVAILABLE"
	default:
		return "INTERNAL_ERROR"
	}
}

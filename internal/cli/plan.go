package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func (run runner) plan(args []string) int {
	flags, config := newCommandFlags("plan", run.stderr)
	wait := flags.Bool("wait", false, "wait for the ChangePlan and print its immutable result")
	if err := flags.Parse(reorderFlagArgs(args, map[string]bool{"--wait": true})); err != nil || flags.NArg() > 1 {
		return 2
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	workspace, err := resolveWorkspace(run.ctx, client, optionalArgument(flags))
	if err != nil {
		return run.report(err)
	}
	operation, err := submitChangePlan(run.ctx, client, workspace)
	if err != nil {
		return run.report(err)
	}
	if !*wait {
		return run.write(config.output, operation, func() { fmt.Fprintln(run.stdout, operation.OperationID) })
	}
	terminal, err := waitForOperation(run.ctx, client, operation.OperationID, run.stderr)
	if err != nil {
		return run.reportWait(err)
	}
	planID, err := persistedChangePlanID(terminal)
	if err != nil {
		return run.report(err)
	}
	plan, err := getChangePlan(run.ctx, client, planID)
	if err != nil {
		return run.report(err)
	}
	return run.write(config.output, plan, func() { writeChangePlanTable(run.stdout, plan) })
}

func submitChangePlan(ctx context.Context, client *apiClient, workspace workspaceDTO) (operationRefDTO, error) {
	var result operationRefDTO
	path := "/api/v1/workspaces/" + url.PathEscape(workspace.ID) + "/change-plans"
	err := client.JSON(ctx, http.MethodPost, path, nil, &result)
	return result, err
}

func persistedChangePlanID(operation operationDTO) (string, error) {
	if operation.State != "succeeded" {
		return "", commandErrorf("ChangePlan Operation ended in %s", operation.State)
	}
	for _, step := range operation.Steps {
		if step.Key == "persist-plan" && step.State == "succeeded" && step.DetailRef != "" {
			return step.DetailRef, nil
		}
	}
	return "", commandErrorf("ChangePlan Operation has no persisted plan reference")
}

func getChangePlan(ctx context.Context, client *apiClient, id string) (changePlanDTO, error) {
	var result changePlanDTO
	err := client.JSON(ctx, http.MethodGet, "/api/v1/change-plans/"+url.PathEscape(id), nil, &result)
	return result, err
}

func writeChangePlanTable(output interface{ Write([]byte) (int, error) }, plan changePlanDTO) {
	fmt.Fprintf(output, "%s\t%s\t%s\titems=%d\tblocked=%d\n", plan.ID, plan.State, plan.Risk, plan.ItemCount, plan.BlockedCount)
	for _, finding := range plan.Items {
		fmt.Fprintf(output, "%s\t%s\t%s\t%s\t%s\n", finding.Risk, finding.Kind, finding.Change, finding.Key, finding.Summary)
	}
}

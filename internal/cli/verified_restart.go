package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

func (run runner) verifiedRestart(args []string) int {
	flags, config := newCommandFlags("verified-restart", run.stderr)
	planID := flags.String("plan", "", "immutable ChangePlan identifier")
	wait := flags.Bool("wait", false, "wait for stability verification to finish")
	if err := flags.Parse(reorderFlagArgs(args, map[string]bool{"--wait": true})); err != nil || flags.NArg() > 1 || *planID == "" {
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
	operation, err := submitVerifiedRestart(run.ctx, client, workspace, *planID)
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
	return run.writeOperation(config.output, terminal)
}

func submitVerifiedRestart(ctx context.Context, client *apiClient, workspace workspaceDTO, planID string) (operationRefDTO, error) {
	var result operationRefDTO
	path := "/api/v1/workspaces/" + url.PathEscape(workspace.ID) + "/verified-restart"
	err := client.JSON(ctx, http.MethodPost, path, map[string]string{"changePlanId": planID}, &result)
	return result, err
}

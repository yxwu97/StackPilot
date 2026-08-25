package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func (run runner) logs(args []string) int {
	flags, config := newCommandFlags("logs", run.stderr)
	follow := flags.Bool("follow", false, "follow new log entries")
	if err := flags.Parse(reorderFlagArgs(args, map[string]bool{"--follow": true})); err != nil || flags.NArg() != 1 {
		return 2
	}
	systemID, serviceID, ok := parseServiceTarget(flags.Arg(0))
	if !ok {
		fprintln(run.stderr, "logs target must be system/service")
		return 2
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	workspace, err := resolveWorkspace(run.ctx, client, systemID)
	if err != nil {
		return run.report(err)
	}
	status, err := getStatus(run.ctx, client, workspace)
	if err != nil {
		return run.report(err)
	}
	if status.InstanceID == "" {
		fmt.Fprintf(run.stderr, "system %q has no runtime instance\n", systemID)
		return 4
	}
	page, err := loadLogPage(run.ctx, client, systemID, serviceID, status.InstanceID)
	if err != nil {
		return run.report(err)
	}
	if config.output == "json" && !*follow {
		if err := json.NewEncoder(run.stdout).Encode(page.Items); err != nil {
			return run.report(err)
		}
	} else {
		writeLogEntries(run.stdout, config.output, page.Items)
	}
	if !*follow {
		return 0
	}
	if err := followLogs(run, client, serviceID, status.InstanceID, lastSequence(page.Items), config.output); err != nil {
		return run.reportWait(err)
	}
	return 0
}

func parseServiceTarget(value string) (string, string, bool) {
	parts := strings.Split(value, "/")
	returnValue := len(parts) == 2 && parts[0] != "" && parts[1] != ""
	if !returnValue {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func loadLogPage(ctx context.Context, client *apiClient, systemID, serviceID, instanceID string) (logPageDTO, error) {
	var page logPageDTO
	query := url.Values{"instanceId": []string{instanceID}, "limit": []string{"5000"}}
	path := "/api/v1/services/" + url.PathEscape(systemID) + "/" + url.PathEscape(serviceID) + "/logs?" + query.Encode()
	err := client.JSON(ctx, http.MethodGet, path, nil, &page)
	return page, err
}

func followLogs(run runner, client *apiClient, serviceID, instanceID string, after int64, output string) error {
	query := url.Values{"instanceId": []string{instanceID}, "serviceId": []string{serviceID}, "afterSequence": []string{fmt.Sprint(after)}}
	response, err := client.Stream(run.ctx, "/api/v1/log-stream?"+query.Encode())
	if err != nil {
		return err
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if line := scanner.Text(); strings.HasPrefix(line, "data: ") {
			var entry logEntryDTO
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &entry) == nil {
				writeLogEntries(run.stdout, output, []logEntryDTO{entry})
			}
		}
	}
	return scanner.Err()
}

func lastSequence(entries []logEntryDTO) int64 {
	if len(entries) == 0 {
		return 0
	}
	return entries[len(entries)-1].Sequence
}

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"stackpilot/internal/platform"
	"stackpilot/internal/security"
)

type commandConfig struct {
	serverURL string
	dataDir   string
	output    string
}

type runner struct {
	ctx            context.Context
	stdin          io.Reader
	stdout, stderr io.Writer
}

// IsCommand reports whether name belongs to the API-client command surface.
func IsCommand(name string) bool {
	switch name {
	case "open", "workspace", "up", "down", "status", "logs", "wait", "secret":
		return true
	default:
		return false
	}
}

// Run executes one authenticated CLI API command.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !IsCommand(args[0]) {
		return 2
	}
	run := runner{ctx: ctx, stdin: os.Stdin, stdout: stdout, stderr: stderr}
	switch args[0] {
	case "open":
		return run.open(args[1:])
	case "workspace":
		return run.workspace(args[1:])
	case "up", "down":
		return run.lifecycle(args[0], args[1:])
	case "status":
		return run.status(args[1:])
	case "logs":
		return run.logs(args[1:])
	case "wait":
		return run.wait(args[1:])
	case "secret":
		return run.secret(args[1:])
	default:
		return 2
	}
}

func (run runner) open(args []string) int {
	flags, config := newCommandFlags("open", run.stderr)
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	if err := openWeb(run.ctx, client); err != nil {
		return run.report(err)
	}
	fprintln(run.stdout, "StackPilot Web console opened.")
	return 0
}

func (run runner) workspace(args []string) int {
	if len(args) == 0 || args[0] != "add" {
		fprintln(run.stderr, "Usage: stackpilot workspace add [flags] <workspace>")
		return 2
	}
	flags, config := newCommandFlags("workspace add", run.stderr)
	open := flags.Bool("open", false, "open the authenticated workspace initialization flow")
	if err := flags.Parse(reorderFlagArgs(args[1:], map[string]bool{"--open": true})); err != nil || flags.NArg() != 1 {
		return 2
	}
	path, err := filepath.Abs(flags.Arg(0))
	if err != nil {
		return run.report(err)
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	var probe workspaceProbeDTO
	if err := client.JSON(run.ctx, http.MethodPost, "/api/v1/workspaces/probe", map[string]string{"path": path}, &probe); err != nil {
		return run.report(err)
	}
	if probe.State == "initialization_required" {
		probe.HandoffURL = client.baseURL + "/#workspace-import=" + url.QueryEscape(path)
		if *open && config.output != "json" {
			if err := openWorkspaceImport(run.ctx, client, path); err != nil {
				return run.report(err)
			}
		}
		if code := run.write(config.output, probe, func() { fmt.Fprintf(run.stdout, "initialization_required\t%s\n", path) }); code != 0 {
			return code
		}
		return 3
	}
	var workspace workspaceDTO
	if err := client.JSON(run.ctx, http.MethodPost, "/api/v1/workspaces", map[string]string{"path": path}, &workspace); err != nil {
		return run.report(err)
	}
	return run.write(config.output, workspace, func() { fmt.Fprintf(run.stdout, "%s\t%s\t%s\n", workspace.ID, workspace.SystemID, workspace.Path) })
}

func (run runner) lifecycle(action string, args []string) int {
	flags, config := newCommandFlags(action, run.stderr)
	wait := flags.Bool("wait", false, "wait for the Operation to reach a terminal state")
	open := flags.Bool("open", false, "open the authenticated Web console after success")
	if err := flags.Parse(reorderFlagArgs(args, map[string]bool{"--wait": true, "--open": true})); err != nil || flags.NArg() > 1 {
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
	operation, err := submitLifecycle(run.ctx, client, action, workspace)
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
	if *open && terminal.State == "succeeded" {
		if err := openWeb(run.ctx, client); err != nil {
			fmt.Fprintf(run.stderr, "open Web console: %v\n", err)
		}
	}
	return run.writeOperation(config.output, terminal)
}

func submitLifecycle(ctx context.Context, client *apiClient, action string, workspace workspaceDTO) (operationRefDTO, error) {
	var result operationRefDTO
	path := "/api/v1/systems/" + url.PathEscape(workspace.SystemID) + "/" + map[string]string{"up": "start", "down": "stop"}[action]
	input := map[string]string{"workspaceId": workspace.ID}
	err := client.JSON(ctx, http.MethodPost, path, input, &result)
	return result, err
}

func (run runner) status(args []string) int {
	flags, config := newCommandFlags("status", run.stderr)
	if err := flags.Parse(reorderFlagArgs(args, nil)); err != nil || flags.NArg() > 1 {
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
	status, err := getStatus(run.ctx, client, workspace)
	if err != nil {
		return run.report(err)
	}
	return run.write(config.output, status, func() { writeStatusTable(run.stdout, status) })
}

func getStatus(ctx context.Context, client *apiClient, workspace workspaceDTO) (runtimeStatusDTO, error) {
	var status runtimeStatusDTO
	query := url.Values{"workspaceId": []string{workspace.ID}}
	path := "/api/v1/systems/" + url.PathEscape(workspace.SystemID) + "/status?" + query.Encode()
	err := client.JSON(ctx, http.MethodGet, path, nil, &status)
	return status, err
}

func (run runner) wait(args []string) int {
	flags, config := newCommandFlags("wait", run.stderr)
	if err := flags.Parse(reorderFlagArgs(args, nil)); err != nil || flags.NArg() != 1 {
		return 2
	}
	client, code := run.connect(config)
	if code != 0 {
		return code
	}
	defer client.Close()
	operation, err := waitForOperation(run.ctx, client, flags.Arg(0), run.stderr)
	if err != nil {
		return run.reportWait(err)
	}
	return run.writeOperation(config.output, operation)
}

func newCommandFlags(name string, output io.Writer) (*flag.FlagSet, *commandConfig) {
	config := &commandConfig{serverURL: defaultServerURL, output: "table"}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&config.serverURL, "server", defaultServerURL, "loopback StackPilot server URL")
	flags.StringVar(&config.dataDir, "data-dir", "", "control-plane data directory")
	flags.StringVar(&config.output, "output", "table", "output format: table or json")
	return flags, config
}

func (run runner) connect(config *commandConfig) (*apiClient, int) {
	if config.output != "table" && config.output != "json" {
		fprintln(run.stderr, "output must be table or json")
		return nil, 2
	}
	if config.dataDir == "" {
		var err error
		config.dataDir, err = platform.DefaultDataDir()
		if err != nil {
			return nil, run.report(err)
		}
	}
	absolute, err := filepath.Abs(config.dataDir)
	if err != nil {
		return nil, run.report(err)
	}
	config.dataDir = absolute
	store, err := security.NewOSTokenStore(config.dataDir)
	if err != nil {
		return nil, run.reportAuth(err)
	}
	token, found, err := store.Load()
	if err != nil || !found {
		return nil, run.reportAuth(err)
	}
	defer erase(token)
	client, err := newAPIClient(config.serverURL, token)
	if err != nil {
		return nil, run.report(err)
	}
	return client, 0
}

func optionalArgument(flags *flag.FlagSet) string {
	if flags.NArg() == 1 {
		return flags.Arg(0)
	}
	return ""
}

func reorderFlagArgs(args []string, booleanFlags map[string]bool) []string {
	options, positional := make([]string, 0, len(args)), make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		value := args[index]
		if !strings.HasPrefix(value, "-") || value == "-" {
			positional = append(positional, value)
			continue
		}
		options = append(options, value)
		name := strings.SplitN(value, "=", 2)[0]
		if strings.Contains(value, "=") || booleanFlags[name] {
			continue
		}
		if index+1 < len(args) {
			index++
			options = append(options, args[index])
		}
	}
	return append(options, positional...)
}

func newIdempotencyKey() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return ""
	}
	return hex.EncodeToString(value)
}

func openWeb(ctx context.Context, client *apiClient) error {
	return openWebFragment(ctx, client, "")
}

func openWorkspaceImport(ctx context.Context, client *apiClient, path string) error {
	return openWebFragment(ctx, client, "workspace-import="+url.QueryEscape(path))
}

func openWebFragment(ctx context.Context, client *apiClient, fragment string) error {
	var bootstrap struct {
		Code string `json:"code"`
	}
	if err := client.JSON(ctx, http.MethodPost, "/api/v1/auth/bootstrap", nil, &bootstrap); err != nil {
		return err
	}
	if bootstrap.Code == "" {
		return fmt.Errorf("empty browser bootstrap")
	}
	value := "bootstrap=" + url.QueryEscape(bootstrap.Code)
	if fragment != "" {
		value += "&" + fragment
	}
	return openBrowser(client.baseURL + "/#" + value)
}

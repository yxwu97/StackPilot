//go:build windows

package supervisorspike

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func inspectRunningExperiment(config experimentConfig, pipeName string, launcherPID uint32) (spikeReport, uint32, error) {
	record := launchRecord{}
	if err := readJSON(filepath.Join(config.workDir, "launch.json"), &record); err != nil {
		return spikeReport{}, 0, fmt.Errorf("read launcher record: %w", err)
	}
	report := spikeReport{Profile: config.profile, LauncherPID: launcherPID, SupervisorPID: record.SupervisorPID, LauncherExited: !processAlive(launcherPID)}
	if err := waitForSupervisorFiles(config.workDir, record.SupervisorPID); err != nil {
		return report, record.SupervisorPID, err
	}
	supervisor := supervisorIdentity{}
	worker := runtimeIdentity{}
	resumed := resumeRecord{}
	if err := readJSON(filepath.Join(config.workDir, "supervisor.json"), &supervisor); err != nil {
		return report, record.SupervisorPID, err
	}
	if err := readJSON(filepath.Join(config.workDir, "identity.json"), &worker); err != nil {
		return report, record.SupervisorPID, err
	}
	if err := readJSON(filepath.Join(config.workDir, "resumed.json"), &resumed); err != nil {
		return report, record.SupervisorPID, err
	}
	if supervisor.PID != record.SupervisorPID {
		return report, record.SupervisorPID, fmt.Errorf("Supervisor PID record mismatch")
	}
	if err := verifyExperimentIdentities(supervisor, worker, pipeName, resumed); err != nil {
		return report, record.SupervisorPID, err
	}
	report.WorkerPID = worker.PID
	report.IdentityRecovered = true
	report.IdentityBeforeResume = worker.WrittenBeforeResume && !worker.IdentityFileWrittenAt.After(resumed.ResumedAt)
	if !report.IdentityBeforeResume {
		return report, record.SupervisorPID, fmt.Errorf("identity file was not persisted before resume")
	}
	if err := inspectPipeAndTree(config.profile, pipeName, &report); err != nil {
		return report, record.SupervisorPID, err
	}
	return report, record.SupervisorPID, nil
}

func waitForSupervisorFiles(workDir string, supervisorPID uint32) error {
	paths := []string{"supervisor.json", "identity.json", "resumed.json"}
	return waitUntil(30*time.Second, func() (bool, error) {
		if !processAlive(supervisorPID) {
			return false, fmt.Errorf("Supervisor %d exited before initialization", supervisorPID)
		}
		for _, name := range paths {
			if !fileExists(filepath.Join(workDir, name)) {
				return false, nil
			}
		}
		return true, nil
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func verifyExperimentIdentities(supervisor supervisorIdentity, worker runtimeIdentity, pipeName string, resumed resumeRecord) error {
	if supervisor.PipeName != pipeName || supervisor.ProtocolVersion != protocolVersion || worker.ProtocolVersion != protocolVersion {
		return fmt.Errorf("Supervisor protocol identity mismatch")
	}
	supervisorRuntime := runtimeIdentity{
		PID: supervisor.PID, CreatedAt: supervisor.CreatedAt,
		ExecutablePath: supervisor.ExecutablePath, AccountSID: supervisor.AccountSID,
	}
	if err := verifyProcessIdentity(supervisorRuntime); err != nil {
		return fmt.Errorf("verify Supervisor identity: %w", err)
	}
	if err := verifyProcessIdentity(worker); err != nil {
		return fmt.Errorf("verify worker identity: %w", err)
	}
	if resumed.ResumedAt.IsZero() {
		return fmt.Errorf("worker resume record is missing")
	}
	return nil
}

func inspectPipeAndTree(profile, pipeName string, report *spikeReport) error {
	first, err := exchange(pipeName, "hello")
	if err != nil {
		return err
	}
	if first.SupervisorPID != report.SupervisorPID || first.WorkerPID != report.WorkerPID || first.ProtocolVersion != protocolVersion {
		return fmt.Errorf("hello response identity mismatch")
	}
	allowedSIDs, err := namedPipeAllowedSIDs(pipeName)
	if err != nil {
		return err
	}
	if err := validatePipeSIDs(allowedSIDs); err != nil {
		return err
	}
	second, err := exchange(pipeName, "inspect-service")
	if err != nil {
		return err
	}
	report.PipeReconnected = second.WorkerPID == report.WorkerPID
	report.PipeAllowedSIDs = allowedSIDs
	return waitForExpectedTree(profile, report)
}

func validatePipeSIDs(actual []string) error {
	_, currentSID, err := currentUserPipeSDDL()
	if err != nil {
		return err
	}
	expected := []string{"S-1-5-18", currentSID}
	sort.Strings(expected)
	if strings.Join(actual, ",") != strings.Join(expected, ",") {
		return fmt.Errorf("named pipe allowed SIDs = %v, want %v", actual, expected)
	}
	return nil
}

func waitForExpectedTree(profile string, report *spikeReport) error {
	return waitUntil(30*time.Second, func() (bool, error) {
		tree, err := processTree(report.WorkerPID)
		if err != nil {
			return false, err
		}
		if !treeMatchesProfile(profile, tree) {
			return false, nil
		}
		report.DescendantExecutables = tree
		report.DescendantPIDs = sortedPIDs(tree)
		return true, nil
	})
}

func treeMatchesProfile(profile string, tree map[uint32]string) bool {
	switch profile {
	case "npm":
		return countExecutable(tree, "node.exe") >= 2
	case "maven":
		return containsExecutable(tree, "java.exe")
	default:
		return countExecutable(tree, "supervisor-spike.exe") >= 2
	}
}

func countExecutable(tree map[uint32]string, expected string) int {
	count := 0
	for _, name := range tree {
		if strings.EqualFold(name, expected) {
			count++
		}
	}
	return count
}

func sortedPIDs(tree map[uint32]string) []uint32 {
	result := make([]uint32, 0, len(tree))
	for pid := range tree {
		result = append(result, pid)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func terminateAndVerifyTree(report *spikeReport) error {
	if err := terminateProcess(report.SupervisorPID); err != nil {
		return err
	}
	targets := append([]uint32{report.WorkerPID}, report.DescendantPIDs...)
	err := waitUntil(10*time.Second, func() (bool, error) {
		for _, pid := range targets {
			if processAlive(pid) {
				return false, nil
			}
		}
		return true, nil
	})
	report.TreeTerminated = err == nil
	return err
}

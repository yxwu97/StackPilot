package quality_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	Name        string                 `yaml:"name"`
	Permissions map[string]string      `yaml:"permissions"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	RunsOn string         `yaml:"runs-on"`
	Needs  []string       `yaml:"needs"`
	Steps  []workflowStep `yaml:"steps"`
}

type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

type releaseConfiguration struct {
	Version     int              `yaml:"version"`
	ProjectName string           `yaml:"project_name"`
	Builds      []releaseBuild   `yaml:"builds"`
	Archives    []releaseArchive `yaml:"archives"`
	Checksum    releaseChecksum  `yaml:"checksum"`
}

type releaseBuild struct {
	ID      string   `yaml:"id"`
	Main    string   `yaml:"main"`
	Binary  string   `yaml:"binary"`
	GOOS    []string `yaml:"goos"`
	GOARCH  []string `yaml:"goarch"`
	LDFlags []string `yaml:"ldflags"`
}

type releaseArchive struct {
	IDs     []string `yaml:"ids"`
	Formats []string `yaml:"formats"`
}

type releaseChecksum struct {
	Name      string `yaml:"name_template"`
	Algorithm string `yaml:"algorithm"`
}

func TestCIWorkflowDefinesWindowsMVPGates(t *testing.T) {
	configured := loadWorkflow(t, "ci.yml")
	if configured.Name != "CI" || configured.Permissions["contents"] != "read" {
		t.Fatalf("workflow identity/permissions = (%q, %v), want CI with read-only contents", configured.Name, configured.Permissions)
	}
	assertRunner(t, configured, "quality", "windows-latest")
	assertRunner(t, configured, "cross-compile", "ubuntu-latest")
	assertRunner(t, configured, "windows-artifact", "windows-latest")

	quality := combinedSteps(configured.Jobs["quality"])
	for _, required := range []string{"npm ci", "npm run type-check", "npm run build:web", "go test ./...", "go vet ./...", "go fmt ./..."} {
		if !strings.Contains(quality, required) {
			t.Errorf("quality job does not contain %q", required)
		}
	}
	crossCompile := combinedSteps(configured.Jobs["cross-compile"])
	if !strings.Contains(crossCompile, "go build -trimpath") {
		t.Error("cross-compile job does not build the Go command")
	}
	artifact := combinedSteps(configured.Jobs["windows-artifact"])
	for _, required := range []string{"goreleaser/goreleaser-action@v6", "v2.12.7", "release --snapshot --clean", "checksums.txt", "actions/upload-artifact@v4"} {
		if !strings.Contains(artifact, required) {
			t.Errorf("Windows artifact job does not contain %q", required)
		}
	}
}

func TestReleaseWorkflowPublishesTaggedArtifacts(t *testing.T) {
	configured := loadWorkflow(t, "release.yml")
	if configured.Name != "Release" || configured.Permissions["contents"] != "write" {
		t.Fatalf("release identity/permissions = (%q, %v)", configured.Name, configured.Permissions)
	}
	assertRunner(t, configured, "release", "windows-latest")
	steps := combinedSteps(configured.Jobs["release"])
	for _, required := range []string{"scripts/check.ps1", "goreleaser/goreleaser-action@v6", "v2.12.7", "release --clean"} {
		if !strings.Contains(steps, required) {
			t.Errorf("release job does not contain %q", required)
		}
	}
}

func TestGoReleaserBuildsVersionedWindowsZipAndChecksum(t *testing.T) {
	contents := readProjectFile(t, ".goreleaser.yml")
	var configured releaseConfiguration
	if err := yaml.Unmarshal(contents, &configured); err != nil {
		t.Fatalf("parse GoReleaser configuration: %v", err)
	}
	if configured.Version != 2 || configured.ProjectName != "stackpilot" || len(configured.Builds) != 1 {
		t.Fatalf("GoReleaser identity/builds = (%d, %q, %d)", configured.Version, configured.ProjectName, len(configured.Builds))
	}
	build := configured.Builds[0]
	if build.ID != "stackpilot-windows" || build.Main != "./cmd/stackpilot" || build.Binary != "stackpilot" {
		t.Fatalf("unexpected release build: %+v", build)
	}
	if strings.Join(build.GOOS, ",") != "windows" || strings.Join(build.GOARCH, ",") != "amd64" {
		t.Fatalf("release targets = %v/%v", build.GOOS, build.GOARCH)
	}
	flags := strings.Join(build.LDFlags, " ")
	for _, variable := range []string{"buildinfo.Version", "buildinfo.Commit", "buildinfo.BuildTime"} {
		if !strings.Contains(flags, variable) {
			t.Errorf("release ldflags do not inject %s", variable)
		}
	}
	if len(configured.Archives) != 1 || strings.Join(configured.Archives[0].Formats, ",") != "zip" {
		t.Fatalf("release archive is not one zip: %+v", configured.Archives)
	}
	if configured.Checksum.Name != "checksums.txt" || configured.Checksum.Algorithm != "sha256" {
		t.Fatalf("release checksum = %+v", configured.Checksum)
	}
}

func loadWorkflow(t *testing.T, name string) workflow {
	t.Helper()
	contents := readProjectFile(t, ".github", "workflows", name)
	var configured workflow
	if err := yaml.Unmarshal(contents, &configured); err != nil {
		t.Fatalf("parse workflow %s: %v", name, err)
	}
	return configured
}

func readProjectFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate workflow test source")
	}
	path := filepath.Join(append([]string{filepath.Dir(filename), "..", ".."}, parts...)...)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read project file %s: %v", path, err)
	}
	return contents
}

func assertRunner(t *testing.T, configured workflow, name, runner string) {
	t.Helper()
	job, ok := configured.Jobs[name]
	if !ok {
		t.Errorf("workflow job %q is missing", name)
		return
	}
	if job.RunsOn != runner {
		t.Errorf("job %q runs-on = %q, want %q", name, job.RunsOn, runner)
	}
}

func combinedSteps(job workflowJob) string {
	var values []string
	for _, step := range job.Steps {
		values = append(values, step.Name, step.Uses, step.Run)
		for key, value := range step.With {
			values = append(values, key, value)
		}
	}
	return strings.Join(values, "\n")
}

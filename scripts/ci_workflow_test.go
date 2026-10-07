package scripts

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ciWorkflow is the pull-request pipeline. It is guarded for one narrow reason: the Windows
// tray shell is compiled by no other job (the default build never enables the tag, and the
// release workflow only packages it on a tag), so a change that breaks the native shell can
// only be caught here.
const ciWorkflow = "../.github/workflows/ci.yml"

func loadWorkflowFile(t *testing.T, path string) workflowFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var parsed workflowFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return parsed
}

// trayBuild and trayVet are the exact commands the job must run: the package list is
// derived from where a `tray` build constraint actually appears, so widening it to ./...
// (and inheriting the admin bundle requirement) is a mistake the test below forbids.
const (
	trayBuild    = "go build -tags tray " + trayPackages
	trayVet      = "go vet -tags tray " + trayPackages
	trayPackages = "./cmd/tunnelmesh-client-tray ./internal/tray/..."
)

func TestCIWorkflowCompilesTheWindowsTray(t *testing.T) {
	workflow := loadWorkflowFile(t, ciWorkflow)
	job := requireJob(t, workflow, windowsTrayJob)

	if !strings.HasPrefix(job.RunsOn, "ubuntu-") {
		t.Errorf("ci jobs.%s runs-on %q, want Linux", windowsTrayJob, job.RunsOn)
	}
	// CGO_ENABLED is job state rather than a command, so it is asserted on the parsed job.
	if got := job.Env["CGO_ENABLED"]; got != "0" {
		t.Errorf("ci jobs.%s sets CGO_ENABLED=%q, want \"0\": the tray shell is cross-compiled "+
			"and a host C toolchain in the build would prove nothing", windowsTrayJob, got)
	}

	body := runs(job)
	for _, want := range []string{
		// Without the tag the tray code is not compiled at all, which is the point of the
		// guard and the thing a job can get wrong silently. The package list is the whole
		// set of directories holding a `tray` build constraint; `./...` is not usable here
		// because it also reaches internal/server, whose `//go:embed all:web_dist` needs the
		// admin bundle this job has no reason to build.
		trayBuild,
		trayVet,
		// -tags tray makes internal/tray/webdist/embed.go live: with no bundle on disk the
		// pattern matches nothing and every Go step in the job fails before checking anything.
		"cd web-tray && npm ci",
		"cd web-tray && npm run build",
		// go vet reads test files, so this is the only place the windows-only client lock
		// test gets compiled without a Windows host.
		"go vet -tags tray ./internal/client",
		// Both architectures: arm64 Windows is cross-compiled and would otherwise ship broken.
		"GOOS=windows GOARCH=",
		"apt-get install -y --no-install-recommends nsis",
		"makensis",
		"installer.nsi",
		"gofmt -l",
		// The bundle tests that reference tray.PanelRoute and the .ico files only exist in
		// these packages, so running them is how they stay true.
		"./deploy/windows",
		"./deploy/macos",
		"./scripts",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("ci jobs.%s must contain %q", windowsTrayJob, want)
		}
	}
	for _, banned := range []string{"go build -tags tray ./...", "go vet -tags tray ./..."} {
		if strings.Contains(body, banned) {
			t.Errorf("ci jobs.%s must not compile with %q: the untagged packages it reaches "+
				"embed a web bundle this job does not build", windowsTrayJob, banned)
		}
	}
	// The CI job must not publish anything.
	if strings.Contains(body, "gh release") || strings.Contains(body, "package-windows-tray.sh") {
		t.Errorf("ci jobs.%s must not publish a release or run the packaging script", windowsTrayJob)
	}
}

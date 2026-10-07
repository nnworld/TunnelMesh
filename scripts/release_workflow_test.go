package scripts

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// releaseWorkflow is the pipeline that publishes a version. It is guarded here rather than
// only in prose because its shape carries a constraint that is easy to break silently: the
// macOS tray client links Cocoa and WebKit through cgo, so it can only be built on a Mac,
// while every other binary is cross-compiled with CGO_ENABLED=0 on Linux. One job cannot do
// both, and a workflow that tries fails in a way that looks like a regression in the three
// portable binaries.
const releaseWorkflow = "../.github/workflows/release.yml"

const (
	trayJobName  = "macos-tray"
	versionJob   = "version"
	releaseJob   = "release"
	trayArtifact = "macos-tray"
	trayScript   = "./scripts/package-macos-tray.sh"
)

type workflowFile struct {
	Name        string            `yaml:"name"`
	Permissions map[string]string `yaml:"permissions"`
	Jobs        map[string]job    `yaml:"jobs"`
}

type job struct {
	RunsOn  string            `yaml:"runs-on"`
	Needs   stringList        `yaml:"needs"`
	Env     map[string]string `yaml:"env"`
	Outputs map[string]string `yaml:"outputs"`
	Steps   []step            `yaml:"steps"`
}

type step struct {
	Name string         `yaml:"name"`
	Uses string         `yaml:"uses"`
	Run  string         `yaml:"run"`
	With map[string]any `yaml:"with"`
	Env  map[string]any `yaml:"env"`
}

// stringList accepts both `needs: version` and `needs: [a, b]`. Both are valid workflow
// syntax, and a guard that only understands one of them fails on the spelling nobody
// happened to write, which trains reviewers to ignore it.
type stringList []string

func (list *stringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		var items []string
		if err := node.Decode(&items); err != nil {
			return err
		}
		*list = items
		return nil
	}
	var single string
	if err := node.Decode(&single); err != nil {
		return err
	}
	*list = []string{single}
	return nil
}

func loadWorkflow(t *testing.T) workflowFile {
	t.Helper()
	return loadWorkflowFile(t, releaseWorkflow)
}

func requireJob(t *testing.T, workflow workflowFile, name string) job {
	t.Helper()
	found, ok := workflow.Jobs[name]
	if !ok {
		t.Fatalf("%s has no jobs.%s (jobs: %s)", releaseWorkflow, name, strings.Join(jobNames(workflow), ", "))
	}
	return found
}

func jobNames(workflow workflowFile) []string {
	var names []string
	for name := range workflow.Jobs {
		names = append(names, name)
	}
	return names
}

// runs collects every `run:` body of a job so an assertion can search the job as a whole.
func runs(target job) string {
	var combined []string
	for _, entry := range target.Steps {
		if entry.Run != "" {
			combined = append(combined, entry.Run)
		}
	}
	return strings.Join(combined, "\n")
}

func stepUsing(target job, prefix string) *step {
	for i := range target.Steps {
		if strings.HasPrefix(target.Steps[i].Uses, prefix) {
			return &target.Steps[i]
		}
	}
	return nil
}

// TestReleaseWorkflowResolvesTheVersionOnce keeps the two build jobs on one version. A tag
// push and a manual dispatch resolve it differently, and if each job did that itself they
// could publish a tray named for one version inside a release titled with another.
func TestReleaseWorkflowResolvesTheVersionOnce(t *testing.T) {
	workflow := loadWorkflow(t)
	version := requireJob(t, workflow, versionJob)

	if _, ok := version.Outputs["version"]; !ok {
		t.Errorf("jobs.%s must expose a `version` output", versionJob)
	}
	body := runs(version)
	for _, want := range []string{
		"workflow_dispatch",
		"github.event.inputs.version",
		"GITHUB_REF_NAME",
		`^v[0-9]+\.[0-9]+\.[0-9]+$`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs.%s must reference %q", versionJob, want)
		}
	}

	for _, name := range []string{trayJobName, releaseJob} {
		target := requireJob(t, workflow, name)
		if !contains(target.Needs, versionJob) {
			t.Errorf("jobs.%s must need jobs.%s, got needs=%v", name, versionJob, target.Needs)
		}
		if got := target.Env["VERSION"]; !strings.Contains(got, "needs."+versionJob+".outputs.version") {
			t.Errorf("jobs.%s must take VERSION from the %s job, got %q", name, versionJob, got)
		}
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// TestReleaseWorkflowBuildsTheTrayOnMacOS pins the macOS half of the split: the tray job
// runs on a macOS runner, builds the settings bundle, packages both darwin architectures,
// verifies what it produced and hands it over as an artifact.
func TestReleaseWorkflowBuildsTheTrayOnMacOS(t *testing.T) {
	workflow := loadWorkflow(t)
	tray := requireJob(t, workflow, trayJobName)

	if !strings.HasPrefix(tray.RunsOn, "macos-") {
		t.Errorf("jobs.%s runs-on %q, want a macOS runner: the tray links Cocoa and WebKit through cgo", trayJobName, tray.RunsOn)
	}
	if tray.RunsOn == "macos-latest" {
		t.Errorf("jobs.%s must pin a macOS label; macos-latest changes the Xcode and SDK under the release", trayJobName)
	}

	body := runs(tray)
	for _, want := range []string{
		"cd web-tray && npm ci",
		"cd web-tray && npm run build",
		trayScript,
		"SHA256SUMS",
		// The icon layout that makes a disk image read as an installer is written by
		// Finder, so a runner without a window server session can only produce a plain
		// folder window. The packaging script records that per asset; the job has to read
		// the record, because a release that quietly lost its installer layout is a
		// downgrade nobody would notice until a user complains.
		"installerLayout",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs.%s must run %q", trayJobName, want)
		}
	}
	if strings.Contains(body, "build-release.sh") {
		t.Errorf("jobs.%s must not run the cross-platform release script", trayJobName)
	}

	node := stepUsing(tray, "actions/setup-node")
	if node == nil {
		t.Fatalf("jobs.%s must set up node for the settings bundle", trayJobName)
	}
	if got, _ := node.With["cache-dependency-path"].(string); got != "web-tray/package-lock.json" {
		t.Errorf("jobs.%s caches npm against %q, want web-tray/package-lock.json", trayJobName, got)
	}
	if got := stepUsing(tray, "actions/setup-go"); got == nil {
		t.Errorf("jobs.%s must set up go from go.mod", trayJobName)
	}

	upload := stepUsing(tray, "actions/upload-artifact")
	if upload == nil {
		t.Fatalf("jobs.%s must upload its artifacts for the release job", trayJobName)
	}
	if got, _ := upload.With["name"].(string); got != trayArtifact {
		t.Errorf("jobs.%s uploads artifact %q, want %q", trayJobName, got, trayArtifact)
	}
	path, _ := upload.With["path"].(string)
	if !strings.Contains(path, "macos-tray") || !strings.Contains(path, "dist/") {
		t.Errorf("jobs.%s uploads %q, want the tray output directory under dist/", trayJobName, path)
	}
	if got, _ := upload.With["if-no-files-found"].(string); got != "error" {
		t.Errorf("jobs.%s must fail instead of uploading nothing, got if-no-files-found=%q", trayJobName, got)
	}
}

// TestReleaseWorkflowPublishesEverythingFromOneJob is the other half: the Linux release job
// downloads the tray, merges it into the release directory and publishes one immutable
// release. It must not try to build the tray itself.
func TestReleaseWorkflowPublishesEverythingFromOneJob(t *testing.T) {
	workflow := loadWorkflow(t)
	release := requireJob(t, workflow, releaseJob)

	if strings.HasPrefix(release.RunsOn, "macos-") {
		t.Errorf("jobs.%s runs-on %q; the cross-platform matrix is built on Linux", releaseJob, release.RunsOn)
	}
	if !contains(release.Needs, trayJobName) {
		t.Errorf("jobs.%s must need jobs.%s, or it can publish before the tray exists", releaseJob, trayJobName)
	}

	download := stepUsing(release, "actions/download-artifact")
	if download == nil {
		t.Fatalf("jobs.%s must download the tray artifact", releaseJob)
	}
	if got, _ := download.With["name"].(string); got != trayArtifact {
		t.Errorf("jobs.%s downloads artifact %q, want %q", releaseJob, got, trayArtifact)
	}
	trayPath, _ := download.With["path"].(string)
	if trayPath == "" {
		t.Errorf("jobs.%s must download the tray into a known directory", releaseJob)
	}

	body := runs(release)
	for _, want := range []string{
		"cd web && npm ci",
		"cd web && npm run build",
		"./scripts/verify-web-embed.sh",
		"./scripts/build-release.sh",
		"TRAY_DIST_DIR=" + trayPath,
		"gh release create",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs.%s must run %q", releaseJob, want)
		}
	}
	if strings.Contains(body, "package-macos-tray.sh") {
		t.Errorf("jobs.%s must not package the tray: it runs on Linux, which cannot link WebKit", releaseJob)
	}
	// The release must stay immutable and complete: every asset in the directory, published
	// against the tagged commit, with generated notes.
	if !strings.Contains(body, "dist/") || !strings.Contains(body, "--generate-notes") {
		t.Errorf("jobs.%s must publish the whole dist directory with generated notes", releaseJob)
	}
}

// TestReleaseWorkflowKeepsWritePermissionOnlyForPublishing documents why the workflow holds
// contents: write at all. It is needed for `gh release create` and nothing else.
func TestReleaseWorkflowKeepsWritePermissionOnlyForPublishing(t *testing.T) {
	workflow := loadWorkflow(t)
	if got := workflow.Permissions["contents"]; got != "write" {
		t.Errorf("%s sets permissions.contents=%q, want write for gh release create", releaseWorkflow, got)
	}
	for name, target := range workflow.Jobs {
		if name == releaseJob {
			continue
		}
		if strings.Contains(runs(target), "gh ") {
			t.Errorf("jobs.%s calls gh but does not need release permission", name)
		}
	}
}

// The Windows tray half of the split: it cross-compiles from Linux, so it must not be waiting
// for a Mac, and it must not be folded into the release job either, because the release job
// publishes from a directory it never builds into.
const (
	windowsTrayJob       = "windows-tray"
	windowsTrayArtifact  = "windows-tray"
	windowsPackageScript = "./scripts/package-windows-tray.sh"
)

func TestReleaseWorkflowBuildsTheWindowsTrayOnLinux(t *testing.T) {
	workflow := loadWorkflow(t)
	tray := requireJob(t, workflow, windowsTrayJob)

	if !strings.HasPrefix(tray.RunsOn, "ubuntu-") {
		t.Errorf("jobs.%s runs-on %q, want a Linux runner: the windows shell is pure Go over WebView2 "+
			"and makensis is a cross compiler", windowsTrayJob, tray.RunsOn)
	}
	if !contains(tray.Needs, versionJob) {
		t.Errorf("jobs.%s must need jobs.%s, or it can name its assets after a different version", windowsTrayJob, versionJob)
	}
	if got := tray.Env["VERSION"]; !strings.Contains(got, "needs."+versionJob+".outputs.version") {
		t.Errorf("jobs.%s must take VERSION from the %s job, got %q", windowsTrayJob, versionJob, got)
	}

	body := runs(tray)
	for _, want := range []string{
		"apt-get install -y --no-install-recommends nsis",
		"cd web-tray && npm ci",
		"cd web-tray && npm test -- --run",
		"cd web-tray && npm run build",
		windowsPackageScript,
		"SHA256SUMS",
		"installerLayout",
		"-windows-amd64-setup.exe",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs.%s must run or assert %q", windowsTrayJob, want)
		}
	}
	if strings.Contains(body, "build-release.sh") || strings.Contains(body, "package-macos-tray.sh") {
		t.Errorf("jobs.%s must only package the windows tray", windowsTrayJob)
	}

	node := stepUsing(tray, "actions/setup-node")
	if node == nil {
		t.Fatalf("jobs.%s must set up node for the settings bundle", windowsTrayJob)
	}
	if got, _ := node.With["cache-dependency-path"].(string); got != "web-tray/package-lock.json" {
		t.Errorf("jobs.%s caches npm against %q, want web-tray/package-lock.json", windowsTrayJob, got)
	}
	upload := stepUsing(tray, "actions/upload-artifact")
	if upload == nil {
		t.Fatalf("jobs.%s must upload its artifacts for the release job", windowsTrayJob)
	}
	if got, _ := upload.With["name"].(string); got != windowsTrayArtifact {
		t.Errorf("jobs.%s uploads artifact %q, want %q", windowsTrayJob, got, windowsTrayArtifact)
	}
	path, _ := upload.With["path"].(string)
	if !strings.Contains(path, "windows-tray") || !strings.Contains(path, "dist/") {
		t.Errorf("jobs.%s uploads %q, want the windows tray output directory under dist/", windowsTrayJob, path)
	}
	if got, _ := upload.With["if-no-files-found"].(string); got != "error" {
		t.Errorf("jobs.%s must fail instead of uploading nothing, got if-no-files-found=%q", windowsTrayJob, got)
	}
}

func TestReleaseWorkflowPublishesBothTrayPlatforms(t *testing.T) {
	workflow := loadWorkflow(t)
	release := requireJob(t, workflow, releaseJob)

	for _, want := range []string{trayJobName, windowsTrayJob} {
		if !contains(release.Needs, want) {
			t.Errorf("jobs.%s must need jobs.%s before publishing", releaseJob, want)
		}
	}

	downloaded := map[string]string{}
	for _, entry := range release.Steps {
		if !strings.HasPrefix(entry.Uses, "actions/download-artifact") {
			continue
		}
		name, _ := entry.With["name"].(string)
		target, _ := entry.With["path"].(string)
		if name == "" || target == "" {
			t.Errorf("jobs.%s has a download-artifact step without both name and path", releaseJob)
			continue
		}
		downloaded[name] = target
	}
	for _, artifact := range []string{trayArtifact, windowsTrayArtifact} {
		if _, ok := downloaded[artifact]; !ok {
			t.Errorf("jobs.%s must download the %s artifact, got %v", releaseJob, artifact, downloaded)
		}
	}

	body := runs(release)
	for _, want := range []string{
		"TRAY_DIST_DIR=" + downloaded[trayArtifact],
		"WINDOWS_TRAY_DIST_DIR=" + downloaded[windowsTrayArtifact],
		// Each hand-off keeps its own manifest, so a published release can say which job
		// produced which asset and the two cannot overwrite each other.
		"manifest-tray.json",
		"manifest-windows-tray.json",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs.%s must reference %q", releaseJob, want)
		}
	}
	if strings.Contains(body, windowsPackageScript) {
		t.Errorf("jobs.%s must not package the windows tray; it merges what the tray job built", releaseJob)
	}
	// The asset census is the last line of defence against a partial release: six platform
	// archives, two disk images, two green windows archives and one windows installer.
	for _, want := range []string{"-name '*.dmg'", "-name '*.exe'", "-eq 8", "-eq 2", "-eq 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("jobs.%s must assert the published asset census with %q", releaseJob, want)
		}
	}
}

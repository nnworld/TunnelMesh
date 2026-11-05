package scripts

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestEveryBinaryEntrypointUsesCLIMain pins the structural rule that every
// TunnelMesh binary hands its process lifecycle to cli.Main.
//
// cli.Main is the only place that installs the SIGINT/SIGTERM handler and the
// only place that maps an error to an exit code. An entrypoint that calls
// cli.Execute and log.Fatal instead gets both halves wrong at once: `run` blocks
// under context.Background(), so a stop signal kills the process outright and the
// graceful drain in ServerRuntime.ServeListener never runs, and every failure
// collapses to exit 1, which is precisely the code the shipped systemd units are
// written to restart on. A source-level guard is how the rest of this package
// enforces structural rules (build tags, native sources): it costs one parse and
// fails the next edit that reintroduces the pattern, rather than failing in
// production during the stop it was supposed to make safe.
//
// Only untagged entrypoints are checked: see the loop below for why the tray is a
// deliberate exception rather than an oversight.
func TestEveryBinaryEntrypointUsesCLIMain(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join("..", "cmd", "*", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	if len(entries) < 3 {
		t.Fatalf("found %d entrypoints under cmd/, want the three shipped binaries", len(entries))
	}
	fset := token.NewFileSet()
	for _, path := range entries {
		// A tagged entrypoint owns a different lifecycle. The tray is the case in
		// point: it drives an AppKit/Win32 run loop on the main thread and installs
		// its own signal handling around it, so cli.Main would fight that design.
		if buildConstraintOf(t, path) != "" {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("%s does not parse: %v", path, err)
		}
		mainFunc := func() *ast.FuncDecl {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Recv == nil && fn.Name.Name == "main" {
					return fn
				}
			}
			return nil
		}()
		if mainFunc == nil {
			t.Fatalf("%s has no main function", path)
		}
		usesMain, fatalCalls := false, []string{}
		ast.Inspect(mainFunc, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pkg.Name == "cli" && sel.Sel.Name == "Main":
				usesMain = true
			case pkg.Name == "log":
				fatalCalls = append(fatalCalls, sel.Sel.Name)
			}
			return true
		})
		if !usesMain {
			t.Errorf("%s does not call cli.Main, so it cannot handle SIGTERM or map exit codes; use os.Exit(cli.Main(...))", path)
		}
		if len(fatalCalls) > 0 {
			t.Errorf("%s calls log.%s inside main, which collapses every exit code to 1", path, strings.Join(fatalCalls, ", log."))
		}
	}
}

// TestLongLivedUnitsAlwaysRestart pins the other half of the same contract: the
// units must restart a process that exits on its own, and must not restart one
// whose cause a restart cannot fix.
//
// The exit codes are not ours to change (internal/workerproto pins them as a
// contract with these files), and Restart=on-failure is not enough on its own:
// an entrypoint returning 0 because a listener closed unexpectedly is a failure
// from the operator's point of view and looks like a clean stop to systemd. This
// is the drift that left the Server unit on Restart=on-failure while client, agent
// and the shared launchd template all said "always".
func TestLongLivedUnitsAlwaysRestart(t *testing.T) {
	dirs := []string{
		filepath.Join("..", "deploy", "systemd"),
		filepath.Join("..", "deploy", "systemd-user"),
	}
	checked := 0
	for _, dir := range dirs {
		units, err := filepath.Glob(filepath.Join(dir, "tunnelmesh-*.service"))
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(units)
		if len(units) == 0 {
			t.Fatalf("%s contains no service units", dir)
		}
		for _, path := range units {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			body := string(data)
			checked++
			for _, want := range []string{"Restart=always", "RestartPreventExitStatus=78 77"} {
				if !strings.Contains(body, want) {
					t.Errorf("%s is missing %q: a long-lived unit must restart on its own exit and must not restart on a code that cannot heal", path, want)
				}
			}
			// The Server drains a bounded HTTP shutdown plus the optional VPN data
			// plane, so its stop budget has to be visible in the unit rather than
			// inherited from a distribution default.
			if strings.Contains(filepath.Base(path), "server") && !strings.Contains(body, "TimeoutStopSec=") {
				t.Errorf("%s does not bound its stop with TimeoutStopSec=", path)
			}
			if !strings.Contains(body, "ExecStart=") {
				t.Errorf("%s has no ExecStart", path)
			}
		}
	}
	if checked < 6 {
		t.Fatalf("checked %d units, want all six shipped templates", checked)
	}
}

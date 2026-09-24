// Package mysql56 guards the local reproduction environment for the MySQL floor
// version that CI verifies.
package mysql56

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	composePath  = "../../docker-compose.cluster.yml"
	ciPath       = "../../.github/workflows/ci.yml"
	serverConfig = "utf8mb4.cnf"
	mountTarget  = "/etc/mysql/conf.d/tunnelmesh.cnf"
	testDatabase = "tunnelmesh_test"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]any            `yaml:"volumes"`
}

type composeService struct {
	Image       string            `yaml:"image"`
	Profiles    []string          `yaml:"profiles"`
	Environment map[string]string `yaml:"environment"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Healthcheck struct {
		Test []string `yaml:"test"`
	} `yaml:"healthcheck"`
}

func loadCompose(t *testing.T) composeFile {
	t.Helper()
	data, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed composeFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", composePath, err)
	}
	return parsed
}

// TestMySQL56ProfileMatchesTheCIFloorVersion keeps the local environment and the
// CI gate on one server version. The whole point of the gate is that MySQL SQL
// is verified against the lowest supported version, so a job that moves to 8.x
// while this profile stays on 5.6 (or the reverse) silently changes what
// "verified" means: 8.x accepts syntax 5.6 rejects.
func TestMySQL56ProfileMatchesTheCIFloorVersion(t *testing.T) {
	service, ok := loadCompose(t).Services["mysql56"]
	if !ok {
		t.Fatalf("%s has no services.mysql56", composePath)
	}
	data, err := os.ReadFile(ciPath)
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`(?m)^\s+image:\s*(\S+)\s*$`).FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatalf("%s declares no service container image", ciPath)
	}
	for _, match := range matches {
		if match[1] != service.Image {
			t.Fatalf("CI image %q != %s image %q: the MySQL floor version must have one source",
				match[1], composePath, service.Image)
		}
	}
	if !strings.HasPrefix(service.Image, "mysql:") {
		t.Fatalf("image = %q, want a MySQL server image", service.Image)
	}
	if service.Environment["MYSQL_DATABASE"] != testDatabase {
		t.Fatalf("MYSQL_DATABASE = %q, want %s: contract tests need a database they may create the schema in",
			service.Environment["MYSQL_DATABASE"], testDatabase)
	}
}

// TestMySQL56ProfileIsOptInAndLoopbackOnly guards the two properties that make
// this service safe to keep in the cluster file: it must not start with the
// cluster stack, and it must not become reachable from a network. MySQL 5.6 has
// no TLS here, so publishing 3307 would expose an unencrypted database.
func TestMySQL56ProfileIsOptInAndLoopbackOnly(t *testing.T) {
	parsed := loadCompose(t)
	service, ok := parsed.Services["mysql56"]
	if !ok {
		t.Fatalf("%s has no services.mysql56", composePath)
	}
	if len(service.Profiles) != 1 || service.Profiles[0] != "mysql56" {
		t.Fatalf("profiles = %v, want [mysql56] so `up` for the cluster never starts it", service.Profiles)
	}
	if len(service.Ports) != 1 || !strings.HasPrefix(service.Ports[0], "127.0.0.1:") {
		t.Fatalf("ports = %v, want one loopback-published mapping", service.Ports)
	}
	if len(service.Healthcheck.Test) == 0 {
		t.Fatal("mysql56 needs a healthcheck: the DSN is useless before mysqld accepts connections")
	}
	declared := false
	for name := range parsed.Volumes {
		if name == "tunnelmesh-mysql56-test-data" {
			declared = true
		}
	}
	if !declared {
		t.Fatalf("volumes must declare the mysql56 test data volume so test runs cannot reuse the cluster dataset")
	}
	// The cluster servers must not depend on this service: a profile-gated
	// container that others wait for would make `up` fail for everyone.
	for name, spec := range parsed.Services {
		if name == "mysql56" {
			continue
		}
		if strings.Contains(strings.Join(spec.Ports, ","), ":3306") && len(spec.Profiles) == 0 {
			t.Fatalf("%s publishes 3306 without a profile, which would collide with a local server", name)
		}
	}
}

// TestMySQL56ServerConfigIsTheProductionCharset is the reason this profile
// exists in addition to the CI job: the CI service container can only run the
// image defaults (latin1 on 5.6), so it cannot show that migrations/ddl.sql
// fits InnoDB's 767-byte key limit under production's utf8mb4.
func TestMySQL56ServerConfigIsTheProductionCharset(t *testing.T) {
	data, err := os.ReadFile(serverConfig)
	if err != nil {
		t.Fatal(err)
	}
	config := string(data)
	for _, want := range []string{"[mysqld]", "character-set-server = utf8mb4", "collation-server = utf8mb4_general_ci"} {
		if !strings.Contains(config, want) {
			t.Fatalf("%s must set %q to mirror production", serverConfig, want)
		}
	}
	// Only options count, not the prose: widening the prefix limit would make the
	// key-length claim untestable, so this file must never set it.
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, forbidden := range []string{"innodb_large_prefix", "innodb_file_format", "sql_mode"} {
			if strings.HasPrefix(line, forbidden) {
				t.Fatalf("%s must not set %q: the 767-byte default limit and the production sql_mode are what this profile is meant to reproduce",
					serverConfig, forbidden)
			}
		}
	}
	mounted := false
	for _, volume := range loadCompose(t).Services["mysql56"].Volumes {
		if strings.HasSuffix(volume, ":"+mountTarget+":ro") && strings.Contains(volume, serverConfig) {
			mounted = true
		}
	}
	if !mounted {
		t.Fatalf("mysql56 must mount %s into %s read-only", serverConfig, mountTarget)
	}
}

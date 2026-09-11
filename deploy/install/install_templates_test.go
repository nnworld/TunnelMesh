package install

import (
	"bytes"
	"os"
	"testing"
)

func TestClientServiceTemplatesExistAndContainRunCommand(t *testing.T) {
	templates := []string{
		"../systemd/tunnelmesh-client.service",
		"../macos/tunnelmesh-client.plist",
		"../windows/tunnelmesh-client-service.xml",
	}
	for _, name := range templates {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, want := range []string{"tunnelmesh-client", "--config", "run"} {
			if !bytes.Contains(data, []byte(want)) {
				t.Fatalf("%s does not contain %q", name, want)
			}
		}
	}
}

func TestInstallScriptsAdvertiseClientRole(t *testing.T) {
	scripts := []string{
		"linux-install.sh",
		"macos-install.sh",
		"windows-install.ps1",
	}
	for _, name := range scripts {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Contains(data, []byte("client")) {
			t.Fatalf("%s does not support the client role", name)
		}
	}
}

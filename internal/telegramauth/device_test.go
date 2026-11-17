package telegramauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceForSessionIsStableAppleProfile(t *testing.T) {
	first := DeviceForSession("/tmp/accounts/1001.session")
	second := DeviceForSession("/tmp/accounts/1001.session")
	if first != second {
		t.Fatalf("device profile changed for the same session: %#v != %#v", first, second)
	}
	if !strings.HasPrefix(first.DeviceModel, "iPhone") {
		t.Fatalf("DeviceModel = %q, want an iPhone model", first.DeviceModel)
	}
	if first.SystemVersion == "Go" || first.LangPack == "tdesktop" {
		t.Fatalf("device profile exposes implementation details: %#v", first)
	}
	if strings.Contains(first.DeviceModel, "TeleNexus") || strings.Contains(first.AppVersion, "TeleNexus") {
		t.Fatal("device profile must not expose the project name")
	}
}

func TestBridgeDeviceProfileKeepsSessionStable(t *testing.T) {
	source := filepath.Join(t.TempDir(), "pending", "login.session")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatalf("create source directory: %v", err)
	}
	if err := os.WriteFile(source, []byte("session"), 0o600); err != nil {
		t.Fatalf("write source session: %v", err)
	}
	destination := filepath.Join(t.TempDir(), "accounts", "42.session")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatalf("create destination directory: %v", err)
	}
	if err := bridgeDeviceProfile(source, destination); err != nil {
		t.Fatalf("bridgeDeviceProfile() error = %v", err)
	}
	if DeviceForSession(source) != DeviceForSession(destination) {
		t.Fatal("device profile changed between pending and authorized session")
	}
	info, err := os.Stat(deviceSidecarPath(destination))
	if err != nil {
		t.Fatalf("stat device sidecar: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("device sidecar mode = %o, want 600", info.Mode().Perm())
	}
}

func TestDeviceForSessionVariesAcrossAccounts(t *testing.T) {
	seen := make(map[string]struct{})
	for _, id := range []string{"1001", "1002", "1003", "1004", "1005"} {
		device := DeviceForSession("/tmp/accounts/" + id + ".session")
		seen[device.DeviceModel+"|"+device.SystemVersion+"|"+device.AppVersion] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("device profiles did not vary across sessions: %#v", seen)
	}
}

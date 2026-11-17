package telegramauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/gotd/td/telegram"
)

type deviceProfile struct {
	Model         string `json:"model"`
	SystemVersion string `json:"system_version"`
	AppVersion    string `json:"app_version"`
}

var appleDeviceProfiles = []deviceProfile{
	{Model: "iPhone 16 Pro", SystemVersion: "iOS 18.6.2", AppVersion: "12.4.1"},
	{Model: "iPhone 16 Pro Max", SystemVersion: "iOS 18.6.2", AppVersion: "12.4.1"},
	{Model: "iPhone 16", SystemVersion: "iOS 18.6.2", AppVersion: "12.3.2"},
	{Model: "iPhone 16 Plus", SystemVersion: "iOS 18.6.2", AppVersion: "12.3.2"},
	{Model: "iPhone 15 Pro", SystemVersion: "iOS 18.5", AppVersion: "12.2.3"},
	{Model: "iPhone 15 Pro Max", SystemVersion: "iOS 18.5", AppVersion: "12.2.3"},
	{Model: "iPhone 15", SystemVersion: "iOS 18.5", AppVersion: "12.2.3"},
	{Model: "iPhone 15 Plus", SystemVersion: "iOS 18.5", AppVersion: "12.2.3"},
	{Model: "iPhone 14 Pro", SystemVersion: "iOS 18.4.1", AppVersion: "12.1.2"},
	{Model: "iPhone 14", SystemVersion: "iOS 18.4.1", AppVersion: "12.0.1"},
	{Model: "iPhone 14 Plus", SystemVersion: "iOS 18.4.1", AppVersion: "12.0.1"},
	{Model: "iPhone 13 Pro", SystemVersion: "iOS 18.3.2", AppVersion: "11.12.4"},
	{Model: "iPhone 13", SystemVersion: "iOS 18.3.2", AppVersion: "11.11.1"},
	{Model: "iPhone 12 Pro Max", SystemVersion: "iOS 18.2.1", AppVersion: "11.9.3"},
	{Model: "iPhone SE (3rd generation)", SystemVersion: "iOS 18.3.1", AppVersion: "11.10.2"},
}

// PrepareDeviceSession assigns and persists a random Apple device profile
// before a session first connects to Telegram.
func PrepareDeviceSession(sessionPath string) error {
	sidecarPath := deviceSidecarPath(sessionPath)
	if data, err := os.ReadFile(sidecarPath); err == nil {
		var profile deviceProfile
		if json.Unmarshal(data, &profile) == nil && validDeviceProfile(profile) {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read Telegram device profile: %w", err)
	}

	var randomBytes [4]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return fmt.Errorf("generate Telegram device profile: %w", err)
	}
	profile := appleDeviceProfiles[binary.BigEndian.Uint32(randomBytes[:])%uint32(len(appleDeviceProfiles))]
	if err := writeDeviceProfile(sessionPath, profile); err != nil {
		return err
	}
	return nil
}

// DeviceForSession returns a stable Apple device profile for one Telegram
// session. Authorized sessions persist the pending login profile in a sidecar
// file so later chat reads use the same device.
func DeviceForSession(sessionPath string) telegram.DeviceConfig {
	profile := deviceProfileForSession(sessionPath)
	return telegram.DeviceConfig{
		DeviceModel:    profile.Model,
		SystemVersion:  profile.SystemVersion,
		AppVersion:     profile.AppVersion,
		SystemLangCode: "zh",
		LangPack:       "ios",
		LangCode:       "zh",
	}
}

func deviceProfileForSession(sessionPath string) deviceProfile {
	if data, err := os.ReadFile(deviceSidecarPath(sessionPath)); err == nil {
		var profile deviceProfile
		if json.Unmarshal(data, &profile) == nil && validDeviceProfile(profile) {
			return profile
		}
	}
	sum := sha256.Sum256([]byte(sessionPath))
	return appleDeviceProfiles[binary.BigEndian.Uint32(sum[:4])%uint32(len(appleDeviceProfiles))]
}

func bridgeDeviceProfile(source, destination string) error {
	profile := deviceProfileForSession(source)
	return writeDeviceProfile(destination, profile)
}

func writeDeviceProfile(sessionPath string, profile deviceProfile) error {
	data, err := json.Marshal(profile)
	if err != nil {
		return fmt.Errorf("encode Telegram device profile: %w", err)
	}
	path := deviceSidecarPath(sessionPath)
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist Telegram device profile: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure Telegram device profile: %w", err)
	}
	return nil
}

func deviceSidecarPath(sessionPath string) string {
	return sessionPath + ".device"
}

func validDeviceProfile(profile deviceProfile) bool {
	for _, candidate := range appleDeviceProfiles {
		if profile == candidate {
			return true
		}
	}
	return false
}

func removeSessionFiles(sessionPath string) {
	for _, path := range []string{sessionPath, deviceSidecarPath(sessionPath)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return
		}
	}
}

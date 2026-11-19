package telegramauth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakeDriver struct {
	passwordRequired bool
	profile          Profile
}

func (d *fakeDriver) SendCode(_ context.Context, sessionPath, _ string) (SentCode, error) {
	if err := os.WriteFile(sessionPath, []byte("pending-session"), 0o600); err != nil {
		return SentCode{}, err
	}
	return SentCode{
		CodeHash:   "phone-code-hash",
		CodeType:   "短信验证码",
		CodeLength: 5,
	}, nil
}

func (d *fakeDriver) SignIn(
	_ context.Context,
	_ string,
	_ string,
	code string,
	codeHash string,
) (*Profile, bool, error) {
	if code != "12345" || codeHash != "phone-code-hash" {
		return nil, false, ErrInvalidCode
	}
	if d.passwordRequired {
		return nil, true, nil
	}
	return &d.profile, false, nil
}

func (d *fakeDriver) Password(_ context.Context, _ string, password string) (*Profile, error) {
	if password != "correct-password" {
		return nil, ErrInvalidPassword
	}
	return &d.profile, nil
}

func TestServiceCompletesCodeLoginAndPersistsSession(t *testing.T) {
	sessionDir := t.TempDir()
	driver := &fakeDriver{profile: Profile{
		ID:          42,
		DisplayName: "Test Account",
	}}
	service, err := NewService(driver, ServiceConfig{SessionDir: sessionDir})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	challenge, err := service.SendCode(context.Background(), "+15550001001", 0)
	if err != nil {
		t.Fatalf("SendCode() error = %v", err)
	}
	if challenge.CodeLength != 5 {
		t.Fatalf("SendCode() challenge = %+v, want code length 5", challenge)
	}

	result, err := service.SubmitCode(context.Background(), challenge.LoginID, "12345")
	if err != nil {
		t.Fatalf("SubmitCode() error = %v", err)
	}
	if result.NextStep != StepComplete || result.Profile == nil || result.Profile.ID != 42 {
		t.Fatalf("SubmitCode() result = %+v, want completed account", result)
	}
	if result.Profile.PhoneNumber != "+15550001001" {
		t.Fatalf("SubmitCode() phone = %q, want pending login phone", result.Profile.PhoneNumber)
	}
	sessionPath := filepath.Join(sessionDir, "accounts", "42.session")
	info, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatalf("authorized session was not stored: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %o, want 600", info.Mode().Perm())
	}
}

func TestServiceSupportsTwoStepPassword(t *testing.T) {
	driver := &fakeDriver{
		passwordRequired: true,
		profile: Profile{
			ID:          43,
			PhoneNumber: "+15550001002",
			DisplayName: "Protected Account",
		},
	}
	service, err := NewService(driver, ServiceConfig{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	challenge, err := service.SendCode(context.Background(), "+15550001002", 0)
	if err != nil {
		t.Fatalf("SendCode() error = %v", err)
	}
	result, err := service.SubmitCode(context.Background(), challenge.LoginID, "12345")
	if err != nil {
		t.Fatalf("SubmitCode() error = %v", err)
	}
	if result.NextStep != StepPassword {
		t.Fatalf("SubmitCode() next step = %q, want password", result.NextStep)
	}
	if _, err := service.SubmitPassword(context.Background(), challenge.LoginID, "wrong"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("SubmitPassword() error = %v, want ErrInvalidPassword", err)
	}
	result, err = service.SubmitPassword(context.Background(), challenge.LoginID, "correct-password")
	if err != nil {
		t.Fatalf("SubmitPassword() error = %v", err)
	}
	if result.NextStep != StepComplete {
		t.Fatalf("SubmitPassword() next step = %q, want complete", result.NextStep)
	}
}

func TestServiceRejectsInvalidPhone(t *testing.T) {
	service, err := NewService(&fakeDriver{}, ServiceConfig{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	_, err = service.SendCode(context.Background(), "not-a-phone", 0)
	if !errors.Is(err, ErrInvalidPhone) {
		t.Fatalf("SendCode() error = %v, want ErrInvalidPhone", err)
	}
}

func TestServiceRejectsMismatchedReloginAccount(t *testing.T) {
	sessionDir := t.TempDir()
	service, err := NewService(
		&fakeDriver{profile: Profile{ID: 42, DisplayName: "Wrong Account"}},
		ServiceConfig{SessionDir: sessionDir},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	challenge, err := service.SendCode(context.Background(), "+15550001003", 99)
	if err != nil {
		t.Fatalf("SendCode() error = %v", err)
	}
	_, err = service.SubmitCode(context.Background(), challenge.LoginID, "12345")
	if !errors.Is(err, ErrAccountMismatch) {
		t.Fatalf("SubmitCode() error = %v, want ErrAccountMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, "accounts", "42.session")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched session was persisted: %v", err)
	}
}

func TestNewServiceCleansStalePendingSessions(t *testing.T) {
	sessionDir := t.TempDir()
	pendingDir := filepath.Join(sessionDir, "pending")
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		t.Fatalf("create pending directory: %v", err)
	}
	stalePath := filepath.Join(pendingDir, "stale.session")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale session: %v", err)
	}

	if _, err := NewService(&fakeDriver{}, ServiceConfig{SessionDir: sessionDir}); err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if _, err := os.Stat(stalePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale session still exists: %v", err)
	}
}

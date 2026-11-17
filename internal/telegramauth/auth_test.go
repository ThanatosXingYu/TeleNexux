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

type fakeFetcher struct {
	code string
}

func (f fakeFetcher) Validate(string) error {
	return nil
}

func (f fakeFetcher) Fetch(context.Context, string, int) (string, error) {
	return f.code, nil
}

func TestServiceCompletesCodeLoginAndPersistsSession(t *testing.T) {
	sessionDir := t.TempDir()
	driver := &fakeDriver{profile: Profile{
		ID:          42,
		DisplayName: "Test Account",
	}}
	service, err := NewService(driver, nil, ServiceConfig{SessionDir: sessionDir})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	challenge, err := service.SendCode(context.Background(), "+15550001001", "")
	if err != nil {
		t.Fatalf("SendCode() error = %v", err)
	}
	if challenge.CodeLength != 5 || challenge.CanFetchCode {
		t.Fatalf("SendCode() challenge = %+v, want code length 5 without auto fetch", challenge)
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
	service, err := NewService(driver, nil, ServiceConfig{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	challenge, err := service.SendCode(context.Background(), "+15550001002", "")
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

func TestServiceFetchesCodeSource(t *testing.T) {
	driver := &fakeDriver{profile: Profile{ID: 44, PhoneNumber: "+15550001003"}}
	service, err := NewService(driver, fakeFetcher{code: "12345"}, ServiceConfig{
		SessionDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	challenge, err := service.SendCode(
		context.Background(),
		"+15550001003",
		"https://api.didiapi.uk/getcode?id=test",
	)
	if err != nil {
		t.Fatalf("SendCode() error = %v", err)
	}
	if !challenge.CanFetchCode {
		t.Fatal("SendCode() should mark code fetching available")
	}
	result, err := service.FetchCode(context.Background(), challenge.LoginID)
	if err != nil {
		t.Fatalf("FetchCode() error = %v", err)
	}
	if result.NextStep != StepComplete {
		t.Fatalf("FetchCode() next step = %q, want complete", result.NextStep)
	}
}

func TestServiceRejectsInvalidPhone(t *testing.T) {
	service, err := NewService(&fakeDriver{}, nil, ServiceConfig{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	_, err = service.SendCode(context.Background(), "not-a-phone", "")
	if !errors.Is(err, ErrInvalidPhone) {
		t.Fatalf("SendCode() error = %v, want ErrInvalidPhone", err)
	}
}

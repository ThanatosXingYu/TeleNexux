package telegramchat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsInvalidSessionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unregistered", err: errors.New("rpc error 401: AUTH_KEY_UNREGISTERED"), want: true},
		{name: "invalid", err: errors.New("AUTH_KEY_INVALID"), want: true},
		{name: "revoked", err: errors.New("SESSION_REVOKED"), want: true},
		{name: "deactivated", err: errors.New("USER_DEACTIVATED"), want: true},
		{name: "network error", err: errors.New("connection reset"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isInvalidSessionError(test.err); got != test.want {
				t.Fatalf("isInvalidSessionError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestNormalizeSearchQueryAcceptsUsernamePrefix(t *testing.T) {
	for _, input := range []string{"@example_user", "  @example_user  ", "example_user"} {
		if got := normalizeSearchQuery(input); got != "example_user" {
			t.Errorf("normalizeSearchQuery(%q) = %q, want example_user", input, got)
		}
	}
}

func TestFilterSearchChatsMatchesTitlesAndUsernames(t *testing.T) {
	chats := []Chat{
		{ID: "channel:1", Title: "Project Updates", Username: "project_news"},
		{ID: "chat:2", Title: "Support Group"},
		{ID: "user:3", Title: "Alice", Username: "example_user"},
	}

	got := filterSearchChats(chats, "@EXAMPLE_USER", 2)
	if len(got) != 1 || got[0].ID != "user:3" || got[0].Preview != "最近会话" {
		t.Fatalf("filterSearchChats() = %+v, want matching user chat", got)
	}
	if got := filterSearchChats(chats, "SUPPORT", 2); len(got) != 1 || got[0].ID != "chat:2" {
		t.Fatalf("filterSearchChats() title result = %+v, want support chat", got)
	}
}

func TestNormalizeProfileInputPreservesNamesAndUsernameActions(t *testing.T) {
	username := "@valid_user"
	firstName, lastName, gotUsername, _, err := normalizeProfileInput(UpdateProfileInput{
		FirstName: "  Alice ",
		LastName:  "Smith  Jones ",
		Username:  &username,
	})
	if err != nil {
		t.Fatalf("normalizeProfileInput() error = %v", err)
	}
	if firstName != "Alice" || lastName != "Smith Jones" {
		t.Fatalf("names = %q %q, want preserved first and last names", firstName, lastName)
	}
	if gotUsername == nil || *gotUsername != "valid_user" {
		t.Fatalf("username = %v, want valid_user", gotUsername)
	}

	emptyUsername := ""
	_, _, gotUsername, _, err = normalizeProfileInput(UpdateProfileInput{
		FirstName: "Alice",
		Username:  &emptyUsername,
	})
	if err != nil {
		t.Fatalf("normalizeProfileInput(clear) error = %v", err)
	}
	if gotUsername == nil || *gotUsername != "" {
		t.Fatalf("username = %v, want explicit empty value", gotUsername)
	}
}

func TestDeleteSessionRemovesOnlyAccountFiles(t *testing.T) {
	root := t.TempDir()
	accountDirectory := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accountDirectory, 0o700); err != nil {
		t.Fatalf("create account directory: %v", err)
	}
	sessionPath := filepath.Join(accountDirectory, "42.session")
	sidecarPath := sessionPath + ".device"
	for _, path := range []string{sessionPath, sidecarPath} {
		if err := os.WriteFile(path, []byte("local session"), 0o600); err != nil {
			t.Fatalf("write session fixture: %v", err)
		}
	}
	service := &Service{sessionDirectory: root}
	if err := service.DeleteSession(42); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	for _, path := range []string{sessionPath, sidecarPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("session file %q still exists", path)
		}
	}
}

func TestDeleteSessionRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	accountDirectory := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accountDirectory, 0o700); err != nil {
		t.Fatalf("create account directory: %v", err)
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write target fixture: %v", err)
	}
	sessionPath := filepath.Join(accountDirectory, "42.session")
	if err := os.Symlink(target, sessionPath); err != nil {
		t.Fatalf("create symlink fixture: %v", err)
	}
	service := &Service{sessionDirectory: root}
	if err := service.DeleteSession(42); err == nil {
		t.Fatal("DeleteSession() must reject a symlink")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target file was affected: %v", err)
	}
}

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerPersistsHashedCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	manager, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if manager.Configured() {
		t.Fatal("new manager should require administrator setup")
	}
	if err := manager.Setup("Admin.User", "strong-password-123"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %o, want 600", info.Mode().Perm())
	}
	reloaded, err := NewManager(path)
	if err != nil {
		t.Fatalf("reload manager error = %v", err)
	}
	if err := reloaded.Login("admin.user", "strong-password-123"); err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if err := reloaded.Login("admin.user", "wrong-password"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("Login() wrong password error = %v, want ErrInvalidLogin", err)
	}
	if err := reloaded.Setup("other", "another-password-123"); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("Setup() second call error = %v, want ErrAlreadyConfigured", err)
	}
}

func TestManagerCreatesAndRevokesSessions(t *testing.T) {
	manager, err := NewTestManager("admin", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	token, session, err := manager.CreateSession()
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if token == "" || session.CSRF == "" || token == session.CSRF {
		t.Fatalf("CreateSession() returned unsafe tokens: token=%q csrf=%q", token, session.CSRF)
	}
	if session.Actor != "admin" || session.Role != RoleAdministrator {
		t.Fatalf("CreateSession() principal = %q/%q", session.Actor, session.Role)
	}
	got, err := manager.Lookup(token)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if got.CSRF != session.CSRF {
		t.Fatalf("Lookup() CSRF = %q, want %q", got.CSRF, session.CSRF)
	}
	if got.Actor != session.Actor || got.Role != session.Role {
		t.Fatalf("Lookup() principal = %q/%q", got.Actor, got.Role)
	}
	manager.Delete(token)
	if _, err := manager.Lookup(token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("Lookup() after Delete() error = %v, want ErrInvalidSession", err)
	}
}

func TestPrincipalContextAndRolePolicy(t *testing.T) {
	principal := Principal{Actor: "admin", Role: RoleAdministrator}
	ctx := WithPrincipal(context.Background(), principal)
	got, ok := PrincipalFromContext(ctx)
	if !ok || got != principal {
		t.Fatalf("PrincipalFromContext() = %+v, %v", got, ok)
	}
	if !CanAccess(RoleAdministrator, PermissionAccess) {
		t.Fatal("administrator access was denied")
	}
	if CanAccess("viewer", PermissionAccess) ||
		CanAccess(RoleAdministrator, "unknown") {
		t.Fatal("unknown role or permission was allowed")
	}
}

func TestManagerRejectsWeakSetup(t *testing.T) {
	manager, err := NewTestManager("admin", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	fresh, err := NewManager(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := fresh.Setup("x", "short"); err == nil {
		t.Fatal("Setup() accepted invalid credentials")
	}
	if !manager.Configured() {
		t.Fatal("test manager should be configured")
	}
}

package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
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
	if !CanAccess(RoleAdministrator, PermissionSettingsWrite) {
		t.Fatal("administrator access was denied")
	}
	if CanAccess(RoleViewer, PermissionSettingsWrite) ||
		CanAccess(RoleViewer, PermissionTelegramAuth) ||
		CanAccess("unknown", PermissionSession) ||
		CanAccess(RoleAdministrator, "unknown") {
		t.Fatal("unknown role or permission was allowed")
	}
}

func TestRolePermissionMatrix(t *testing.T) {
	tests := []struct {
		name       string
		role       string
		permission string
		want       bool
	}{
		{"administrator audit", RoleAdministrator, PermissionAuditRead, true},
		{"operator run monitor", RoleOperator, PermissionMonitorOperate, true},
		{"operator change settings", RoleOperator, PermissionSettingsWrite, false},
		{"operator read audit", RoleOperator, PermissionAuditRead, false},
		{"operator modify retention", RoleOperator, PermissionRetentionWrite, false},
		{"viewer read chat", RoleViewer, PermissionChatRead, true},
		{"viewer operate chat", RoleViewer, PermissionChatOperate, false},
		{"viewer refresh account", RoleViewer, PermissionAccountWrite, false},
		{"viewer auth telegram", RoleViewer, PermissionTelegramAuth, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := CanAccess(test.role, test.permission); got != test.want {
				t.Fatalf(
					"CanAccess(%q, %q) = %v, want %v",
					test.role,
					test.permission,
					got,
					test.want,
				)
			}
		})
	}
}

func TestPermissionsReturnsStableRoleCapabilities(t *testing.T) {
	administrator := Permissions(RoleAdministrator)
	if !slices.IsSorted(administrator) {
		t.Fatalf("administrator permissions are not sorted: %v", administrator)
	}
	if !slices.Contains(administrator, PermissionAccountWrite) ||
		!slices.Contains(administrator, PermissionSettingsWrite) {
		t.Fatalf("administrator permissions are incomplete: %v", administrator)
	}

	viewer := Permissions(RoleViewer)
	if !slices.IsSorted(viewer) {
		t.Fatalf("viewer permissions are not sorted: %v", viewer)
	}
	if !slices.Contains(viewer, PermissionChatRead) ||
		slices.Contains(viewer, PermissionChatOperate) ||
		slices.Contains(viewer, PermissionAccountWrite) {
		t.Fatalf("viewer permissions do not match read-only policy: %v", viewer)
	}
	if got := Permissions("unsupported"); len(got) != 0 {
		t.Fatalf("unsupported role permissions = %v, want none", got)
	}
}

func TestManagerCreatesScopedSession(t *testing.T) {
	manager, err := NewTestManager("operator", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	token, created, err := manager.CreateRoleSession(RoleViewer)
	if err != nil {
		t.Fatalf("CreateRoleSession() error = %v", err)
	}
	if created.Role != RoleViewer {
		t.Fatalf("session role = %q, want %q", created.Role, RoleViewer)
	}
	loaded, err := manager.Lookup(token)
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if loaded.Role != RoleViewer {
		t.Fatalf("lookup role = %q, want %q", loaded.Role, RoleViewer)
	}
	if _, _, err := manager.CreateRoleSession("owner"); err == nil {
		t.Fatal("CreateRoleSession() accepted an unsupported role")
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

package auth

import (
	"context"
	"encoding/json"
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
	if err := fresh.Setup("admin", "1234567"); err == nil {
		t.Fatal("Setup() accepted a password shorter than 8 characters")
	}
	if err := fresh.Setup("admin", "12345678"); err != nil {
		t.Fatalf("Setup() rejected an 8-character password: %v", err)
	}
	userManager, err := NewManager(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatalf("NewManager(users) error = %v", err)
	}
	if err := userManager.Setup("owner", "owner-pass"); err != nil {
		t.Fatalf("Setup(owner) error = %v", err)
	}
	if _, err := userManager.CreateUser("operator", "1234567", RoleOperator); err == nil {
		t.Fatal("CreateUser() accepted a password shorter than 8 characters")
	}
	if _, err := userManager.CreateUser("operator", "12345678", RoleOperator); err != nil {
		t.Fatalf("CreateUser() rejected an 8-character password: %v", err)
	}
	shortPassword := "1234567"
	if _, err := userManager.UpdateUser("operator", UserUpdate{Password: &shortPassword}); err == nil {
		t.Fatal("UpdateUser() accepted a password shorter than 8 characters")
	}
	validPassword := "87654321"
	if _, err := userManager.UpdateUser("operator", UserUpdate{Password: &validPassword}); err != nil {
		t.Fatalf("UpdateUser() rejected an 8-character password: %v", err)
	}
	if !manager.Configured() {
		t.Fatal("test manager should be configured")
	}
}

func TestManagerLoadsLegacyAdministratorCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	data, err := json.Marshal(credentials{
		Username:     "legacy.admin",
		PasswordHash: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
	})
	if err != nil {
		t.Fatalf("marshal legacy credentials: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write legacy credentials: %v", err)
	}
	manager, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if !manager.Configured() {
		t.Fatal("legacy administrator should be configured")
	}
	users := manager.ListUsers()
	if len(users) != 1 || users[0].Role != RoleAdministrator || users[0].Username != "legacy.admin" {
		t.Fatalf("legacy users = %+v", users)
	}
}

func TestManagerCreatesViewerAndRevokesChangedSessions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	manager, err := NewManager(path)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.Setup("owner", "strong-password-123"); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if _, err := manager.CreateUser("analyst", "strong-password-456", RoleViewer); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	actor, err := manager.Authenticate("analyst", "strong-password-456")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if actor.Actor != "analyst" || actor.Role != RoleViewer {
		t.Fatalf("Authenticate() principal = %+v", actor)
	}
	token, _, err := manager.CreateUserSession(actor.Actor, actor.Role)
	if err != nil {
		t.Fatalf("CreateUserSession() error = %v", err)
	}
	disabled := true
	if _, err := manager.UpdateUser("analyst", UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	if _, err := manager.SessionFor(token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("SessionFor() after disable error = %v, want ErrInvalidSession", err)
	}
	if _, err := manager.Authenticate("analyst", "strong-password-456"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatalf("disabled login error = %v, want ErrInvalidLogin", err)
	}
	reloaded, err := NewManager(path)
	if err != nil {
		t.Fatalf("reload manager error = %v", err)
	}
	if len(reloaded.ListUsers()) != 2 {
		t.Fatalf("reloaded users = %+v", reloaded.ListUsers())
	}
	if _, err := reloaded.CreateUser("owner2", "strong-password-789", RoleOperator); err != nil {
		t.Fatalf("CreateUser() after reload error = %v", err)
	}
}

func TestManagerPreventsRemovingLastAdministrator(t *testing.T) {
	manager, err := NewTestManager("owner", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	viewerRole := RoleViewer
	if _, err := manager.UpdateUser("owner", UserUpdate{Role: &viewerRole}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("UpdateUser() role error = %v, want ErrLastAdministrator", err)
	}
	disabled := true
	if _, err := manager.UpdateUser("owner", UserUpdate{Disabled: &disabled}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("UpdateUser() disable error = %v, want ErrLastAdministrator", err)
	}
}

package static

import (
	"os"
	"strings"
	"testing"
)

func TestBusinessScriptUsesAuthenticatedAPIs(t *testing.T) {
	content, err := os.ReadFile("business.js")
	if err != nil {
		t.Fatalf("read business.js: %v", err)
	}
	script := string(content)

	for _, behavior := range []string{
		`/api/v1/dashboard`,
		`/api/v1/groups`,
		`/api/v1/monitor/rules`,
		`/api/v1/monitor/channels`,
		`/api/v1/monitor/alerts`,
		`notification_channel_name`,
		`notification_channel_id`,
		`/api/v1/messages?`,
		`/api/v1/reports?days=`,
		`X-CSRF-Token`,
		`window.location.replace("/login")`,
		`renderChart`,
		`formatPhone`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("business.js missing behavior %q", behavior)
		}
	}
	for _, forbidden := range []string{
		"innerHTML",
		"insertAdjacentHTML",
		"eval(",
		"document.write(",
		"localStorage",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("business.js must not use %q", forbidden)
		}
	}
}

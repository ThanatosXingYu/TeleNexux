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
		`/api/v1/monitor/rules/preview`,
		`/versions`,
		`/api/v1/monitor/channels`,
		`data-notification-channel-form`,
		`data-channel-list-body`,
		`cooldown_seconds`,
		`exclude_keywords`,
		`data-rule-preview`,
		`data-rule-version-list`,
		`delivery_next_attempt_at`,
		`/api/v1/monitor/alerts`,
		`/api/v1/monitor/targets`,
		`data-monitor-backlog`,
		`data-monitor-health-body`,
		`data-monitor-account-body`,
		`data-monitor-queued`,
		`data-monitor-delivery-queued`,
		`data-monitor-delay`,
		`data-monitor-flood`,
		`next_collection_at`,
		`formatDuration`,
		`checkpoint_delay_seconds`,
		`last_batch_fetched`,
		`最近批次 ${Number(target.last_batch_fetched) || 0} 条`,
		`尚无批次`,
		`notification_channel_name`,
		`notification_channel_id`,
		`delivery_state`,
		`delivery_at`,
		`无投递记录`,
		`method: "PATCH"`,
		`set_severity`,
		`data-alert-action-form`,
		`/api/v1/messages?`,
		`/api/v1/search/health`,
		`/api/v1/search/rebuild`,
		`data-search-index-state`,
		`data-search-index-rebuild`,
		`renderIndexHealth`,
		`/api/v1/correlations?`,
		`data-correlation-list`,
		`data-correlation-type`,
		`initCorrelations`,
		`confidence_reason`,
		`evidence_event_id`,
		`crypto_address`,
		`explorer_url`,
		`/api/v1/reports?days=`,
		`/api/v1/evidence/manifest?days=`,
		`data-evidence-manifest-generate`,
		`data-evidence-manifest-actor`,
		`data-evidence-manifest-sha`,
		`X-CSRF-Token`,
		`window.location.replace("/login")`,
		`renderChart`,
		`formatPhone`,
		`accountDisplayStatus`,
		`verificationTTL`,
		`状态待确认`,
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

package alerts

import (
	"context"
	"strings"
	"testing"

	"monitor/internal/testutil"
)

func TestCheckWebhookURL_HTTPSOnlyUnlessAllowed(t *testing.T) {
	good := []string{"https://hooks.slack.com/services/T0/B0/abc", " https://chat.example.com/hooks/x "}
	for _, raw := range good {
		if got, err := CheckWebhookURL(raw, false); err != nil || got != strings.TrimSpace(raw) {
			t.Errorf("CheckWebhookURL(%q) = %q, %v", raw, got, err)
		}
	}
	bad := []string{"http://hooks.slack.com/services/x", "ftp://hooks.slack.com/x", "hooks.slack.com/x", "https://u:p@hooks.slack.com/x", "https://"}
	for _, raw := range bad {
		if _, err := CheckWebhookURL(raw, false); err == nil || !strings.HasSuffix(err.Error(), ".") {
			t.Errorf("CheckWebhookURL(%q) = %v, want a sentence refusing it", raw, err)
		}
	}
	if _, err := CheckWebhookURL("http://hooks.slack.com/x", false); err == nil || !strings.Contains(err.Error(), "https://") {
		t.Errorf("plain http: err = %v, want one asking for https://", err)
	}

	// DEV_ALLOW_HTTP_WEBHOOKS lets local fakes in, nothing else.
	if _, err := CheckWebhookURL("http://127.0.0.1:9999/services/x", true); err != nil {
		t.Errorf("http with the dev flag: %v", err)
	}
	if _, err := CheckWebhookURL("ftp://127.0.0.1/x", true); err == nil {
		t.Error("ftp with the dev flag: want an error")
	}
}

func TestSlack_PostsHTTPOnlyWhenAllowed(t *testing.T) {
	fake := testutil.NewSlackServer(t) // plain http, as local fakes are
	ctx := context.Background()
	err := NewSlack(false).Post(ctx, fake.URL, "hello")
	if err == nil || fake.Requests() != 0 {
		t.Fatalf("http webhook without the dev flag: err = %v, %d requests; want refused before sending", err, fake.Requests())
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Errorf("the error shows the webhook: %v", err)
	}
	if err := NewSlack(true).Post(ctx, fake.URL, "hello"); err != nil {
		t.Fatalf("with the dev flag: %v", err)
	}
	if got := fake.Messages(); len(got) != 1 || got[0] != "hello" {
		t.Errorf("messages = %q", got)
	}
}

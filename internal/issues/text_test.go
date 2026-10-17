package issues

import (
	"testing"

	"monitor/internal/store"
)

func TestEventText_AsIngestSummarizes(t *testing.T) {
	data := `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc","platform":"php","level":"warning",
		"exception":{"values":[{"type":"RuntimeException","value":"Checkout failed",
		"stacktrace":{"frames":[{"filename":"app/PaymentGateway.php","lineno":12,"in_app":true}]}}]}}`
	got, err := EventText(data)
	if err != nil {
		t.Fatal(err)
	}
	want := store.IssueText{Title: "RuntimeException", Message: "Checkout failed", Culprit: "PaymentGateway.php:12", Level: "warning"}
	if got != want {
		t.Errorf("EventText = %+v, want %+v", got, want)
	}
	if _, err := EventText("not json"); err == nil {
		t.Error("EventText of a broken event: no error")
	}
}

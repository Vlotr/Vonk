package sentry_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"monitor/internal/sentry"
)

// frame describes one stack frame for stackEvent.
type frame struct {
	file, function string
	line           int
	inApp          bool
}

// stackEvent builds an event with one exception and these frames (oldest call first).
func stackEvent(t *testing.T, excType, value string, frames ...frame) *sentry.Event {
	t.Helper()
	var list []map[string]any
	for _, f := range frames {
		list = append(list, map[string]any{"filename": f.file, "function": f.function, "lineno": f.line, "colno": 3, "in_app": f.inApp})
	}
	data, _ := json.Marshal(map[string]any{
		"platform": "php",
		"exception": map[string]any{"values": []any{map[string]any{
			"type": excType, "value": value, "stacktrace": map[string]any{"frames": list},
		}}},
	})
	return decode(t, string(data))
}

var checkoutFrames = []frame{
	{"/public/index.php", "{main}", 52, true},
	{"/vendor/laravel/framework/src/Illuminate/Routing/Router.php", "Illuminate\\Routing\\Router::dispatch", 740, false},
	{"/app/Http/Controllers/CheckoutController.php", "App\\Http\\Controllers\\CheckoutController::store", 16, true},
	{"/app/Services/PaymentGateway.php", "App\\Services\\PaymentGateway::charge", 12, true},
}

func withFrames(frames []frame, change func(i int, f *frame)) []frame {
	out := append([]frame(nil), frames...)
	for i := range out {
		change(i, &out[i])
	}
	return out
}

func TestFingerprint_Format(t *testing.T) {
	fp := sentry.Fingerprint(stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...))
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(fp) {
		t.Errorf("Fingerprint = %q, want 32 hex characters", fp)
	}
}

func TestFingerprint_IgnoresLineNumbers(t *testing.T) {
	a := stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...)
	b := stackEvent(t, "RuntimeException", "Checkout failed", withFrames(checkoutFrames, func(i int, f *frame) { f.line += 10 * (i + 1) })...)

	if sentry.Fingerprint(a) != sentry.Fingerprint(b) {
		t.Error("moving the code to other lines changed the fingerprint")
	}
}

func TestFingerprint_IgnoresMessageWhenThereIsAStack(t *testing.T) {
	a := stackEvent(t, "RuntimeException", "Checkout failed for order 1", checkoutFrames...)
	b := stackEvent(t, "RuntimeException", "Payment declined", checkoutFrames...)

	if sentry.Fingerprint(a) != sentry.Fingerprint(b) {
		t.Error("a different message changed the fingerprint of the same stack")
	}
}

func TestFingerprint_DifferentFunctionDiffers(t *testing.T) {
	base := stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...)
	otherFunction := stackEvent(t, "RuntimeException", "Checkout failed", withFrames(checkoutFrames, func(i int, f *frame) {
		if i == 3 {
			f.function = "App\\Services\\PaymentGateway::refund"
		}
	})...)
	otherFile := stackEvent(t, "RuntimeException", "Checkout failed", withFrames(checkoutFrames, func(i int, f *frame) {
		if i == 2 {
			f.file = "/app/Http/Controllers/OrderController.php"
		}
	})...)
	otherType := stackEvent(t, "LogicException", "Checkout failed", checkoutFrames...)

	fp := sentry.Fingerprint(base)
	for name, ev := range map[string]*sentry.Event{"function": otherFunction, "file": otherFile, "type": otherType} {
		if sentry.Fingerprint(ev) == fp {
			t.Errorf("a different %s gave the same fingerprint", name)
		}
	}
}

func TestFingerprint_OnlyTopFiveInAppFrames(t *testing.T) {
	var frames []frame
	for i := range 8 {
		frames = append(frames, frame{fmt.Sprintf("/app/Step%d.php", i), fmt.Sprintf("step%d", i), 10, true})
		frames = append(frames, frame{"/vendor/lib/Pipeline.php", fmt.Sprintf("pipe%d", i), 20, false})
	}
	base := sentry.Fingerprint(stackEvent(t, "RuntimeException", "x", frames...))

	// Frames 0..2 are older than the top five in-app frames (3..7), and vendor frames do not count.
	deep := withFrames(frames, func(i int, f *frame) {
		if i == 0 || !f.inApp {
			f.function += "Changed"
		}
	})
	if sentry.Fingerprint(stackEvent(t, "RuntimeException", "x", deep...)) != base {
		t.Error("an old in-app frame or a vendor frame changed the fingerprint")
	}

	near := withFrames(frames, func(i int, f *frame) {
		if i == 6 { // step3, the fifth in-app frame from the top
			f.function += "Changed"
		}
	})
	if sentry.Fingerprint(stackEvent(t, "RuntimeException", "x", near...)) == base {
		t.Error("a change in the top five in-app frames kept the fingerprint")
	}
}

func TestFingerprint_NoInAppFramesUsesAllFrames(t *testing.T) {
	vendorOnly := withFrames(checkoutFrames, func(_ int, f *frame) { f.inApp = false })
	base := sentry.Fingerprint(stackEvent(t, "RuntimeException", "x", vendorOnly...))

	changed := withFrames(vendorOnly, func(i int, f *frame) {
		if i == 1 {
			f.function = "Illuminate\\Routing\\Router::other"
		}
	})
	if sentry.Fingerprint(stackEvent(t, "RuntimeException", "x", changed...)) == base {
		t.Error("without in-app frames, a change in the other frames kept the fingerprint")
	}
}

func TestFingerprint_NormalizesBrowserFileNames(t *testing.T) {
	jsFrames := func(file string) []frame {
		return []frame{
			{file, "HTMLButtonElement.onCheckoutClick", 8305, true},
			{file, "renderCartTotal", 8301, true},
		}
	}
	base := sentry.Fingerprint(stackEvent(t, "TypeError", "x", jsFrames("http://localhost:5173/assets/index-D8xk2Lp1.js")...))

	same := []string{
		"https://shop.example.com/assets/index-Bq3x9ZzA.js",     // other host, new build hash
		"https://shop.example.com/assets/index-BqXzWkPq.js?v=2", // hash without digits, query
		"https://cdn.example.com/assets/index-D8xk2Lp1.js#top",
	}
	for _, file := range same {
		if sentry.Fingerprint(stackEvent(t, "TypeError", "x", jsFrames(file)...)) != base {
			t.Errorf("%s: the build hash, host or query changed the fingerprint", file)
		}
	}
	if sentry.Fingerprint(stackEvent(t, "TypeError", "x", jsFrames("http://localhost:5173/assets/checkout-D8xk2Lp1.js")...)) == base {
		t.Error("a different bundle name gave the same fingerprint")
	}
}

func TestFingerprint_IgnoresPHPClosureLines(t *testing.T) {
	closure := func(line int) []frame {
		return []frame{{"/routes/web.php", fmt.Sprintf("{closure:/routes/web.php:%d}", line), line + 1, true}}
	}
	if sentry.Fingerprint(stackEvent(t, "RuntimeException", "x", closure(42)...)) != sentry.Fingerprint(stackEvent(t, "RuntimeException", "x", closure(57)...)) {
		t.Error("the line number in a PHP closure name changed the fingerprint")
	}
}

func TestFingerprint_InnermostException(t *testing.T) {
	chain := func(outerType string) *sentry.Event {
		return decode(t, `{"exception":{"values":[
			{"type":"PDOException","value":"SQLSTATE[23000]","stacktrace":{"frames":[{"filename":"/app/Orders.php","function":"save","in_app":true}]}},
			{"type":"`+outerType+`","value":"wrapped","stacktrace":{"frames":[{"filename":"/app/Other.php","function":"run","in_app":true}]}}
		]}}`)
	}
	if sentry.Fingerprint(chain("QueryException")) != sentry.Fingerprint(chain("WrapperException")) {
		t.Error("the outer exception of a chain changed the fingerprint; only the innermost should count")
	}
}

func TestFingerprint_SDKDefault(t *testing.T) {
	plain := stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...)
	withDefault := stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...)
	withDefault.Raw["fingerprint"] = []any{"{{ default }}"}
	withDefault.Refresh()

	if sentry.Fingerprint(withDefault) != sentry.Fingerprint(plain) {
		t.Error(`["{{ default }}"] did not give the computed fingerprint`)
	}

	extended := stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...)
	extended.Raw["fingerprint"] = []any{"{{default}}", "tenant-a"}
	extended.Refresh()
	if sentry.Fingerprint(extended) == sentry.Fingerprint(plain) {
		t.Error(`["{{default}}", "tenant-a"] gave the same fingerprint as the default`)
	}
}

func TestFingerprint_SDKCustom(t *testing.T) {
	a := stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...)
	b := stackEvent(t, "PDOException", "Deadlock found", frame{"/app/Jobs/Sync.php", "handle", 3, true})
	for _, ev := range []*sentry.Event{a, b} {
		ev.Raw["fingerprint"] = []any{"checkout-deadlock"}
		ev.Refresh()
	}

	if sentry.Fingerprint(a) != sentry.Fingerprint(b) {
		t.Error(`two events with fingerprint ["checkout-deadlock"] were not grouped`)
	}
	if sentry.Fingerprint(a) == sentry.Fingerprint(stackEvent(t, "RuntimeException", "Checkout failed", checkoutFrames...)) {
		t.Error("the custom fingerprint was not used")
	}
}

func TestFingerprint_NoStacktrace(t *testing.T) {
	fp := func(excType, value string) string {
		return sentry.Fingerprint(stackEvent(t, excType, value))
	}

	same := [][2]string{
		{"Order 1234 failed", "Order 98 failed"},
		{"Job 550e8400-e29b-41d4-a716-446655440000 timed out", "Job 6BA7B810-9DAD-11D1-80B4-00C04FD430C8 timed out"},
		{"Bad pointer 0x7ffd5e8c", "Bad pointer 0xDEADBEEF"},
		{"Cache key deadbeefcafe1234 missing", "Cache key 0123abcd4567ef89 missing"},
		{"Order 12345678 failed", "Order 1 failed"},
	}
	for _, pair := range same {
		if fp("RuntimeException", pair[0]) != fp("RuntimeException", pair[1]) {
			t.Errorf("%q and %q were not grouped", pair[0], pair[1])
		}
	}
	if fp("RuntimeException", "Order 1 failed") == fp("RuntimeException", "Payment 1 failed") {
		t.Error("different messages were grouped")
	}
	if fp("RuntimeException", "Order 1 failed") == fp("LogicException", "Order 1 failed") {
		t.Error("different types were grouped")
	}

	// A log message without an exception is grouped by its text the same way.
	msg := func(text string) string {
		return sentry.Fingerprint(decode(t, `{"message":`+fmt.Sprintf("%q", text)+`}`))
	}
	if msg("Queue backlog 120 jobs") != msg("Queue backlog 7 jobs") || msg("Queue backlog 120 jobs") == msg("Disk almost full") {
		t.Error("log messages are not grouped by their normalized text")
	}
}

func TestSummarize(t *testing.T) {
	php := sentry.Summarize(parse(t, fixture(t, "php-exception.envelope.gz")).Events[0])
	if php != (sentry.Summary{Title: "RuntimeException", Message: "Checkout failed", Culprit: "PaymentGateway.php:12"}) {
		t.Errorf("PHP fixture summary = %+v", php)
	}

	browser := sentry.Summarize(parse(t, fixture(t, "browser-typeerror.envelope")).Events[0])
	want := sentry.Summary{Title: "TypeError", Message: "Cannot read properties of undefined (reading 'toFixed')", Culprit: "index-D8xk2Lp1.js:8301"}
	if browser != want {
		t.Errorf("browser fixture summary = %+v, want %+v", browser, want)
	}

	cases := []struct {
		name string
		json string
		want sentry.Summary
	}{
		{"log message", `{"message":"Payment provider timed out\nretrying"}`,
			sentry.Summary{Title: "Payment provider timed out", Message: "Payment provider timed out\nretrying"}},
		{"nothing", `{}`, sentry.Summary{Title: "<unlabeled event>"}},
		{"exception without type", `{"exception":{"values":[{"value":"boom"}]}}`, sentry.Summary{Title: "Error", Message: "boom"}},
		{"no in-app frame: top frame", `{"exception":{"values":[{"type":"E","stacktrace":{"frames":[
			{"filename":"/vendor/a/A.php","lineno":3},{"abs_path":"https://cdn.example.com/lib/b.js?v=1","lineno":9}]}}]}}`,
			sentry.Summary{Title: "E", Culprit: "b.js:9"}},
		{"innermost exception", `{"exception":{"values":[{"type":"Inner","value":"cause"},{"type":"Outer","value":"wrapped"}]}}`,
			sentry.Summary{Title: "Inner", Message: "cause"}},
	}
	for _, tc := range cases {
		if got := sentry.Summarize(decode(t, tc.json)); got != tc.want {
			t.Errorf("%s: Summarize = %+v, want %+v", tc.name, got, tc.want)
		}
	}

	long := sentry.Summarize(decode(t, `{"exception":{"values":[{"type":"`+strings.Repeat("T", 500)+`","value":"`+strings.Repeat("é", 5000)+`"}]}}`))
	if len([]rune(long.Title)) > 200 || len([]rune(long.Message)) > 1000 || !strings.HasSuffix(long.Message, "…") {
		t.Errorf("long title/message not shortened: %d / %d runes", len([]rune(long.Title)), len([]rune(long.Message)))
	}
}

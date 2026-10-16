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

// Frame URLs are masked before they are stored (a hex directory becomes
// :hash, as page URLs do), and grouping stays what it was before masking.
func TestFingerprint_MaskedHashDirectoryGroupsAsBefore(t *testing.T) {
	file := func(dir string) []frame {
		return []frame{{"https://cdn.example.com/" + dir + "/app.js", "renderCart", 10, true}}
	}
	sent := stackEvent(t, "TypeError", "x", file("0123456789abcdef0123")...)
	masked := stackEvent(t, "TypeError", "x", file(":hash")...)
	if sentry.Fingerprint(sent) != sentry.Fingerprint(masked) {
		t.Error("masking a hash directory changes the fingerprint")
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

func TestFingerprint_MainExceptionIsTheLast(t *testing.T) {
	// Sentry lists a chain oldest first: the cause, then the exception that wraps it.
	chain := func(causeType, outerType string) *sentry.Event {
		return decode(t, `{"exception":{"values":[
			{"type":"`+causeType+`","value":"SQLSTATE[23000]","stacktrace":{"frames":[{"filename":"/app/Orders.php","function":"save","in_app":true}]}},
			{"type":"`+outerType+`","value":"wrapped","stacktrace":{"frames":[{"filename":"/app/Other.php","function":"run","in_app":true}]}}
		]}}`)
	}
	if sentry.Fingerprint(chain("PDOException", "QueryException")) != sentry.Fingerprint(chain("OtherCause", "QueryException")) {
		t.Error("the cause of a chain changed the fingerprint; only the last (outer) exception should count")
	}
	if sentry.Fingerprint(chain("PDOException", "QueryException")) == sentry.Fingerprint(chain("PDOException", "ViewException")) {
		t.Error("a different outer exception gave the same fingerprint")
	}
}

func TestSummarize_UnhandledRejection(t *testing.T) {
	// @sentry/browser 11 reports an unhandled promise rejection as an Error
	// with mechanism "auto.browser.global_handlers.onunhandledrejection".
	ev := parse(t, fixture(t, "browser-rejection.envelope")).Events[0]
	want := sentry.Summary{Title: "Unhandled rejection", Message: "Failed to fetch /api/stock", Culprit: "src/main.js:30"}
	if got := sentry.Summarize(ev); got != want {
		t.Errorf("Summarize = %+v, want %+v", got, want)
	}

	// Older SDKs and Node name the mechanism without a prefix.
	plain := decode(t, `{"platform":"node","exception":{"values":[{"type":"Error","value":"boom","mechanism":{"type":"onunhandledrejection","handled":false}}]}}`)
	if got := sentry.Summarize(plain).Title; got != "Unhandled rejection" {
		t.Errorf("mechanism onunhandledrejection: title = %q", got)
	}
	// Other mechanisms keep the exception type.
	handled := decode(t, `{"platform":"javascript","exception":{"values":[{"type":"TypeError","value":"x","mechanism":{"type":"auto.browser.browserapierrors.addEventListener"}}]}}`)
	if got := sentry.Summarize(handled).Title; got != "TypeError" {
		t.Errorf("addEventListener mechanism: title = %q, want TypeError", got)
	}

	// The label is only for people: grouping still uses the exception type.
	withoutMechanism := parse(t, fixture(t, "browser-rejection.envelope")).Events[0]
	delete(withoutMechanism.Raw["exception"].(map[string]any)["values"].([]any)[0].(map[string]any), "mechanism")
	withoutMechanism.Refresh()
	if sentry.Fingerprint(ev) != sentry.Fingerprint(withoutMechanism) {
		t.Error("the unhandled-rejection label changed the fingerprint")
	}
}

func TestSummarize_CulpritPerPlatform(t *testing.T) {
	culprit := func(platform, frameJSON string) string {
		ev := decode(t, `{"platform":"`+platform+`","exception":{"values":[{"type":"E","stacktrace":{"frames":[`+frameJSON+`]}}]}}`)
		return sentry.Summarize(ev).Culprit
	}
	tests := []struct {
		name, platform, frame, want string
	}{
		{"browser: the URL's path without origin and query", "javascript",
			`{"filename":"https://shop.example.com/assets/index-D8xk2Lp1.js?v=2#x","lineno":1,"in_app":true}`, "assets/index-D8xk2Lp1.js:1"},
		{"browser: abs_path when there is no filename", "javascript",
			`{"abs_path":"http://127.0.0.1:5173/src/main.js","lineno":21,"in_app":true}`, "src/main.js:21"},
		{"browser: other URL schemes", "javascript", `{"filename":"app:///main.js","lineno":4,"in_app":true}`, "main.js:4"},
		{"browser: a path, not a URL", "javascript", `{"filename":"/resources/js/cart.js","lineno":142,"in_app":true}`, "resources/js/cart.js:142"},
		{"browser: no file, the module", "javascript", `{"module":"cart","lineno":3,"in_app":true}`, "cart:3"},
		{"PHP keeps the file name", "php",
			`{"filename":"/app/Http/Controllers/CheckoutController.php","lineno":14,"in_app":true}`, "CheckoutController.php:14"},
		{"Node keeps the file name", "node", `{"filename":"/srv/app/src/jobs/sync.js","lineno":9,"in_app":true}`, "sync.js:9"},
	}
	for _, tt := range tests {
		if got := culprit(tt.platform, tt.frame); got != tt.want {
			t.Errorf("%s: culprit = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestFingerprint_CulpritChangeKeepsGrouping(t *testing.T) {
	// The browser culprit became a path; grouping still uses the normalized
	// file without host or build hash, so the same error from another
	// deploy or host is the same issue.
	a := decode(t, `{"platform":"javascript","exception":{"values":[{"type":"TypeError","stacktrace":{"frames":[
		{"filename":"https://shop.example.com/assets/index-D8xk2Lp1.js","function":"addToCart","lineno":1,"in_app":true}]}}]}}`)
	b := decode(t, `{"platform":"javascript","exception":{"values":[{"type":"TypeError","stacktrace":{"frames":[
		{"filename":"https://staging.example.com/assets/index-Q9wz7Rt3.js?v=3","function":"addToCart","lineno":1,"in_app":true}]}}]}}`)
	if sentry.Fingerprint(a) != sentry.Fingerprint(b) {
		t.Error("the same browser error from another host and build was not grouped")
	}
}

func TestSummarize_LaravelQueryExceptionChain(t *testing.T) {
	// Laravel wraps a PDOException in a QueryException; the issue is named after the QueryException.
	ev := decode(t, `{"platform":"php","exception":{"values":[
		{"type":"PDOException","value":"SQLSTATE[23000]: Integrity constraint violation","stacktrace":{"frames":[
			{"filename":"/vendor/laravel/framework/src/Illuminate/Database/Connection.php","function":"execute","lineno":587,"in_app":false},
			{"filename":"/app/Repositories/OrderRepository.php","function":"insert","lineno":40,"in_app":true}]}},
		{"type":"Illuminate\\Database\\QueryException","value":"SQLSTATE[23000]: Integrity constraint violation (Connection: mysql, SQL: insert into orders ...)","stacktrace":{"frames":[
			{"filename":"/app/Http/Controllers/OrderController.php","function":"store","lineno":28,"in_app":true},
			{"filename":"/vendor/laravel/framework/src/Illuminate/Database/Connection.php","function":"runQueryCallback","lineno":829,"in_app":false}]}}
	]}}`)

	want := sentry.Summary{
		Title:   `Illuminate\Database\QueryException`,
		Message: "SQLSTATE[23000]: Integrity constraint violation (Connection: mysql, SQL: insert into orders ...)",
		Culprit: "OrderController.php:28",
	}
	if got := sentry.Summarize(ev); got != want {
		t.Errorf("Summarize = %+v, want %+v", got, want)
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

	// Browser culprits are the file's path on the site, not only its name.
	browser := sentry.Summarize(parse(t, fixture(t, "browser-typeerror.envelope")).Events[0])
	want := sentry.Summary{Title: "TypeError", Message: "Cannot read properties of undefined (reading 'toFixed')", Culprit: "assets/index-D8xk2Lp1.js:8301"}
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
		{"last exception of a chain", `{"exception":{"values":[{"type":"Inner","value":"cause"},{"type":"Outer","value":"wrapped"}]}}`,
			sentry.Summary{Title: "Outer", Message: "wrapped"}},
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

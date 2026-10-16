package scrub_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"monitor/internal/scrub"
	"monitor/internal/sentry"
)

// scrubbed decodes an event, scrubs it and returns it with its stored JSON.
func scrubbed(t *testing.T, event string) (*sentry.Event, string) {
	t.Helper()
	ev, err := sentry.DecodeEvent([]byte(event))
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	scrub.Event(ev)
	data, err := ev.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return ev, string(data)
}

// at walks a decoded JSON value: at(raw, "request", "headers", "Cookie").
func at(v any, path ...any) any {
	for _, p := range path {
		switch key := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[key]
		case int:
			list, _ := v.([]any)
			if key >= len(list) {
				return nil
			}
			v = list[key]
		}
	}
	return v
}

func has(v any, path ...string) bool {
	for i, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		next, ok := m[p]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		v = next
	}
	return false
}

func assertNotContains(t *testing.T, data string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(data, s) {
			t.Errorf("stored JSON still contains %q:\n%s", s, data)
		}
	}
}

func TestScrub_Headers(t *testing.T) {
	ev, data := scrubbed(t, `{"request":{"headers":{
		"Authorization":"Bearer sk_live_abc","Cookie":"laravel_session=abc","X-Api-Key":"key123",
		"X-CSRF-TOKEN":"csrf123","Set-Cookie":"sid=1","User-Agent":"Mozilla/5.0","Accept":"*/*"}}}`)

	for _, name := range []string{"Authorization", "Cookie", "X-Api-Key", "X-CSRF-TOKEN", "Set-Cookie"} {
		if got := ev.Request.Headers[name]; got != "[Filtered]" {
			t.Errorf("header %s = %q, want [Filtered]", name, got)
		}
	}
	if ev.Request.Headers["User-Agent"] != "Mozilla/5.0" || ev.Request.Headers["Accept"] != "*/*" {
		t.Errorf("harmless headers changed: %v", ev.Request.Headers)
	}
	assertNotContains(t, data, "sk_live_abc", "laravel_session=abc", "key123", "csrf123")

	// Headers as a list of pairs, and with list values as sentry-php sends them.
	_, data = scrubbed(t, `{"request":{"headers":[["Authorization","Bearer sk_live_abc"],["Accept","*/*"]]}}`)
	assertNotContains(t, data, "sk_live_abc")
	if !strings.Contains(data, `["Accept","*/*"]`) {
		t.Errorf("harmless pair changed: %s", data)
	}
	_, data = scrubbed(t, `{"request":{"headers":{"Cookie":["laravel_session=abc"],"Host":["shop.example.com"]}}}`)
	assertNotContains(t, data, "laravel_session=abc")
	if !strings.Contains(data, `"Host":["shop.example.com"]`) {
		t.Errorf("harmless header changed: %s", data)
	}
}

func TestScrub_DropsCookies(t *testing.T) {
	ev, data := scrubbed(t, `{"request":{"cookies":{"theme":"dark","laravel_session":"abc"},"url":"https://shop.example.com/"}}`)

	if has(ev.Raw, "request", "cookies") {
		t.Errorf("request.cookies kept: %s", data)
	}
	if has(ev.Raw, "request", "headers") || has(ev.Raw, "request", "env") {
		t.Errorf("scrubbing added request fields the SDK did not send: %s", data)
	}
	assertNotContains(t, data, "dark", "abc")
}

func TestScrub_NestedExtra(t *testing.T) {
	ev, data := scrubbed(t, `{
		"extra":{"order":{"id":5,"payment":{"card_number":"4111111111111111","holder":"Jan"},"notes":[{"api_key":"k-123"},"plain"]},"db_password":"pw-123"},
		"contexts":{"app":{"secret_value":"s-123","name":"shop"},"os":{"name":"Linux"}},
		"tags":{"session_id":"sess-123","browser":"Chrome"},
		"user":{"id":"42","email":"jan@example.com","data":{"auth_token":"t-123"}}}`)

	assertNotContains(t, data, "4111111111111111", "k-123", "pw-123", "s-123", "sess-123", "t-123")
	if at(ev.Raw, "extra", "order", "payment", "card_number") != "[Filtered]" {
		t.Errorf("card_number = %v", at(ev.Raw, "extra", "order", "payment", "card_number"))
	}
	for _, kept := range [][]any{
		{"extra", "order", "payment", "holder"}, {"extra", "order", "notes", 1}, {"contexts", "app", "name"},
		{"contexts", "os", "name"}, {"tags", "browser"}, {"user", "email"},
	} {
		if v, _ := at(ev.Raw, kept...).(string); v == "" || v == "[Filtered]" {
			t.Errorf("%v = %v, want it kept", kept, at(ev.Raw, kept...))
		}
	}
	if ev.Tags["session_id"] != "[Filtered]" || ev.User.ID != "42" {
		t.Errorf("tags/user after scrub = %v / %+v", ev.Tags, ev.User)
	}
}

func TestScrub_URLQuery(t *testing.T) {
	ev, data := scrubbed(t, `{"request":{
		"url":"https://shop.example.com/checkout?coupon=SPRING&token=tok-123&IBAN=NL91ABNA0417164300#access_token=at-123&tab=2",
		"query_string":"coupon=SPRING&api_key=key-123&page=2"}}`)

	assertNotContains(t, data, "tok-123", "NL91ABNA0417164300", "at-123", "key-123")
	want := "https://shop.example.com/checkout?coupon=SPRING&token=[Filtered]&IBAN=[Filtered]#access_token=[Filtered]&tab=2"
	if ev.Request.URL != want {
		t.Errorf("url = %q, want %q", ev.Request.URL, want)
	}
	if ev.Request.QueryString != "coupon=SPRING&api_key=[Filtered]&page=2" {
		t.Errorf("query_string = %q", ev.Request.QueryString)
	}

	// Query strings as an object or a list of pairs.
	_, data = scrubbed(t, `{"request":{"query_string":{"token":"tok-123","page":"2"}}}`)
	assertNotContains(t, data, "tok-123")
	_, data = scrubbed(t, `{"request":{"query_string":[["password","pw-123"],["page","2"]]}}`)
	assertNotContains(t, data, "pw-123")
	if !strings.Contains(data, `["page","2"]`) {
		t.Errorf("harmless pair changed: %s", data)
	}

	// Percent-encoded keys are matched too.
	ev, _ = scrubbed(t, `{"request":{"url":"/reset?pass%77ord=pw-123&x=1"}}`)
	if ev.Request.URL != "/reset?pass%77ord=[Filtered]&x=1" {
		t.Errorf("url = %q", ev.Request.URL)
	}
}

func TestScrub_URLUserInfo(t *testing.T) {
	ev, data := scrubbed(t, `{
		"breadcrumbs":[{"category":"http","data":{"url":"https://user:secret-1@api.example.com/v1/orders?page=2"}}],
		"extra":{
			"database":"mysql://root:secret-2@db/app",
			"cache":"redis://:secret-3@cache:6379/0",
			"log":"connecting to amqp://guest:secret-4@queue:5672 failed, retrying",
			"odd":"https://admin:pa#ss@secret-5@monitor.example.com/x",
			"dsn":"https://5f2b6a0c7d3e4f1a@sentry.example.com/1",
			"plain":"https://shop.example.com/products?ref=mail"}}`)

	assertNotContains(t, data, "secret-1", "secret-2", "secret-3", "secret-4", "secret-5")
	want := map[string]string{
		"database": "mysql://root:[Filtered]@db/app",
		"cache":    "redis://:[Filtered]@cache:6379/0",
		"log":      "connecting to amqp://guest:[Filtered]@queue:5672 failed, retrying",
		"odd":      "https://admin:[Filtered]@monitor.example.com/x",
		// A user name without a password, such as a DSN's public key, stays.
		"dsn":   "https://5f2b6a0c7d3e4f1a@sentry.example.com/1",
		"plain": "https://shop.example.com/products?ref=mail",
	}
	for key, value := range want {
		if got := at(ev.Raw, "extra", key); got != value {
			t.Errorf("extra.%s = %q, want %q", key, got, value)
		}
	}
	if got := at(ev.Raw, "breadcrumbs", 0, "data", "url"); got != "https://user:[Filtered]@api.example.com/v1/orders?page=2" {
		t.Errorf("breadcrumb url = %q", got)
	}
}

func TestScrub_RequestBody(t *testing.T) {
	cases := map[string]string{
		"object":       `{"request":{"data":{"email":"jan@example.com","password":"pw-123","cart":{"cvv":"cvv-123"}}}}`,
		"form string":  `{"request":{"data":"email=jan%40example.com&password=pw-123"}}`,
		"JSON string":  `{"request":{"data":"{\"user\":{\"password\":\"pw-123\"},\"items\":3}"}}`,
		"other string": `{"request":{"data":"<login><password>pw-123</password></login>"}}`,
		"JSON pairs":   `{"request":{"data":"[[\"password\",\"pw-123\"],[\"email\",\"jan@example.com\"]]"}}`,
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			_, data := scrubbed(t, event)
			assertNotContains(t, data, "pw-123", "cvv-123")
		})
	}

	_, data := scrubbed(t, `{"request":{"data":"{\"items\":3,\"password\":\"pw-123\"}"}}`)
	if !strings.Contains(data, `items`) {
		t.Errorf("JSON body lost its harmless fields: %s", data)
	}
	_, data = scrubbed(t, `{"request":{"data":"just some text"}}`)
	if !strings.Contains(data, "just some text") {
		t.Errorf("harmless text body changed: %s", data)
	}
	// A body of [key, value] pairs sent as JSON text gets the pair rule.
	ev, _ := scrubbed(t, `{"request":{"data":"[[\"password\",\"pw-123\"],[\"email\",\"jan@example.com\"]]"}}`)
	if body, _ := at(ev.Raw, "request", "data").(string); body != `[["password","[Filtered]"],["email","jan@example.com"]]` {
		t.Errorf("pair body = %s", body)
	}
}

func TestScrub_Breadcrumbs(t *testing.T) {
	for _, shape := range []string{`{"values":%s}`, `%s`} {
		crumbs := `[
			{"category":"fetch","type":"http","data":{"url":"/api/cart?session_token=sess-123&cvv=123&page=2","method":"GET"}},
			{"category":"navigation","data":{"from":"/checkout?card=4111111111111111","to":"/done?ok=1"}},
			{"category":"db.query","message":"select * from users","data":{"bindings":{"password":"pw-123"}}},
			{"category":"http","data":{"url":"https://payments.example.com/v1/charges?api_key=pk_live_51Hxyz&currency=eur"}}
		]`
		ev, data := scrubbed(t, `{"breadcrumbs":`+strings.Replace(shape, "%s", crumbs, 1)+`}`)

		assertNotContains(t, data, "sess-123", "cvv=123", "4111111111111111", "pw-123", "pk_live_51Hxyz")
		for _, keep := range []string{"page=2", "/done?ok=1", "currency=eur", "select * from users"} {
			if !strings.Contains(data, keep) {
				t.Errorf("harmless part %q lost: %s", keep, data)
			}
		}
		_ = ev
	}
}

func TestScrub_FrameVars(t *testing.T) {
	frames := `{"frames":[{"function":"login","in_app":true,"context_line":"$token = request('token');",
		"vars":{"password":"pw-123","input":{"email":"jan@example.com","password":"pw-456"},"orderId":"1234"}}]}`
	ev, data := scrubbed(t, `{"exception":{"values":[{"type":"E","stacktrace":`+frames+`}]},
		"threads":{"values":[{"stacktrace":`+frames+`}]},
		"stacktrace":`+frames+`}`)

	assertNotContains(t, data, "pw-123", "pw-456")
	vars := ev.Exceptions[0].Frames[0].Vars
	if vars["orderId"] != "1234" || at(vars, "input", "email") != "jan@example.com" {
		t.Errorf("harmless vars changed: %v", vars)
	}
	if ev.Exceptions[0].Frames[0].ContextLine != "$token = request('token');" {
		t.Errorf("source line changed: %q", ev.Exceptions[0].Frames[0].ContextLine)
	}
}

func TestScrub_PairLists(t *testing.T) {
	// SDKs may send these fields as lists of [key, value] pairs.
	ev, data := scrubbed(t, `{
		"request":{
			"headers":[["Authorization","Bearer sk-123"],["X-Forwarded-For","198.51.100.7"],["Accept","*/*"]],
			"query_string":[["token","tok-123"],["page","2"]],
			"env":[["REMOTE_ADDR","203.0.113.9"],["SECRET_KEY","env-123"],["SERVER_NAME","web-1"]],
			"data":[["password","pw-123"],["email","jan@example.com"]]},
		"tags":[["session_id","sess-123"],["browser","Chrome"]]}`)

	assertNotContains(t, data, "sk-123", "198.51.100.7", "tok-123", "203.0.113.9", "env-123", "pw-123", "sess-123")
	for _, keep := range []string{`["Accept","*/*"]`, `["page","2"]`, `["SERVER_NAME","web-1"]`, `["email","jan@example.com"]`, `["browser","Chrome"]`} {
		if !strings.Contains(data, keep) {
			t.Errorf("harmless pair %s lost: %s", keep, data)
		}
	}
	if ev.Tags["session_id"] != "[Filtered]" || ev.Tags["browser"] != "Chrome" {
		t.Errorf("tags = %v", ev.Tags)
	}
}

func TestScrub_KeepsFingerprintAndSourceLines(t *testing.T) {
	// The fingerprint decides grouping and is computed after scrubbing, and
	// source lines are code: neither may change, even when they look sensitive.
	ev, data := scrubbed(t, `{
		"fingerprint":["auth-failure","{{ default }}"],
		"exception":{"values":[{"type":"E","stacktrace":{"frames":[{"function":"login","in_app":true,
			"pre_context":["$password = request('password');","$url = '/reset?token='.$t;"],
			"context_line":"return redirect('/login?next=/account');",
			"post_context":["// cookie","$session = null;"]}]}}]},
		"extra":{"steps":["card-declined","checkout"]}}`)

	if got := strings.Join(ev.Fingerprint, "|"); got != "auth-failure|{{ default }}" {
		t.Errorf("fingerprint = %q, want it unchanged", got)
	}
	f := ev.Exceptions[0].Frames[0]
	if strings.Join(f.PreContext, "|") != "$password = request('password');|$url = '/reset?token='.$t;" ||
		f.ContextLine != "return redirect('/login?next=/account');" ||
		strings.Join(f.PostContext, "|") != "// cookie|$session = null;" {
		t.Errorf("source lines changed: %+v", f)
	}
	if !strings.Contains(data, `"steps":["card-declined","checkout"]`) {
		t.Errorf("a two-item list outside the pair fields was treated as a pair: %s", data)
	}

	_, data = scrubbed(t, `{"fingerprint":["card-declined","checkout"]}`)
	if !strings.Contains(data, `"fingerprint":["card-declined","checkout"]`) {
		t.Errorf("fingerprint changed: %s", data)
	}
}

func TestScrub_DropsIP(t *testing.T) {
	ev, data := scrubbed(t, `{
		"user":{"id":"42","ip_address":"203.0.113.9"},
		"request":{
			"env":{"REMOTE_ADDR":"203.0.113.9","SERVER_ADDR":"10.0.0.5","HTTP_X_FORWARDED_FOR":"198.51.100.7","SERVER_NAME":"web-1"},
			"headers":{"X-Forwarded-For":"198.51.100.7, 10.0.0.1","X-Real-IP":"198.51.100.7","Forwarded":"for=198.51.100.7","CF-Connecting-IP":"2001:db8::1","Accept":"*/*"}},
		"contexts":{"client":{"ip":"{{auto}}","name":"x"}},
		"sdk":{"name":"sentry.javascript.browser","settings":{"infer_ip":"auto"}}}`)

	assertNotContains(t, data, "203.0.113.9", "198.51.100.7", "10.0.0.5", "10.0.0.1", "2001:db8::1", "{{auto}}", "ip_address", "REMOTE_ADDR")
	if ev.User.ID != "42" || ev.User.IPAddress != "" {
		t.Errorf("user = %+v", ev.User)
	}
	if ev.Request.Env["SERVER_NAME"] != "web-1" || ev.Request.Headers["Accept"] != "*/*" {
		t.Errorf("harmless request fields lost: %+v", ev.Request)
	}
	if at(ev.Raw, "contexts", "client", "name") != "x" {
		t.Errorf("contexts.client.name lost: %s", data)
	}

	// The SDK placeholder that asks the server to fill in the IP.
	ev, data = scrubbed(t, `{"user":{"id":"42","ip_address":"{{auto}}"},"request":{"headers":[["X-Real-IP","198.51.100.7"],["Accept","*/*"]]}}`)
	assertNotContains(t, data, "{{auto}}", "ip_address", "198.51.100.7")
	if ev.User.ID != "42" || !strings.Contains(data, `["Accept","*/*"]`) {
		t.Errorf("after scrub: %s", data)
	}
}

func TestScrub_SensitiveKeys(t *testing.T) {
	sensitive := []string{"password", "Passwd", "pwd", "client_secret", "access_token", "api_key", "X-Api-Key", "apikey",
		"auth", "Authorization", "cookie", "laravel_session", "csrf", "xsrf", "credit", "card_number", "cvv", "iban", "ssn", "Set-Cookie"}
	for _, key := range sensitive {
		if !scrub.IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"email", "user_id", "page", "browser", "orderId", "name"} {
		if scrub.IsSensitiveKey(key) {
			t.Errorf("IsSensitiveKey(%q) = true, want false", key)
		}
	}
}

// ipv4Pattern finds dotted quads. A quad right after "/" is a version, as in
// "Chrome/141.0.0.0", and is skipped.
var ipv4Pattern = regexp.MustCompile(`(^|[^/\d.])(\d{1,3}(\.\d{1,3}){3})\b`)

// containsIP reports an IPv4 or IPv6 address anywhere in s.
func containsIP(s string) (string, bool) {
	for _, m := range ipv4Pattern.FindAllStringSubmatch(s, -1) {
		if _, err := netip.ParseAddr(m[2]); err == nil {
			return m[2], true
		}
	}
	for _, word := range regexp.MustCompile(`[0-9A-Fa-f:]{3,}`).FindAllString(s, -1) {
		if strings.Count(word, ":") >= 2 {
			if addr, err := netip.ParseAddr(strings.Trim(word, ":")); err == nil && addr.Is6() {
				return word, true
			}
		}
	}
	return "", false
}

func TestScrub_Fixtures(t *testing.T) {
	cases := map[string][]string{
		// Secrets and IPs the fixtures were captured with; see internal/sentry/testdata/capture.
		"php-exception.envelope.gz": {"hunter2", "4111111111111111", "sk_live_abc123", "eyJpdiI6IjEyMyJ9", "tok_abc123", "pk_live_51Hxyz", "XSRF-TOKEN=abc123",
			"127.0.0.1", "203.0.113.9"},
		"browser-typeerror.envelope": {"4111111111111111", "NL91ABNA0417164300", "cvv=123"},
	}
	for name, secrets := range cases {
		t.Run(name, func(t *testing.T) {
			raw := readFixture(t, name)
			if _, found := containsIP(eventJSON(t, raw, false)); !found && name == "php-exception.envelope.gz" {
				t.Fatal("the PHP fixture should contain an IP before scrubbing; did it change?")
			}

			data := eventJSON(t, raw, true)
			if ip, found := containsIP(data); found {
				t.Errorf("IP %s left after scrubbing:\n%s", ip, data)
			}
			assertNotContains(t, data, append(secrets, "{{auto}}", "ip_address", "REMOTE_ADDR", "cookies")...)
			// What the dashboard needs is still there.
			for _, keep := range []string{`"exception"`, `"in_app":true`, `"id":"42"`, `"jan@example.com"`, `"User-Agent"`} {
				if !strings.Contains(data, keep) {
					t.Errorf("%s lost while scrubbing", keep)
				}
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "sentry", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(name, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if data, err = io.ReadAll(zr); err != nil {
			t.Fatal(err)
		}
	}
	return data
}

// eventJSON parses the fixture envelope and returns its one event as stored JSON.
func eventJSON(t *testing.T, envelope []byte, doScrub bool) string {
	t.Helper()
	env, err := sentry.ParseEnvelope(bytes.NewReader(envelope), 1<<20)
	if err != nil || len(env.Events) != 1 {
		t.Fatalf("ParseEnvelope: %v (%d events)", err, len(env.Events))
	}
	if doScrub {
		scrub.Event(env.Events[0])
	}
	data, err := env.Events[0].JSON()
	if err != nil || !json.Valid(data) {
		t.Fatalf("JSON: %v", err)
	}
	return string(data)
}

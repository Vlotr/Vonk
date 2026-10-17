package routes

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalize_Table(t *testing.T) {
	patterns := []string{"/products/:slug", "/blog/:year/:post/", "shop/:name/reviews"}
	cases := []struct {
		path string
		want string
	}{
		{"/", "/"},
		{"", "/"},
		{"/checkout", "/checkout"},
		{"/checkout/", "/checkout"},
		{"/checkout?step=2&token=tok_abc#top", "/checkout"},
		{"/checkout#/payment", "/checkout"},
		{"https://shop.example.com/cart/?coupon=SPRING", "/cart"},
		{"//shop.example.com/cart", "/cart"},
		{"orders", "/orders"},
		{"/a//b///c/", "/a/b/c"},

		// Project patterns come first: ":slug" stands for one segment.
		{"/products/blue-shoes", "/products/:slug"},
		{"/products/12345", "/products/:slug"},
		{"/products/blue-shoes/reviews", "/products/blue-shoes/reviews"},
		{"/products", "/products"},
		{"/blog/2026/hello-world", "/blog/:year/:post"},
		{"/shop/blue/reviews", "/shop/:name/reviews"},

		// Then the automatic rules, per segment.
		{"/orders/12345", "/orders/:id"},
		{"/orders/12345/items/7", "/orders/:id/items/:id"},
		{"/users/550e8400-e29b-41d4-a716-446655440000/edit", "/users/:uuid/edit"},
		{"/users/550E8400-E29B-41D4-A716-446655440000", "/users/:uuid"},
		{"/assets/0123456789abcdef0123", "/assets/:hash"},
		{"/commit/deadbeefcafebabe", "/commit/:hash"},
		{"/reset/eyJhbGciOiJIUzI1NiJ9xQ", "/reset/:token"},
		{"/invite/Xk9-aB3_qL0zP1mN2oQ4rS", "/invite/:token"},

		// Words, slugs and short codes stay.
		{"/sale/summer-sale-2026-shoes-and-boots", "/sale/summer-sale-2026-shoes-and-boots"},
		{"/docs/v2-release-notes-for-2026", "/docs/v2-release-notes-for-2026"},
		{"/coupon/abc123", "/coupon/abc123"},
		{"/commit/deadbeef", "/commit/deadbeef"},
		{"/ThisIsMyVeryLongPageName", "/ThisIsMyVeryLongPageName"},
	}
	for _, c := range cases {
		if got := Normalize(c.path, patterns); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestNormalize_NoPatterns(t *testing.T) {
	if got := Normalize("/products/12345/", nil); got != "/products/:id" {
		t.Errorf("Normalize without patterns = %q, want /products/:id", got)
	}
}

func TestNormalize_FirstPatternWins(t *testing.T) {
	patterns := []string{"/p/:name", "/p/special"}
	if got := Normalize("/p/special", patterns); got != "/p/:name" {
		t.Errorf("got %q, want the first matching pattern", got)
	}
	// Blank lines are skipped; a line that is no path matches nothing.
	if got := Normalize("/p/x", []string{"", "  ", "not a path", "/p/:n"}); got != "/p/:n" {
		t.Errorf("got %q", got)
	}
}

func TestNormalize_LongPathIsCut(t *testing.T) {
	long := "/"
	for range 100 {
		long += "segment/"
	}
	if got := Normalize(long, nil); len(got) > MaxLength {
		t.Errorf("route is %d bytes, want at most %d", len(got), MaxLength)
	}
}

// Percent-encoding is canonical, so a path Sentry sends encoded and one
// PostHog sends decoded land on the same route.
func TestNormalize_PercentEncoding(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/zoeken/caf%C3%A9", "/zoeken/café"},
		{"/zoeken/café", "/zoeken/café"},
		{"https://shop.example.com/zoeken/caf%c3%a9?q=1", "/zoeken/café"},
		{"/blue%20shoes", "/blue shoes"},
		// An encoded "/" stays one segment.
		{"/files/a%2Fb", "/files/a%2Fb"},
		{"/files/a%2fb/c", "/files/a%2Fb/c"},
		// A literal "%" stays encoded, so a route normalizes to itself.
		{"/sale/100%25", "/sale/100%25"},
		// Invalid escapes are left as they are.
		{"/sale/100%", "/sale/100%"},
		{"/sale/%zz", "/sale/%zz"},
		// Encoded digits are still an id.
		{"/orders/%31%32%33", "/orders/:id"},
		// Escapes that decode to a NUL or to invalid UTF-8 leave their
		// segment as sent: Postgres stores neither in text.
		{"/orders/a%00b", "/orders/a%00b"},
		{"/zoeken/caf%C3/x", "/zoeken/caf%C3/x"},
		{"/x%ffy%20z", "/x%ffy%20z"},
	}
	for _, c := range cases {
		if got := Path(c.path); strings.ContainsRune(got, 0) || !utf8.ValidString(got) {
			t.Errorf("Path(%q) = %q, which Postgres refuses", c.path, got)
		}
		if got := Normalize(c.path, nil); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.path, got, c.want)
		}
		if got := Normalize(Normalize(c.path, nil), nil); got != c.want {
			t.Errorf("Normalize(Normalize(%q)) = %q, want it unchanged (%q)", c.path, got, c.want)
		}
	}
	// Patterns are read the same way.
	if got := Normalize("/zoeken/caf%C3%A9/1", []string{"/zoeken/café/:page"}); got != "/zoeken/café/:page" {
		t.Errorf("pattern with an accent = %q", got)
	}
	if got := Normalize("/files/a%2Fb", []string{"/files/:name"}); got != "/files/:name" {
		t.Errorf("an encoded slash is one segment for patterns too: %q", got)
	}
}

// Cutting a long route or path never splits a character.
func TestNormalize_CutKeepsCharactersWhole(t *testing.T) {
	got := Normalize("/"+strings.Repeat("é", 400), nil) // 801 bytes
	if len(got) > MaxLength || !utf8.ValidString(got) {
		t.Errorf("route is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
	if got := Path("/" + strings.Repeat("ü", 600)); len(got) > MaxPathLength || !utf8.ValidString(got) {
		t.Errorf("path is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}

// laravelResetToken is what Laravel puts in a password reset link: 64 hex characters.
const laravelResetToken = "4f8c2a9e7b1d3f5a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8f0a2c4e6b8d0f2a"

// Path is what is stored of a page: the secrets in it masked (ruling 38),
// digits and words kept, so the raw-path preview still shows the page.
func TestPath_MasksSecrets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/reset-password/" + laravelResetToken, "/reset-password/:hash"},
		{"https://shop.example.com/reset-password/" + laravelResetToken + "?email=jan%40example.com", "/reset-password/:hash"},
		{"/users/550e8400-e29b-41d4-a716-446655440000/edit", "/users/:uuid/edit"},
		{"/invite/Xk9-aB3_qL0zP1mN2oQ4rS", "/invite/:token"},
		{"/orders/12345", "/orders/12345"},
		// Digits are kept however long: 16 or more of them are hex too, but no secret.
		{"/orders/12345678901234567", "/orders/12345678901234567"},
		{"/products/summer-sale-2026-shoes", "/products/summer-sale-2026-shoes"},
		{"/zoeken/caf%C3%A9", "/zoeken/café"},
		{"/files/a%2Fb", "/files/a%2Fb"},
		{"", "/"},
		{"/", "/"},
	}
	for _, c := range cases {
		if got := Path(c.in); got != c.want {
			t.Errorf("Path(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The route of a stored path is the route of the page.
	for _, p := range []string{"/orders/12345", "/orders/12345678901234567", "/reset-password/" + laravelResetToken, "/zoeken/caf%C3%A9", "/sale/100%25"} {
		if a, b := Normalize(Path(p), nil), Normalize(p, nil); a != b {
			t.Errorf("Normalize(Path(%q)) = %q, Normalize = %q", p, a, b)
		}
	}
}

// MaskURL keeps a URL as it was sent but for the secrets in its path.
func TestMaskURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://shop.example.com/reset-password/" + laravelResetToken, "https://shop.example.com/reset-password/:hash"},
		{"https://shop.example.com/reset-password/" + laravelResetToken + "?email=a%40b.c#top", "https://shop.example.com/reset-password/:hash?email=a%40b.c#top"},
		{"http://localhost:8000/users/550e8400-e29b-41d4-a716-446655440000", "http://localhost:8000/users/:uuid"},
		{"/invite/Xk9-aB3_qL0zP1mN2oQ4rS/", "/invite/:token/"},
		{"//cdn.example.com/a/0123456789abcdef0123", "//cdn.example.com/a/:hash"},
		// Digits, words and encoding stay as sent.
		{"https://shop.example.com/orders/12345", "https://shop.example.com/orders/12345"},
		{"https://shop.example.com/orders/12345678901234567", "https://shop.example.com/orders/12345678901234567"},
		{"https://shop.example.com/zoeken/caf%C3%A9", "https://shop.example.com/zoeken/caf%C3%A9"},
		{"https://shop.example.com", "https://shop.example.com"},
		// The query is no path: its values are scrubbed by key elsewhere.
		{"https://shop.example.com/?ref=0123456789abcdef0123", "https://shop.example.com/?ref=0123456789abcdef0123"},
		{"", ""},
	}
	for _, c := range cases {
		if got := MaskURL(c.in); got != c.want {
			t.Errorf("MaskURL(%q)\n = %q\nwant %q", c.in, got, c.want)
		}
	}
}

package routes

import "testing"

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

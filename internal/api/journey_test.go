package api

import (
	"fmt"
	"testing"
	"time"
)

// journeyResponse is what the journey adds to the issue detail: the page
// views before the error, and the route of the error itself.
type journeyResponse struct {
	BeforeError []journeyStep `json:"beforeError"`
	LatestEvent *struct {
		Route string `json:"route"`
	} `json:"latestEvent"`
}

type journeyStep struct {
	Route      string    `json:"route"`
	DurationMs *int64    `json:"durationMs"`
	Start      time.Time `json:"start"`
}

func journeyOf(t *testing.T, env *testEnv, issueID int64) journeyResponse {
	t.Helper()
	got := getJSON[journeyResponse](t, env, fmt.Sprintf("/ui/api/issues/%d", issueID))
	if got.BeforeError == nil {
		t.Fatal("beforeError is missing or null, want a list")
	}
	return got
}

// stepsText writes the steps as "route (duration)" for messages.
func stepsText(steps []journeyStep) string {
	out := ""
	for _, s := range steps {
		d := "no time"
		if s.DurationMs != nil {
			d = fmt.Sprintf("%d ms", *s.DurationMs)
		}
		out += fmt.Sprintf("[%s %s %s] ", s.Route, d, s.Start.Format(time.TimeOnly))
	}
	return out
}

// The latest event's user saw three pages in the 30 minutes before the
// error: they are listed oldest first with their time on the page. Pages
// from before those 30 minutes, after the error, and by bots are left out.
func TestJourney_PagesBeforeError(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	errorAt := testNow.Add(-5 * time.Minute)
	env.visits(t, shop.ID,
		visit{user: "42", route: "/", at: errorAt.Add(-31 * time.Minute), stay: time.Minute},
		visit{user: "42", route: "/products/:slug", at: errorAt.Add(-4 * time.Minute), stay: 100 * time.Second},
		visit{user: "42", route: "/cart", at: errorAt.Add(-2 * time.Minute), stay: 35 * time.Second},
		// Still on the page when the error happened: no time yet.
		visit{user: "42", route: "/checkout", at: errorAt.Add(-80 * time.Second)},
		visit{user: "42", route: "/thanks", at: errorAt.Add(time.Second), stay: time.Minute},
		visit{user: "42", route: "/crawled", at: errorAt.Add(-time.Minute), bot: true},
	)
	// An older event of the same issue from someone else; the latest event decides.
	env.save(t, shop.ID,
		phpEvent(t, eventSpec{id: hexID(1), userID: "7", at: errorAt.Add(-time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(2), userID: "42", url: "https://shop.example.com/checkout?step=2", at: errorAt}),
	)

	got := journeyOf(t, env, firstIssue(t, env, shop.ID))
	want := []string{"/products/:slug", "/cart", "/checkout"}
	if len(got.BeforeError) != len(want) {
		t.Fatalf("before the error: %s; want %v", stepsText(got.BeforeError), want)
	}
	for i, route := range want {
		if got.BeforeError[i].Route != route {
			t.Errorf("step %d = %s, want %s", i+1, got.BeforeError[i].Route, route)
		}
	}
	first, last := got.BeforeError[0], got.BeforeError[2]
	if first.DurationMs == nil || *first.DurationMs != 100_000 || !first.Start.Equal(errorAt.Add(-4*time.Minute)) {
		t.Errorf("first step = %s, want 100 s from 4 minutes before the error", stepsText(got.BeforeError[:1]))
	}
	if d := got.BeforeError[1].DurationMs; d == nil || *d != 35_000 {
		t.Errorf("second step = %s, want 35 s", stepsText(got.BeforeError[1:2]))
	}
	if last.DurationMs != nil {
		t.Errorf("the page of the error has a time (%d ms) although nothing closed it", *last.DurationMs)
	}
	// The card's last row: "Error on /checkout".
	if got.LatestEvent == nil || got.LatestEvent.Route != "/checkout" {
		t.Errorf("latestEvent = %+v, want route /checkout", got.LatestEvent)
	}
}

// At most ten pages: the ten nearest the error, oldest first.
func TestJourney_AtMostTenNearestTheError(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	var visits []visit
	for i := range 14 {
		visits = append(visits, visit{user: "42", route: fmt.Sprintf("/p%d", i), at: testNow.Add(-time.Duration(28-2*i) * time.Minute)})
	}
	env.visits(t, shop.ID, visits...)
	env.save(t, shop.ID, phpEvent(t, eventSpec{id: hexID(1), userID: "42"}))

	got := journeyOf(t, env, firstIssue(t, env, shop.ID))
	if len(got.BeforeError) != 10 || got.BeforeError[0].Route != "/p4" || got.BeforeError[9].Route != "/p13" {
		t.Errorf("before the error: %s; want /p4 to /p13", stepsText(got.BeforeError))
	}
}

// Only the page views of the error's user count: not another user's, not
// an anonymous visitor's, and not the same user id in another project.
func TestJourney_OtherUserIgnored(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	portal := env.pageProject(t, "Portal")
	env.visits(t, shop.ID,
		visit{user: "7", route: "/someone-else", at: testNow.Add(-3 * time.Minute), stay: time.Minute},
		visit{user: "", route: "/anonymous", at: testNow.Add(-2 * time.Minute), stay: time.Minute},
		visit{user: "42", route: "/mine", at: testNow.Add(-time.Minute), stay: 30 * time.Second},
	)
	env.visits(t, portal.ID, visit{user: "42", route: "/other-project", at: testNow.Add(-90 * time.Second), stay: time.Minute})
	env.save(t, shop.ID, phpEvent(t, eventSpec{id: hexID(1), userID: "42"}))

	got := journeyOf(t, env, firstIssue(t, env, shop.ID))
	if len(got.BeforeError) != 1 || got.BeforeError[0].Route != "/mine" {
		t.Errorf("before the error: %s; want only /mine", stepsText(got.BeforeError))
	}
}

// When the latest event has no user id, there is no journey (the card is
// hidden), even though an older event of the issue had one.
func TestJourney_NoUserIDEmpty(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	env.visits(t, shop.ID,
		visit{user: "42", route: "/cart", at: testNow.Add(-3 * time.Minute), stay: time.Minute},
		visit{user: "", route: "/checkout", at: testNow.Add(-time.Minute), stay: time.Minute},
	)
	env.save(t, shop.ID,
		phpEvent(t, eventSpec{id: hexID(1), userID: "42", at: testNow.Add(-2 * time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(2)}),
	)

	got := journeyOf(t, env, firstIssue(t, env, shop.ID))
	if len(got.BeforeError) != 0 {
		t.Errorf("before the error: %s; want nothing without a user id", stepsText(got.BeforeError))
	}
}

// Ruling 41: the error's time is corrected to the server's clock with the
// envelope's sent_at, as page views' times are, so a browser whose clock
// is 5 minutes behind or ahead still gets exactly the pages before the
// error; the user activity puts the error between the same pages.
func TestJourney_ClientClockOff(t *testing.T) {
	for _, skew := range []time.Duration{-5 * time.Minute, 5 * time.Minute} {
		t.Run(fmt.Sprint("clock off by ", skew), func(t *testing.T) {
			env := newTestEnv(t)
			shop := env.pageProject(t, "Webshop")
			// By the server's clock the error happened a second before it arrived (testNow).
			errorAt := testNow.Add(-time.Second)
			env.visits(t, shop.ID,
				visit{user: "42", route: "/too-early", at: errorAt.Add(-31 * time.Minute), stay: time.Minute},
				visit{user: "42", route: "/products/:slug", at: errorAt.Add(-4 * time.Minute), stay: 100 * time.Second},
				visit{user: "42", route: "/cart", at: errorAt.Add(-2 * time.Minute), stay: 35 * time.Second},
				visit{user: "42", route: "/checkout", at: errorAt.Add(-80 * time.Second)},
				// Opened after the error (within the clock's error).
				visit{user: "42", route: "/thanks", at: errorAt.Add(30 * time.Second)},
			)
			// The browser's clock is off by skew: its timestamp and its sent_at both.
			env.saveSent(t, shop.ID, testNow.Add(skew),
				phpEvent(t, eventSpec{id: hexID(1), userID: "42", url: "https://shop.example.com/checkout", at: errorAt.Add(skew)}))

			got := journeyOf(t, env, firstIssue(t, env, shop.ID))
			if text := stepsText(got.BeforeError); len(got.BeforeError) != 3 || got.BeforeError[0].Route != "/products/:slug" ||
				got.BeforeError[2].Route != "/checkout" {
				t.Errorf("before the error: %s; want /products/:slug, /cart, /checkout", text)
			}
			activity := getJSON[activityResponse](t, env, activityPath(shop.ID, "42", ""))
			if text := itemsText(activity.Items); text != "pageview:/thanks error:RuntimeException pageview:/checkout pageview:/cart pageview:/products/:slug pageview:/too-early" {
				t.Errorf("activity = %s", text)
			}
		})
	}
}

// The window is the 30 minutes up to the error: a page opened exactly 30
// minutes before it is in, one opened a millisecond earlier is not.
func TestJourney_ThirtyMinuteEdge(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	errorAt := testNow.Add(-5 * time.Minute)
	env.visits(t, shop.ID,
		visit{user: "42", route: "/just-before-the-window", at: errorAt.Add(-30*time.Minute - time.Millisecond)},
		visit{user: "42", route: "/window-start", at: errorAt.Add(-30 * time.Minute)},
		visit{user: "42", route: "/at-the-error", at: errorAt},
	)
	env.save(t, shop.ID, phpEvent(t, eventSpec{id: hexID(1), userID: "42", at: errorAt}))

	got := journeyOf(t, env, firstIssue(t, env, shop.ID))
	if len(got.BeforeError) != 2 || got.BeforeError[0].Route != "/window-start" || got.BeforeError[1].Route != "/at-the-error" {
		t.Errorf("before the error: %s; want /window-start and /at-the-error", stepsText(got.BeforeError))
	}
}

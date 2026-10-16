package store

import (
	"context"
	"errors"
	"testing"
)

func TestIssueByID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p, _ := db.CreateProject(ctx, NewProject{Name: "Webshop"})
	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(1), "fp", at(0))})
	if err != nil || len(res.NewIssues) != 1 {
		t.Fatalf("SaveErrorEvents: %+v %v", res, err)
	}

	got, err := db.IssueByID(ctx, res.NewIssues[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != res.NewIssues[0] {
		t.Errorf("IssueByID = %+v, want %+v", got, res.NewIssues[0])
	}
	if _, err := db.IssueByID(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
}

// The search ignores the case of ASCII letters only, on both databases
// (SQLite's LOWER; Postgres' LOWER would also fold É): "ÉCHEC" and "É"
// find "Échec", "é" does not; "REFUSÉ" finds "REFUSÉ", "refusé" does not.
// "%" and "_" are literal.
func TestListIssuePage_SearchFoldsASCIIOnly(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	ev := newEvent(p.ID, eventID(1), "fp", at(0))
	ev.Title, ev.Message = "PaymentException", "Échec du paiement: 100% REFUSÉ"
	if _, err := db.SaveErrorEvents(ctx, []NewErrorEvent{ev}); err != nil {
		t.Fatal(err)
	}
	for search, want := range map[string]int{"ÉCHEC": 1, "É": 1, "é": 0, "paymentexception": 1, "100%": 1, "1_0": 0, "REFUSÉ": 1, "refusé": 0} {
		rows, err := db.ListIssuePage(ctx, IssueFilter{ProjectID: p.ID, Status: IssueOpen, Search: search, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != want {
			t.Errorf("search %q found %d issues, want %d", search, len(rows), want)
		}
	}
}

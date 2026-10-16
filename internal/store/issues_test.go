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

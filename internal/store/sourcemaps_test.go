package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAPITokens_CreateFindTouchDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	admin := addUser(t, db, "admin@example.com", RoleAdmin)
	shop := addProject(t, db, "Webshop")
	portal := addProject(t, db, "Portal")

	tok, err := db.CreateAPIToken(ctx, NewAPIToken{Name: " CI uploads ", TokenHash: "hash-1", Hint: "abcd",
		ProjectIDs: []int64{portal.ID, shop.ID, shop.ID}, CreatedBy: admin.ID, Now: userTestNow})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != "CI uploads" || !reflect.DeepEqual(tok.ProjectIDs, []int64{shop.ID, portal.ID}) || tok.LastUsedAt != nil {
		t.Errorf("created = %+v", tok)
	}

	found, err := db.APITokenByHash(ctx, "hash-1")
	if err != nil || found.ID != tok.ID || !reflect.DeepEqual(found.ProjectIDs, tok.ProjectIDs) || found.Hint != "abcd" {
		t.Errorf("by hash = %+v, %v", found, err)
	}
	if _, err := db.APITokenByHash(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown hash: %v, want ErrNotFound", err)
	}

	// Last use is written at most once a minute.
	if err := db.TouchAPIToken(ctx, tok.ID, userTestNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchAPIToken(ctx, tok.ID, userTestNow.Add(time.Hour+30*time.Second)); err != nil {
		t.Fatal(err)
	}
	found, _ = db.APITokenByHash(ctx, "hash-1")
	if found.LastUsedAt == nil || !found.LastUsedAt.Equal(userTestNow.Add(time.Hour)) {
		t.Errorf("last used = %v, want the first use within the minute", found.LastUsedAt)
	}

	// A deleted project leaves the token with the others.
	if err := db.DeleteProject(ctx, portal.ID); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListAPITokens(ctx)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0].ProjectIDs, []int64{shop.ID}) {
		t.Errorf("list = %+v, %v", list, err)
	}

	if err := db.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.APITokenByHash(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted token still found: %v", err)
	}
	if err := db.DeleteAPIToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: %v, want ErrNotFound", err)
	}
}

func TestAPITokens_OnlyAdminsAndExistingProjects(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	addUser(t, db, "admin@example.com", RoleAdmin)
	shop := addProject(t, db, "Webshop")
	member := addUser(t, db, "member@example.com", RoleMember, shop.ID)

	_, err := db.CreateAPIToken(ctx, NewAPIToken{Name: "x", TokenHash: "h", ProjectIDs: []int64{shop.ID}, CreatedBy: member.ID, Now: userTestNow})
	if !errors.Is(err, ErrNotAdmin) {
		t.Errorf("by a member: %v, want ErrNotAdmin", err)
	}
	_, err = db.CreateAPIToken(ctx, NewAPIToken{Name: "x", TokenHash: "h", ProjectIDs: []int64{999}, CreatedBy: firstAdmin(t, db), Now: userTestNow})
	if !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: %v, want ErrUnknownProject", err)
	}
}

func TestSourceMapFiles_SaveFindAndExpire(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	shop := addProject(t, db, "Webshop")
	day := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	bundle := []SourceMapFile{
		{ProjectID: shop.ID, Release: "shop@1.0.0", File: "~/assets/index-abc.js", Kind: MinifiedSourceKind,
			DebugID: "C5BC0F5B-2A6F-50F4-AAFE-19E667444D31", Sourcemap: "index-abc.js.map", Bundle: "b1", Path: "1/a", Size: 10, UploadedAt: day},
		{ProjectID: shop.ID, Release: "shop@1.0.0", File: "~/assets/index-abc.js.map", Kind: SourceMapKind,
			DebugID: "c5bc0f5b-2a6f-50f4-aafe-19e667444d31", Bundle: "b1", Path: "1/b", Size: 20, UploadedAt: day},
	}
	stored, err := db.SaveSourceMapFiles(ctx, bundle)
	if err != nil || len(stored) != 2 {
		t.Fatalf("save = %d files, %v", len(stored), err)
	}
	// The same bundle again (two uploads at once) stores nothing.
	if again, err := db.SaveSourceMapFiles(ctx, bundle); err != nil || len(again) != 0 {
		t.Errorf("second save = %d files, %v", len(again), err)
	}

	m, err := db.SourceMapByDebugID(ctx, shop.ID, "C5BC0F5B-2A6F-50F4-AAFE-19E667444D31", SourceMapKind)
	if err != nil || m.Path != "1/b" || m.DebugID != "c5bc0f5b-2a6f-50f4-aafe-19e667444d31" {
		t.Errorf("by debug id = %+v, %v", m, err)
	}
	min, err := db.SourceMapByFile(ctx, shop.ID, "shop@1.0.0", MinifiedSourceKind, "http://shop.test/assets/index-abc.js", "~/assets/index-abc.js")
	if err != nil || min.Sourcemap != "index-abc.js.map" {
		t.Errorf("by file = %+v, %v", min, err)
	}
	if _, err := db.SourceMapByFile(ctx, shop.ID, "shop@2.0.0", MinifiedSourceKind, "~/assets/index-abc.js"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other release: %v, want ErrNotFound", err)
	}

	// Uploading the bundle again moves its upload time, which retention counts from.
	later := day.Add(48 * time.Hour)
	if has, err := db.HasSourceMapBundle(ctx, shop.ID, "b1", later); err != nil || !has {
		t.Errorf("has bundle = %v, %v", has, err)
	}
	if has, _ := db.HasSourceMapBundle(ctx, shop.ID, "b2", later); has {
		t.Error("unknown bundle reported as stored")
	}
	old, err := db.SourceMapsUploadedBefore(ctx, shop.ID, day.Add(24*time.Hour), 10)
	if err != nil || len(old) != 0 {
		t.Errorf("before the new upload time = %d files, %v", len(old), err)
	}
	old, _ = db.SourceMapsUploadedBefore(ctx, shop.ID, later.Add(time.Minute), 1)
	if len(old) != 1 {
		t.Fatalf("limit 1 gave %d files", len(old))
	}
	if n, err := db.DeleteSourceMapFiles(ctx, []int64{old[0].ID}); err != nil || n != 1 {
		t.Errorf("delete = %d, %v", n, err)
	}
	paths, err := db.SourceMapPaths(ctx)
	if err != nil || len(paths) != 1 {
		t.Errorf("paths left = %v, %v", paths, err)
	}

	if _, err := db.SaveSourceMapFiles(ctx, []SourceMapFile{{ProjectID: 999, File: "x", Kind: SourceMapKind, Bundle: "b3", Path: "p"}}); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: %v, want ErrUnknownProject", err)
	}
}

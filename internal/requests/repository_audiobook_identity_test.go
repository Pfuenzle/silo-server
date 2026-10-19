package requests

import "testing"

// Audiobook metadata IDs have no TMDB counterpart. Reading one must not make
// the account's entire mixed-media request list fail or discard its identity.
func TestRequestListsWithAudiobookIdentityDatabase(t *testing.T) {
	repo, pool := lifecycleTestRepository(t)
	ctx := t.Context()
	insertLifecycleRequest(t, repo, "movie", 1, 949, StatusPending)
	for _, row := range []struct {
		id       string
		userID   int
		provider string
	}{
		{"audiobook", 1, "book-1"},
		{"other-account", 2, "book-2"},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO media_requests (
			id, provider, provider_item_id, media_type, tmdb_id, title,
			status, outcome, requested_by_user_id, requested_by_profile_id
		) VALUES ($1, 'audiobook-metadata', $2, 'audiobook', NULL, 'Example audiobook',
			'pending', 'active', $3, 'profile')`, row.id, row.provider, row.userID); err != nil {
			t.Fatal(err)
		}
	}
	assertIdentity := func(req *Request) {
		t.Helper()
		if req.Provider != "audiobook-metadata" || req.ProviderItemID != "book-1" || req.MediaType != MediaTypeAudiobook || req.TMDBID != 0 {
			t.Fatalf("audiobook identity = %q/%q/%q/%d", req.Provider, req.ProviderItemID, req.MediaType, req.TMDBID)
		}
	}

	mine, err := repo.ListMine(ctx, 1, ListFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list mixed-media requests: %v", err)
	}
	if len(mine) != 2 {
		t.Fatalf("mine count = %d, want 2", len(mine))
	}
	seen := map[string]bool{}
	for _, req := range mine {
		seen[req.ID] = true
		switch req.ID {
		case "audiobook":
			assertIdentity(req)
		case "movie":
			if req.TMDBID != 949 || req.Provider != "tmdb" || req.ProviderItemID != "" {
				t.Fatalf("movie identity = %q/%q/%d", req.Provider, req.ProviderItemID, req.TMDBID)
			}
		default:
			t.Fatalf("unexpected request %q in account's list", req.ID)
		}
	}
	if !seen["audiobook"] || !seen["movie"] {
		t.Fatalf("mine IDs = %v, want audiobook and movie", seen)
	}
	admin, err := repo.ListAdmin(ctx, ListFilter{Limit: 100})
	if err != nil || len(admin) != 3 {
		t.Fatalf("admin list count = %d, err = %v; want 3", len(admin), err)
	}
	stored, err := repo.GetRequest(ctx, "audiobook")
	if err != nil {
		t.Fatalf("read audiobook request: %v", err)
	}
	assertIdentity(stored)
	withdrawn, err := repo.SetOutcome(ctx, "audiobook", guardPending, OutcomeCancelled, Viewer{UserID: 1, ProfileID: "profile"}, "")
	if err != nil {
		t.Fatalf("withdraw audiobook request: %v", err)
	}
	assertIdentity(withdrawn)
	if withdrawn.Outcome != OutcomeCancelled {
		t.Fatalf("withdrawn outcome = %q", withdrawn.Outcome)
	}
}

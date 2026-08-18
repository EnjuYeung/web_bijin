package main

import "testing"

func TestGroupAlbumsUsesDirectFolderAndNewestCover(t *testing.T) {
	photos := []photo{
		{ID: 1, RelPath: "loose.jpg", MtimeUnix: 10},
		{ID: 2, RelPath: "2025/旅行/a.jpg", MtimeUnix: 20},
		{ID: 3, RelPath: "2026/旅行/b.jpg", MtimeUnix: 30},
		{ID: 4, RelPath: "2026/旅行/c.jpg", MtimeUnix: 40},
		{ID: 5, RelPath: "2026/旅行/deep/d.jpg", MtimeUnix: 50},
	}

	albums := groupAlbums(photos)
	if len(albums) != 4 {
		t.Fatalf("want 4 albums, got %d: %+v", len(albums), albums)
	}
	byID := make(map[string]albumSummary)
	for _, a := range albums {
		byID[a.ID] = a
	}
	if root := byID[rootAlbumID]; root.Name != "根目录" || root.Count != 1 || root.Cover.ID != 1 {
		t.Fatalf("root album %+v", root)
	}
	if trip := byID["2026/旅行"]; trip.Name != "旅行" || trip.Count != 2 || trip.Cover.ID != 4 {
		t.Fatalf("trip album %+v", trip)
	}
	if deep := byID["2026/旅行/deep"]; deep.Name != "deep" || deep.Count != 1 || deep.Cover.ID != 5 {
		t.Fatalf("deep album %+v", deep)
	}
	if byID["2025/旅行"].Name != byID["2026/旅行"].Name {
		t.Fatal("same folder names should remain separate albums with the same display name")
	}

	filtered := filterAlbum(photos, "2026/旅行")
	if len(filtered) != 2 || filtered[0].ID != 3 || filtered[1].ID != 4 {
		t.Fatalf("filtered %+v", filtered)
	}
}

func TestValidAlbumID(t *testing.T) {
	for _, good := range []string{".", "旅行", "2026/旅行", "space name"} {
		if got, ok := validAlbumID(good); !ok || got != good {
			t.Fatalf("valid %q => %q %v", good, got, ok)
		}
	}
	for _, bad := range []string{"", "/photos", "../etc", "a/../b", "a//b", ".hidden", "a\\b"} {
		if _, ok := validAlbumID(bad); ok {
			t.Fatalf("accepted invalid album %q", bad)
		}
	}
}

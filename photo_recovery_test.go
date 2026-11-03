package main

import (
	"context"
	"image/color"
	"testing"
)

func TestRetryRejectedVersion(t *testing.T) {
	f, st, _, sc := s3Harness(t, 2, nil)
	ctx := context.Background()
	targets := map[string]photoRecoveryEntry{}
	for _, name := range []string{"recover.jpg", "untouched.jpg", "changed.jpg"} {
		f.put(name, fixtureJPEG(color.RGBA{1, 2, 3, 255}))
		match := sc.sources.match("jan", name)[0]
		object, err := sc.sources.Stat(ctx, match.key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.upsert(photo{SourceKey: match.key, RelPath: name, Broken: true, SourceVersion: object.Version, InvalidReason: "test reason"}); err != nil {
			t.Fatal(err)
		}
		targets[name] = photoRecoveryEntry{Key: match.key, Version: object.Version}
	}
	entry := targets["recover.jpg"]
	original, _, _ := st.getBySourceKey(entry.Key)
	if result := sc.photos.SyncKey(ctx, entry.Key); !result.Invalid || result.Err != nil || result.InvalidReason != "test reason" {
		t.Fatalf("cached rejection: %+v", result)
	}
	f.put("changed.jpg", fixtureJPEG(color.RGBA{80, 20, 120, 255}))
	if result, attempted := sc.photos.retryRejected(ctx, targets["changed.jpg"]); attempted {
		t.Fatalf("recovery tried changed object: %+v", result)
	}
	if result, attempted := sc.photos.retryRejected(ctx, entry); !attempted || result.Err != nil || !result.AlbumUsable || !result.WallpapersReady {
		t.Fatalf("recovery result: %+v, attempted=%v", result, attempted)
	}
	recovered, _, _ := st.getBySourceKey(entry.Key)
	if recovered.ID != original.ID || recovered.SourceVersion != original.SourceVersion || recovered.Broken || recovered.InvalidReason != "" {
		t.Fatalf("recovered state: %+v", recovered)
	}
	if _, attempted := sc.photos.retryRejected(ctx, entry); attempted {
		t.Fatal("already recovered entry should be skipped")
	}
	if untouched, _, _ := st.getBySourceKey(targets["untouched.jpg"].Key); !untouched.Broken {
		t.Fatal("unlisted rejection was modified")
	}
}

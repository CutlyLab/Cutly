package cutly

import (
	"context"
	"errors"
	"testing"

	"github.com/CutlyLab/Cutly/core/videoedit"
)

func TestCreateLocalProjectV1IsIdempotentAndInspectable(t *testing.T) {
	dir := t.TempDir()
	document := localProjectFixtureDocument()
	created, err := CreateLocalProjectV1(context.Background(), dir, document)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := created.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := videoedit.DocumentHash(document)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Document.ID != document.ID || snapshot.DocumentHash != wantHash || snapshot.PendingPlans == nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	replayed, err := CreateLocalProjectV1(context.Background(), dir, document)
	if err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	if _, err := replayed.Inspect(context.Background()); err != nil {
		t.Fatal(err)
	}

	different := document
	different.Revision++
	if _, err := CreateLocalProjectV1(context.Background(), dir, different); !errors.Is(err, ErrLocalProjectExists) {
		t.Fatalf("different project error = %v", err)
	}

	opened, err := OpenLocalProjectV1(dir)
	if err != nil {
		t.Fatal(err)
	}
	if openedSnapshot, err := opened.Inspect(context.Background()); err != nil || openedSnapshot.DocumentHash != wantHash {
		t.Fatalf("opened snapshot = %+v, error = %v", openedSnapshot, err)
	}
}

func TestLocalProjectV1NilHandleFailsClosed(t *testing.T) {
	var project *LocalProjectV1
	if _, err := project.Inspect(context.Background()); !errors.Is(err, ErrLocalProjectUnavailable) {
		t.Fatalf("nil project error = %v", err)
	}
}

func localProjectFixtureDocument() DocumentV1 {
	duration := videoedit.RationalTime{Value: 1, Rate: 1}
	return DocumentV1{
		SchemaVersion: videoedit.SchemaVersion,
		ID:            "project-document",
		Revision:      1,
		Assets: []videoedit.Asset{{
			ID: "video", Kind: videoedit.AssetKindVideo, URI: "video.mp4",
			ContentHash: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Duration:    &duration,
		}},
		Timelines: []videoedit.Timeline{{
			ID: "timeline",
			Tracks: []videoedit.Track{{
				ID: "video-track", Kind: videoedit.TrackKindVideo,
				Clips: []videoedit.Clip{{
					ID: "video-clip", AssetID: "video",
					TimelineRange: videoedit.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: duration},
					SourceRange:   videoedit.TimeRange{Start: videoedit.RationalTime{Value: 0, Rate: 1}, Duration: duration},
				}},
			}},
		}},
	}
}

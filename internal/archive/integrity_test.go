package archive

import (
	"errors"
	"testing"
)

func TestValidateVideoFileMetadataAcceptsValidStates(t *testing.T) {
	t.Parallel()

	for _, metadata := range []VideoFileMetadata{
		{State: VideoStateCatalogOnly, ThumbnailPath: "thumbs/catalog.jpg"},
		{State: VideoStateDownloaded, MediaPath: "videos/sample.mp4", ThumbnailPath: "thumbs/sample.jpg", SubtitlePaths: []string{"subtitles/sample.en.vtt"}},
		{State: VideoStateMissing, MediaPath: "videos/missing.mp4"},
		{State: VideoStateFailed},
		{State: VideoStatePartial},
	} {
		if err := ValidateVideoFileMetadata(metadata); err != nil {
			t.Fatalf("expected %#v to be valid, got %v", metadata, err)
		}
	}
}

func TestVideoStateStringsMatchDocumentedNames(t *testing.T) {
	t.Parallel()

	states := map[VideoState]string{
		VideoStateCatalogOnly: "catalog-only",
		VideoStateDownloaded:  "downloaded",
		VideoStateMissing:     "missing",
		VideoStateFailed:      "failed",
		VideoStatePartial:     "partial",
	}
	for state, expected := range states {
		if string(state) != expected {
			t.Fatalf("expected state %q to render as %q", state, expected)
		}
	}
}

func TestValidateVideoFileMetadataRejectsInvalidStates(t *testing.T) {
	t.Parallel()

	for _, metadata := range []VideoFileMetadata{
		{State: ""},
		{State: "unknown"},
		{State: VideoStateCatalogOnly, MediaPath: "videos/not-downloaded.mp4"},
		{State: VideoStateDownloaded},
		{State: VideoStateDownloaded, MediaPath: "../secret.mp4"},
		{State: VideoStateDownloaded, MediaPath: "/tmp/secret.mp4"},
		{State: VideoStateDownloaded, MediaPath: "videos/./sample.mp4"},
		{State: VideoStateDownloaded, MediaPath: "videos//sample.mp4"},
		{State: VideoStateDownloaded, MediaPath: "videos/"},
		{State: VideoStateDownloaded, MediaPath: "videos/sample.mp4", ThumbnailPath: "thumbs/../secret.jpg"},
		{State: VideoStateDownloaded, MediaPath: "videos/sample.mp4", SubtitlePaths: []string{"/tmp/subtitle.vtt"}},
		{State: VideoStatePartial, MediaPath: "videos/partial.mp4"},
		{State: VideoStateFailed, MediaPath: "videos/failed.mp4"},
	} {
		if err := ValidateVideoFileMetadata(metadata); !errors.Is(err, ErrInvalidIntegrityState) {
			t.Fatalf("expected invalid integrity state for %#v, got %v", metadata, err)
		}
	}
}

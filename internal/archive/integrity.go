package archive

import (
	"errors"

	"kapsel/internal/assetpath"
)

var ErrInvalidIntegrityState = errors.New("invalid archive integrity state")

type VideoState string

const (
	VideoStateCatalogOnly VideoState = "catalog-only"
	VideoStateDownloaded  VideoState = "downloaded"
	VideoStateMissing     VideoState = "missing"
	VideoStateFailed      VideoState = "failed"
	VideoStatePartial     VideoState = "partial"
)

type VideoFileMetadata struct {
	State         VideoState
	MediaPath     string
	ThumbnailPath string
	SubtitlePaths []string
	PreviewPath   string
}

func ValidateVideoFileMetadata(metadata VideoFileMetadata) error {
	if err := validateOptionalPath(metadata.ThumbnailPath); err != nil {
		return err
	}
	if err := validateOptionalPath(metadata.PreviewPath); err != nil {
		return err
	}
	for _, subtitlePath := range metadata.SubtitlePaths {
		if err := validateOptionalPath(subtitlePath); err != nil {
			return err
		}
	}

	switch metadata.State {
	case VideoStateCatalogOnly:
		if metadata.MediaPath != "" {
			return ErrInvalidIntegrityState
		}
	case VideoStateDownloaded:
		if err := validateRequiredPath(metadata.MediaPath); err != nil {
			return err
		}
	case VideoStateMissing:
		if err := validateOptionalPath(metadata.MediaPath); err != nil {
			return err
		}
	case VideoStateFailed, VideoStatePartial:
		if metadata.MediaPath != "" {
			return ErrInvalidIntegrityState
		}
	default:
		return ErrInvalidIntegrityState
	}

	return nil
}

func validateRequiredPath(raw string) error {
	if raw == "" {
		return ErrInvalidIntegrityState
	}

	return validateOptionalPath(raw)
}

func validateOptionalPath(raw string) error {
	if raw == "" {
		return nil
	}
	cleaned, err := assetpath.Clean(raw)
	if err != nil || cleaned != raw {
		return ErrInvalidIntegrityState
	}

	return nil
}

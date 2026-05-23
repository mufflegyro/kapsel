package meta

import (
	"os"
	"strings"
	"testing"
)

func TestMVPHouseholdArchiveAcceptanceChecklist(t *testing.T) {
	body, err := os.ReadFile("mvp_acceptance.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	for _, required := range []string{
		"# MVP Household Archive Acceptance Test",
		"## v1.0 Acceptance Path",
		"## Deferred After v1.0",
		"## Smoke Test Mapping",
		"issues/build-settings-and-first-run-readiness-ui.md",
		"issues/add-local-authentication-and-session-protection.md",
		"issues/add-direct-video-download-flow.md",
		"issues/persist-playback-progress-from-the-web-player.md",
		"issues/hydrate-search-results-with-archive-records.md",
		"issues/sync-channel-video-catalog-metadata.md",
		"issues/add-backup-and-restore-workflow.md",
		"issues/add-browser-end-to-end-smoke-tests.md",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("expected MVP acceptance checklist to contain %q", required)
		}
	}

	for _, required := range []string{
		"first-run setup",
		"configuration readiness",
		"authentication",
		"direct video",
		"channel",
		"watch",
		"resume",
		"search",
		"catalog-only",
		"restart",
		"backup",
		"restore",
		"failure recovery",
		"without live youtube network calls",
	} {
		if !strings.Contains(strings.ToLower(text), required) {
			t.Fatalf("expected MVP acceptance checklist to cover %q", required)
		}
	}
}

func TestArchiveIntegrityInvariantsDocument(t *testing.T) {
	body, err := os.ReadFile("archive_integrity.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	for _, required := range []string{
		"# Archive Integrity Invariants",
		"## Video States",
		"## Asset Ownership",
		"## Idempotency Expectations",
		"## Validation Coverage",
		"issues/harden-download-path-and-metadata-validation.md",
		"issues/make-download-ingestion-atomic-and-idempotent.md",
		"issues/add-storage-maintenance-and-orphan-cleanup.md",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("expected archive integrity document to contain %q", required)
		}
	}

	for _, required := range []string{
		"downloaded",
		"catalog-only",
		"missing",
		"failed",
		"partial",
		"media",
		"thumbnail",
		"subtitle",
		"comment",
		"derived preview",
		"configured storage roots",
	} {
		if !strings.Contains(strings.ToLower(text), required) {
			t.Fatalf("expected archive integrity document to cover %q", required)
		}
	}
}

# Polish catalog media availability states

## Summary

Make it clearer when Kapsel has a playable local media file versus only catalog metadata, especially on the home feed and watch page.

## Requirements

- Show catalog-only and locally playable states clearly on video cards without disrupting the existing dark/warm visual language.
- Use watch-page copy that distinguishes metadata archival from downloaded media availability.
- Make the catalog-only watch-page primary action point to downloading the video.
- Keep retention/protection controls available without competing with the main download action.
- Reduce visual weight for empty description and comments sections when a catalog-only video has no imported content.

## Acceptance Criteria

- Home/library video cards make catalog-only videos visually distinct from locally playable videos.
- A catalog-only watch page does not use ambiguous copy such as "Archived locally" to imply the media file exists.
- The catalog-only watch page has a clearly primary "Download video" action and a secondary retention/protection action.
- Empty description and comments areas on catalog-only pages are compact or collapsed by default.
- Browser coverage or a documented manual screenshot check verifies the catalog-only and locally playable states.

## Notes

- UI review priority: answer the user's core archive question, "Do I have this video locally, or only its metadata?"
- Consider copy such as "Metadata only - no media file downloaded yet" and "Metadata archived - media not downloaded".
- Consider whether "Keep forever" should be renamed or explained as protection from auto-cleanup.

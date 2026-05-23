# Reset scroll on in-app navigation

## Summary

Reset the window scroll position when users navigate between routes inside the Svelte app so a video detail page does not open halfway down after clicking from a scrolled library view.

## Requirements

- Scroll to the top for in-app route changes triggered by links or controls.
- Keep browser back and forward navigation behavior intact.
- Avoid resetting scroll when the requested route does not actually change.

## Acceptance Criteria

- Browser coverage verifies clicking a video from a scrolled library view opens the video detail at the top.
- Existing browser smoke tests continue to pass.

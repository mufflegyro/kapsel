# Install Deno for yt-dlp

## Summary

The deployed Kapsel LXC logs show yt-dlp warning that YouTube extraction without a JavaScript runtime is deprecated, and that Deno is enabled by default.

## Requirements

- Install Deno in the Kapsel LXC so yt-dlp can use a supported JavaScript runtime.
- Verify the `kapsel` service user can run Deno.
- Verify yt-dlp no longer reports the missing JavaScript runtime warning for a simulated YouTube extraction.
- Investigate what the `en-zh-Hant` subtitle language code means in the failed yt-dlp subtitle download.

## Acceptance Criteria

- `deno --version` succeeds inside CT 119 as the `kapsel` user.
- A yt-dlp simulate check runs with Deno available.
- The meaning of `en-zh-Hant` is documented in the result.

## Notes

- This does not address YouTube HTTP 429 rate limiting or impersonation-target warnings.
- `en-zh-Hant` was confirmed with `yt-dlp --list-subs` as `English from Chinese (Traditional)`, an auto-translated caption track matched by Kapsel's broad `en.*` subtitle selector.

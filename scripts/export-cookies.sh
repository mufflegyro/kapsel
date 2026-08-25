#!/usr/bin/env bash
set -euo pipefail

# Export YouTube cookies from a browser into the Netscape cookies.txt format
# that Kapsel passes to yt-dlp via KAPSEL_YTDLP_COOKIES_FILE.
#
# Why: YouTube's "Sign in to confirm you're not a bot" check blocks anonymous
# downloads. Authenticated cookies make yt-dlp look like a signed-in browser.
#
# Usage:
#   ./scripts/export-cookies.sh <browser> [output-path]
#
# Browsers: chrome, chromium, edge, firefox, safari
#   - chromium-family and edge read from the installed profile directly.
#   - firefox and safari need to be QUIT first (their cookie DBs are locked),
#     and firefox needs the "cookies.txt" add-on exporting to
#     ~/Downloads/cookies.txt first.
#
# Output: defaults to ./youtube.cookies.txt (gitignored). Point
# KAPSEL_YTDLP_COOKIES_FILE at the absolute path.

BROWSER="${1:-}"
OUT="${2:-youtube.cookies.txt}"

if [[ -z "$BROWSER" ]]; then
  echo "usage: $0 <browser> [output-path]" >&2
  echo "browsers: chrome chromium edge firefox safari" >&2
  exit 2
fi

PYTHON="$(command -v python3 || true)"
if [[ -z "$PYTHON" ]]; then
  echo "error: python3 required for browser cookie extraction" >&2
  exit 1
fi

case "$BROWSER" in
  chrome)
    PROFILE="${CHROME_PROFILE:-$HOME/Library/Application Support/Google/Chrome/Default}"
    ;;
  chromium)
    PROFILE="${CHROMIUM_PROFILE:-$HOME/Library/Application Support/Chromium/Default}"
    ;;
  edge)
    PROFILE="${EDGE_PROFILE:-$HOME/Library/Application Support/Microsoft Edge/Default}"
    ;;
  firefox)
    # firefox keeps cookies in a locked sqlite db; the cookies.txt add-on
    # exports to ~/Downloads/cookies.txt. FireFox must be quit.
    SRC="${FIREFOX_COOKIES:-$HOME/Downloads/cookies.txt}"
    if [[ ! -f "$SRC" ]]; then
      echo "error: $SRC not found. Install the cookies.txt add-on, export, and quit Firefox." >&2
      exit 1
    fi
    cp "$SRC" "$OUT"
    echo "cookies exported to $(pwd)/$OUT"
    exit 0
    ;;
  safari)
    # safari stores cookies in a binary plist; no clean CLI export. The
    # recommended path is to use the cookies.txt add-on / a chromium browser.
    echo "error: safari has no supported cookie export. Use chrome/edge/firefox instead." >&2
    exit 2
    ;;
  *)
    echo "error: unknown browser $BROWSER" >&2
    exit 2
    ;;
esac

"$PYTHON" - "$PROFILE" "$OUT" << 'PY'
import os, sys, sqlite3, time, shutil, tempfile

profile, out = sys.argv[1], sys.argv[2]
db_path = os.path.join(profile, "Cookies")
if not os.path.exists(db_path):
    sys.exit(f"error: no Cookies db at {db_path}. Check the profile path or close the browser.")

# Chrome locks the cookies db; copy it to a temp file first.
tmp = tempfile.mktemp(suffix=".db")
shutil.copy2(db_path, tmp)
try:
    conn = sqlite3.connect(tmp)
    rows = conn.execute(
        "SELECT host_key, path, is_secure, expires_utc, name, value, "
        "encrypted_value, has_expires FROM cookies "
        "WHERE host_key LIKE '%youtube.com%' OR host_key LIKE '%google.com%'"
    ).fetchall()
finally:
    conn.close()
    os.remove(tmp)

# On macOS the cookie values are encrypted with the system keychain; yt-dlp's
# own browser extraction decrypts them. Writing raw encrypted bytes here is
# useless, so prefer decryption via the keychain (needs the 'browser_cookie3'
# python package, or the platform security binary).
try:
    import browser_cookie3  # noqa
    jar = browser_cookie3.chrome(domain_name="youtube.com")
    lines = []
    for c in jar:
        lines.append("\t".join([
            c.domain, "TRUE" if c.domain.startswith(".") else "FALSE",
            c.path, "TRUE" if c.secure else "FALSE",
            str(int(c.expires)) if c.expires else "0",
            c.name, c.value,
        ]))
    with open(out, "w") as f:
        f.write("# Netscape HTTP Cookie File\n")
        f.write("\n".join(lines) + "\n")
    print(f"cookies exported to {os.path.abspath(out)} ({len(lines)} entries)")
except ImportError:
    sys.exit(
        "error: cookie values are encrypted. Install browser_cookie3 "
        "(pip3 install browser_cookie3) and retry, or export cookies.txt "
        "from Firefox and use 'firefox' mode."
    )
PY

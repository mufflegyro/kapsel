#!/usr/bin/env python3
"""Re-link intact media files into the rebuilt Kapsel catalog.

After the catalog was rebuilt from subscriptions.csv (channel scans), video
rows for subscribed channels exist again but their media_path/thumbnail_path
are empty even though the downloaded files are still on disk. Videos that were
downloaded from non-subscribed channels (manual/direct downloads) have no row
at all, but their <id>.info.json metadata is intact.

This script, per video:
  1. Reads <id>.info.json left by yt-dlp.
  2. If the video row is missing, creates it from the metadata.
  3. If a complete media file exists, points media_path (and thumbnail) at it
     and marks media_origin='manual', media_downloaded_at, archived_at.

Usage:
    python3 scripts/relink_media.py <media-root> <kapsel.db>

Idempotent: safe to run repeatedly; only creates rows that are missing and
only updates rows that have a matching info.json + media file. Never deletes
anything.
"""
import json
import os
import re
import sqlite3
import sys
from datetime import datetime, timezone

YOUTUBE_SOURCE = "youtube"
ID_PATTERN = re.compile(r"^[A-Za-z0-9_-]{11}$")


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: relink_media.py <media-root> <kapsel.db>", file=sys.stderr)
        return 2
    media_root, db_path = sys.argv[1], sys.argv[2]

    if not os.path.isdir(media_root):
        print(f"media root not found: {media_root}", file=sys.stderr)
        return 1

    db = sqlite3.connect(db_path)
    db.execute("PRAGMA busy_timeout = 5000")

    linked, created, skipped = 0, 0, 0
    for name in sorted(os.listdir(media_root)):
        if not name.endswith(".info.json"):
            continue
        video_id = name[: -len(".info.json")]
        info_path = os.path.join(media_root, name)
        try:
            with open(info_path) as f:
                meta = json.load(f)
        except (OSError, json.JSONDecodeError) as exc:
            print(f"skip {video_id}: bad info.json ({exc})", file=sys.stderr)
            skipped += 1
            continue

        media_file = find_media_file(media_root, video_id, meta)
        if not media_file:
            print(f"skip {video_id}: no complete media file", file=sys.stderr)
            skipped += 1
            continue

        row = db.execute(
            "SELECT id FROM videos WHERE source = ? AND external_id = ?",
            (YOUTUBE_SOURCE, video_id),
        ).fetchone()
        if not row:
            if not create_video_row(db, video_id, meta):
                skipped += 1
                continue
            created += 1

        thumb_file = find_thumbnail(media_root, video_id)
        downloaded_at = iso_from_epoch(meta.get("epoch"))
        db.execute(
            """UPDATE videos SET
                 media_path = ?,
                 thumbnail_path = ?,
                 media_origin = 'manual',
                 media_downloaded_at = ?,
                 archived_at = COALESCE(archived_at, ?),
                 updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
               WHERE id = ?""",
            (media_file, thumb_file, downloaded_at, downloaded_at, video_id),
        )
        print(f"linked {video_id}: {media_file}")
        linked += 1

    db.commit()
    db.close()
    print(f"done: {linked} linked ({created} created), {skipped} skipped")
    return 0


def create_video_row(db: sqlite3.Connection, video_id: str, meta: dict) -> bool:
    """Insert a minimal video row from info.json. Returns False if the id is
    unusable or the row already exists (insert race)."""
    if not ID_PATTERN.match(video_id):
        print(f"skip {video_id}: not a YouTube video id", file=sys.stderr)
        return False
    title = (meta.get("title") or video_id).strip()
    if not title:
        title = video_id

    channel_id = (meta.get("channel_id") or "").strip()
    channel_name = (meta.get("channel") or "").strip()
    if channel_id:
        db.execute(
            """INSERT INTO channels (id, external_id, name, updated_at)
               VALUES (?, ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
               ON CONFLICT(id) DO UPDATE SET
                 name = CASE WHEN excluded.name <> '' THEN excluded.name ELSE channels.name END,
                 updated_at = excluded.updated_at""",
            (channel_id, channel_id, channel_name),
        )
    else:
        channel_id = ""

    published_at = upload_date_to_iso(meta.get("upload_date"))
    duration = int(meta.get("duration") or 0)
    description = (meta.get("description") or "")[:4000]
    thumbnail_url = (meta.get("thumbnail") or "")[:2048]

    try:
        db.execute(
            """INSERT OR IGNORE INTO videos
                 (id, source, external_id, channel_id, title, description,
                  published_at, duration_seconds, thumbnail_url)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)""",
            (video_id, YOUTUBE_SOURCE, video_id, channel_id, title,
             description, published_at, duration, thumbnail_url),
        )
    except sqlite3.Error as exc:
        print(f"skip {video_id}: insert failed ({exc})", file=sys.stderr)
        return False
    return True


def find_media_file(media_root: str, video_id: str, meta: dict) -> str:
    """Return the stored relative media path for a complete download, or ''."""
    candidates = [f"{video_id}.mp4", f"{video_id}.mkv", f"{video_id}.webm"]
    for cand in candidates:
        if os.path.isfile(os.path.join(media_root, cand)):
            return cand
    # yt-dlp leaves .f*.mp4 fragments for format-split downloads; only link
    # if the companion audio fragment is absent (i.e. the merge completed).
    if os.path.isfile(os.path.join(media_root, f"{video_id}.f135.mp4")) and not os.path.isfile(
        os.path.join(media_root, f"{video_id}.f140.m4a.part")
    ):
        return f"{video_id}.f135.mp4"
    return ""


def find_thumbnail(media_root: str, video_id: str) -> str:
    for ext in (".webp", ".jpg"):
        path = f"{video_id}{ext}"
        if os.path.isfile(os.path.join(media_root, path)):
            return path
    return ""


def iso_from_epoch(epoch) -> str:
    if not epoch:
        return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%fZ")
    try:
        return datetime.fromtimestamp(float(epoch), tz=timezone.utc).strftime("%Y-%m-%dT%H:%M:%fZ")
    except (TypeError, ValueError, OSError):
        return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%fZ")


def upload_date_to_iso(value) -> str:
    if not value or not isinstance(value, str) or len(value) != 8 or not value.isdigit():
        return ""
    try:
        return datetime.strptime(value, "%Y%m%d").date().isoformat()
    except ValueError:
        return ""


if __name__ == "__main__":
    sys.exit(main())

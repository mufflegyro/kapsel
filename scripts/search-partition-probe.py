#!/usr/bin/env python3
"""Live-archive search partition probe.

Walks every page of /api/search for the given queries and verifies the
two invariants the episode window must hold after the server-side owner
dedupe:

  1. Pages are disjoint: no video owner appears on two pages.
  2. Coverage is complete: unique episode owners + unique channels &
     playlists owners equals the reported distinct_owners.

The fixture-based browser smoke cannot see partition defects (its owners
carry one document each); this probe runs against a real archive where
owners match through several docs and recency/field re-ranking orders
diverge from raw BM25 order.

Usage: python3 scripts/search-partition-probe.py [BASE_URL] [QUERY...]
Defaults: http://127.0.0.1:18080, a small set of real-data queries.
"""

import json
import sys
import urllib.parse
import urllib.request

PAGE_SIZE = 50
MAX_PAGES = 220  # offset cap on the server is 10000; stay well inside it.

# Mirrors of server-side bounds (internal/search/search.go):
#   - secondaryResultCap: the channels & playlists block serves at most 8
#     owners even when more match, so distinct_owners can legitimately
#     exceed what the response exposes by up to this much when the block
#     is saturated.
#   - maxPoolDocs: episode owners whose documents all rank beyond the
#     2000-doc pool are unreachable on huge match sets (a documented,
#     offset-independent bound).
SECONDARY_RESULT_CAP = 8
MAX_POOL_DOCS = 2000


def search(base_url, query, offset):
    url = f"{base_url}/api/search?q={urllib.parse.quote(query)}&limit={PAGE_SIZE}&offset={offset}"
    with urllib.request.urlopen(url, timeout=30) as response:
        return json.load(response)


def probe(base_url, query):
    episode_pages = []
    secondary_owners = set()
    total_docs = None
    distinct_owners = None
    offset = 0
    for _ in range(MAX_PAGES):
        payload = search(base_url, query, offset)
        total_docs = payload.get("total")
        distinct_owners = payload.get("distinct_owners")
        episodes, secondary = [], []
        for row in payload.get("data", []):
            record = row.get("record") or {}
            if (record.get("type") or row.get("owner_type")) == "video":
                episodes.append(record.get("id") or row.get("owner_id"))
            else:
                secondary_owners.add(record.get("id") or row.get("owner_id"))
        episode_pages.append(episodes)
        if not episodes:
            break
        offset += len(episodes)

    seen = {}
    duplicates = []
    for page_index, page in enumerate(episode_pages):
        for owner in page:
            if owner in seen and owner not in duplicates:
                duplicates.append(owner)
            seen[owner] = page_index

    covered = len(seen) + len(secondary_owners)
    shortfall = distinct_owners - covered
    notes = []
    if shortfall > 0:
        if total_docs is not None and total_docs > MAX_POOL_DOCS:
            notes.append(
                f"shortfall {shortfall} attributable to the episode pool cap "
                f"({total_docs} matching docs > {MAX_POOL_DOCS}; the "
                f"channels & playlists cap may also contribute)"
            )
        elif len(secondary_owners) >= SECONDARY_RESULT_CAP:
            notes.append(
                f"shortfall {shortfall} attributable to the channels & playlists cap "
                f"(block saturated at {SECONDARY_RESULT_CAP})"
            )
        else:
            notes.append(f"UNEXPLAINED shortfall {shortfall}")
    unexplained = any(n.startswith("UNEXPLAINED") for n in notes)
    status = "OK" if not duplicates and not unexplained else "FAIL"
    print(
        f"[{status}] {query!r}: pages={len(episode_pages)} "
        f"episode_owners={len(seen)} secondary_owners={len(secondary_owners)} "
        f"covered={covered}/{distinct_owners} duplicates={len(duplicates)}"
    )
    for note in notes:
        print(f"       note: {note}")
    for owner in duplicates[:10]:
        print(f"       duplicate owner on pages {seen[owner]}+ : {owner}")
    return status == "OK"


def main():
    base_url = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:18080"
    queries = sys.argv[2:] or ["island", "music", "the"]
    ok = all(probe(base_url, query) for query in queries)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()

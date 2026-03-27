# Librarr Development Guidelines

## Search & Filtering Rules

These rules MUST be maintained across all code changes. Breaking them causes unrelated search results.

### `title_relevant()` in `download_helpers.py`
- **Minimum 60% word overlap** between query words and title words (excluding stopwords)
- Stopwords excluded from matching: `the, a, an, of, in, to, for, and, or, is, it, at, by, on, with, as, from, that, this, not`
- Full phrase match (`query in title+author`) always passes
- Combined title+author check requires **ALL** query words present
- **NEVER** use `any()` for single-word matching — it causes "mother" to match "Mother-in-Law Cure" when searching "Mother of Learning"

### `search_annas_archive()` in `provider_search.py`
- The `return results` MUST be **outside** the `for search_q, search_ext` loop — not inside it
- Multiple query variations (plain, no ext, +all) must all be tried before returning
- Results sorted by size descending, deduplicated by MD5

### `filter_results()` in `download_helpers.py`
- Torrents: skip seeders < 1, skip size < 10KB or > 2GB
- All sources: apply `title_relevant()` check
- Suspicious keywords filtered via `_SUSPICIOUS_KEYWORDS` regex

## Download Reliability

### Post-download verification (`novel_annas_workers.py`)
- After downloading an epub, `_verify_epub_title()` checks the OPF `<dc:title>` matches expected title
- Requires ≥30% word overlap between expected and actual title
- Wrong books are deleted and the next candidate is tried
- **NEVER** skip this verification — it prevents wrong-book downloads (e.g., "Final Empire" MD5 returning "Well of Ascension")

### Candidate selection (`novel_annas_workers.py`)
- Quick libgen pre-check on top 8 candidates
- Candidates with working libgen links sorted first
- Try up to 5 candidates (not 3)
- When libgen fails, alt MD5 search finds different MD5s with working links

### `download_from_annas()` fallback
- Primary: `libgen.li/ads.php?md5={md5}` → extract `get.php` link
- Fallback: search Anna's Archive for the same title, find alt MD5 with working libgen
- **NEVER** fall back to `file.php` links (IPFS only, broken)

## Pipeline (`pipeline.py`)

### File organization
- `organize_file()` extracts author from epub OPF `<dc:creator>` when author is unknown
- Prevents files going to `Unknown/` directory
- Calibre import uses `--automerge overwrite` to prevent duplicates

### Post-pipeline hooks (run after every successful import)
1. **ABS cleanup** — deduplicates ABS entries, clears stale cover caches
2. **Auto-classify** — assigns series metadata:
   - Phase A: match title patterns against known series in ABS
   - Phase A2: check filenames in item directory for book numbers
   - Phase B: web lookup via Open Library + Google Books APIs
   - Strips marketing adjectives from series names

### Cover handling
- ABS caches covers in `/metadata/items/{id}/` and `/metadata/cache/covers/`
- When replacing epub files, MUST clear the ABS cover cache for that item
- ABS won't re-extract covers from updated epubs without cache invalidation

## ABS Integration

### Incoming directory
- `/data/media/books/ebooks/incoming/` has `.audiobookshelfignore` and `.ignore` files
- These prevent ABS from scanning the incoming dir and creating phantom library items
- **NEVER** remove these files

### Series metadata
- ABS items listing does NOT include series data — must fetch individual items via `/api/items/{id}`
- Library API fetches all items (not paginated) for proper cross-page series grouping
- Series set via `PATCH /api/items/{id}/media` with `metadata.series` array

## Security
- No hardcoded IPs, passwords, API keys, or user-specific paths in committed code
- All credentials come from environment variables or `config.py` settings
- Test files use generic credentials (`testuser`/`testpass`)
- Kavita URL replacement must NOT hardcode LAN IPs

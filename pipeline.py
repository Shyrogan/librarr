"""Post-processing pipeline — organize, import, track."""
import logging
import os
import re
import shutil

import config
import targets
import telemetry

logger = logging.getLogger("librarr")


def sanitize_filename(name, max_len=80):
    """Make a string safe for use as a filename."""
    name = re.sub(r'[<>:"/\\|?*]', "", name)
    name = re.sub(r"\s+", " ", name).strip()
    name = name.strip(".")
    if len(name) > max_len:
        name = name[:max_len].rstrip()
    return name or "Unknown"


def clean_series_title(name):
    """Clean a torrent name into a series title for directory naming."""
    # Strip file extensions
    name = re.sub(r"\.(epub|cbz|cbr|pdf|zip|mobi|azw3)$", "", name, flags=re.IGNORECASE)
    # Strip release group tags like [VIZ Media] [Bondman] [1r0n]
    name = re.sub(r"\[[^\]]*\]", "", name)
    # Strip parenthetical tags like (Digital) (f) (c2c)
    name = re.sub(r"\((?:Digital|f|c2c|Viz|Complete)\)", "", name, flags=re.IGNORECASE)
    # Strip volume/chapter ranges like v01-32, 1-39, Vol. 1-10
    name = re.sub(r"\s*(?:Vol\.?|Volume|v)\s*\d+.*$", "", name, flags=re.IGNORECASE)
    name = re.sub(r"\s*\d+-\d+.*$", "", name)
    # Clean up whitespace
    name = re.sub(r"\s+", " ", name).strip().rstrip("-").strip()
    return name or "Unknown"


def organize_file(file_path, title, author, media_type="ebook"):
    if not config.FILE_ORG_ENABLED:
        return file_path

    if not os.path.exists(file_path):
        logger.warning(f"organize_file: path not found: {file_path}")
        return file_path

    # Try to extract author from epub metadata if not provided
    if not author or author == "Unknown":
        try:
            import zipfile, re as _re
            if file_path.endswith('.epub') and os.path.isfile(file_path):
                with zipfile.ZipFile(file_path, 'r') as z:
                    for name in z.namelist():
                        if name.endswith('.opf'):
                            opf = z.read(name).decode('utf-8', errors='ignore')
                            author_m = _re.search(r'<dc:creator[^>]*>(.*?)</dc:creator>', opf)
                            if author_m:
                                author = author_m.group(1).strip()
                                logger.info(f"Extracted author from epub: {author}")
                            break
        except Exception:
            pass

    safe_author = sanitize_filename(author or "Unknown")
    safe_title = sanitize_filename(title or "Unknown")

    if media_type == "manga":
        safe_title = clean_series_title(title or "Unknown")

    if media_type == "audiobook":
        base_dir = config.AUDIOBOOK_ORGANIZED_DIR
    elif media_type == "manga":
        # Manga: {MANGA_ORGANIZED_DIR}/{series}/{filename} — flat series structure
        dest_dir = os.path.join(config.MANGA_ORGANIZED_DIR, safe_title)
        os.makedirs(dest_dir, exist_ok=True)
        if os.path.isdir(file_path):
            dest = dest_dir
            if os.path.abspath(file_path) != os.path.abspath(dest):
                try:
                    shutil.move(file_path, dest)
                    logger.info(f"Organized manga folder: {dest}")
                    file_path = dest
                except Exception as e:
                    logger.error(f"organize_file (manga folder) failed: {e}")
        else:
            fname = os.path.basename(file_path)
            dest = os.path.join(dest_dir, fname)
            if os.path.abspath(file_path) != os.path.abspath(dest):
                try:
                    shutil.move(file_path, dest)
                    logger.info(f"Organized manga: {dest}")
                    file_path = dest
                except Exception as e:
                    logger.error(f"organize_file (manga) failed: {e}")
        # Copy to Kavita manga library
        if config.KAVITA_MANGA_LIBRARY_PATH:
            try:
                kavita_dir = os.path.join(config.KAVITA_MANGA_LIBRARY_PATH, safe_title)
                os.makedirs(kavita_dir, exist_ok=True)
                dest_file = os.path.join(kavita_dir, os.path.basename(file_path))
                if os.path.abspath(file_path) != os.path.abspath(dest_file):
                    shutil.copy2(file_path, dest_file)
                    logger.info(f"Copied manga to Kavita: {dest_file}")
            except Exception as e:
                logger.warning(f"Kavita manga copy failed: {e}")
        return file_path
    else:
        base_dir = config.EBOOK_ORGANIZED_DIR

    dest_dir = os.path.join(base_dir, safe_author, safe_title)

    if os.path.isdir(file_path):
        # Move entire folder (e.g. audiobook with multiple chapter files)
        if os.path.abspath(file_path) == os.path.abspath(dest_dir):
            return file_path
        try:
            os.makedirs(os.path.dirname(dest_dir), exist_ok=True)
            shutil.move(file_path, dest_dir)
            logger.info(f"Organized folder: {dest_dir}")
            return dest_dir
        except Exception as e:
            logger.error(f"organize_file (folder) failed: {e}")
            return file_path
    else:
        ext = os.path.splitext(file_path)[1].lower() or ".epub"
        dest_path = os.path.join(dest_dir, f"{safe_title}{ext}")
        if os.path.abspath(file_path) == os.path.abspath(dest_path):
            return file_path
        try:
            os.makedirs(dest_dir, exist_ok=True)
            shutil.move(file_path, dest_path)
            logger.info(f"Organized: {dest_path}")
        except Exception as e:
            logger.error(f"organize_file failed: {e}")
            return file_path

        if config.KAVITA_LIBRARY_PATH and media_type == "ebook":
            try:
                kavita_dir = os.path.join(config.KAVITA_LIBRARY_PATH, safe_author, safe_title)
                kavita_path = os.path.join(kavita_dir, f"{safe_title}{ext}")
                os.makedirs(kavita_dir, exist_ok=True)
                shutil.copy2(dest_path, kavita_path)
                logger.info(f"Copied to Kavita library: {kavita_path}")
            except Exception as e:
                logger.warning(f"Kavita copy failed: {e}")

        return dest_path

def _resolve_target_names(media_type, source, requested_target_names=None):
    """Resolve import target names using explicit request and routing rules."""
    enabled = set(config.get_enabled_target_names())
    if requested_target_names:
        requested = {t for t in requested_target_names if t}
        return enabled & requested

    rules = config.get_target_routing_rules()
    selected = set(enabled)
    # Supported lightweight shapes:
    # {"media_type":{"ebook":["calibre","kavita"]},"source":{"annas":["calibre"]}}
    # {"ebook":["calibre"], "audiobook":["audiobookshelf"]}
    try:
        media_rules = rules.get("media_type", {}) if isinstance(rules, dict) else {}
        source_rules = rules.get("source", {}) if isinstance(rules, dict) else {}
        if not media_rules and isinstance(rules.get(media_type), list):
            media_rules = {media_type: rules.get(media_type)}
        if media_type in media_rules and isinstance(media_rules[media_type], list):
            selected &= set(media_rules[media_type])
        if source in source_rules and isinstance(source_rules[source], list):
            selected &= set(source_rules[source])
    except Exception:
        return enabled
    return selected




def _cleanup_abs_after_import(title, media_type, logger):
    """After import, clean up ABS duplicates and invalidate stale covers."""
    try:
        import config
        if not config.has_audiobookshelf():
            return
        import requests, subprocess, re, threading

        def _cleanup_worker():
            try:
                abs_url = config.ABS_URL
                token = config.ABS_TOKEN
                headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}

                lib_id = config.ABS_EBOOK_LIBRARY_ID if media_type == "ebook" else config.ABS_LIBRARY_ID
                if not lib_id:
                    return

                # Trigger scan to pick up new files
                requests.post(f"{abs_url}/api/libraries/{lib_id}/scan?force=1", headers=headers, timeout=10)
                import time
                time.sleep(10)

                # Get all items
                all_items = []
                page = 0
                while True:
                    r = requests.get(f"{abs_url}/api/libraries/{lib_id}/items",
                        params={"limit": 100, "page": page}, headers=headers, timeout=15)
                    if r.status_code != 200:
                        break
                    results = r.json().get("results", [])
                    all_items.extend(results)
                    if len(results) < 100:
                        break
                    page += 1

                # Deduplicate by normalized title — keep Calibre (ID) versions
                import os
                seen = {}
                for item in all_items:
                    meta = item.get("media", {}).get("metadata", {})
                    item_title = meta.get("title", "")
                    iid = item["id"]
                    path = item.get("path", "")

                    if item_title.lower() == "incoming" or not item_title:
                        requests.delete(f"{abs_url}/api/items/{iid}?hard=1", headers=headers, timeout=10)
                        continue

                    # Normalize title for dedup
                    key = item_title.lower()
                    key = re.sub(r'\([^)]*\)', '', key).strip()
                    key = re.sub(r':\s.*$', '', key).strip()
                    for prefix in ['mistborn: ', 'stormlight archive \d+ - ']:
                        key = re.sub(prefix, '', key)
                    key = key.strip()

                    in_calibre = bool(re.search(r'\(\d+\)$', os.path.basename(path)))

                    if key in seen:
                        # Keep the one in Calibre dir, delete the other
                        if in_calibre and not seen[key]["in_calibre"]:
                            # New one is better, delete old
                            requests.delete(f"{abs_url}/api/items/{seen[key]['id']}?hard=1", headers=headers, timeout=10)
                            logger.info("[ABS-cleanup] Replaced dup: %s", item_title)
                            seen[key] = {"id": iid, "in_calibre": True}
                        else:
                            # Old one is better, delete new
                            requests.delete(f"{abs_url}/api/items/{iid}?hard=1", headers=headers, timeout=10)
                            logger.info("[ABS-cleanup] Removed dup: %s", item_title)
                    else:
                        seen[key] = {"id": iid, "in_calibre": in_calibre}

                # Clear cover cache for the newly imported book
                for item in all_items:
                    meta = item.get("media", {}).get("metadata", {})
                    if title.lower()[:20] in meta.get("title", "").lower():
                        subprocess.run(
                            ["docker", "exec", "audiobookshelf", "rm", "-rf",
                             f"/metadata/items/{item['id']}"],
                            capture_output=True, timeout=5,
                        )

            except Exception as e:
                logger.warning("[ABS-cleanup] Failed: %s", e)

        threading.Thread(target=_cleanup_worker, daemon=True).start()
    except Exception as e:
        logger.warning("[ABS-cleanup] Setup failed: %s", e)


def _lookup_series_from_web(title, author, logger):
    """Look up series info from Open Library and Google Books APIs.

    Returns: (series_name, sequence_number) or (None, None)
    """
    import requests, re

    clean_title = re.sub(r'\([^)]*\)', '', title).strip()
    clean_title = re.sub(r'\[.*?\]', '', clean_title).strip()
    clean_title = re.sub(r'\s*[-:]\s*(A |An )?LitRPG.*$', '', clean_title, flags=re.IGNORECASE).strip()
    clean_title = re.sub(r':\s+.*$', '', clean_title).strip()  # Strip any subtitle after colon
    clean_title = re.sub(r',?\s*Book\s*\d+.*$', '', clean_title, flags=re.IGNORECASE).strip()

    # --- Open Library ---
    try:
        q = f"{clean_title} {author}".strip() if author else clean_title
        r = requests.get(
            "https://openlibrary.org/search.json",
            params={"q": q, "limit": 5, "fields": "title,author_name,subject,edition_key,key"},
            headers={"User-Agent": "Librarr/1.0"},
            timeout=10,
        )
        if r.status_code == 200:
            docs = r.json().get("docs", [])
            for doc in docs[:3]:
                work_key = doc.get("key", "")
                if not work_key:
                    continue
                # Fetch the work details which may have series info
                wr = requests.get(
                    f"https://openlibrary.org{work_key}.json",
                    headers={"User-Agent": "Librarr/1.0"},
                    timeout=10,
                )
                if wr.status_code == 200:
                    work = wr.json()
                    # Check for series links
                    links = work.get("links", [])
                    for link in links:
                        link_title = link.get("title", "")
                        if "series" in link_title.lower():
                            logger.info("[Web-classify] OL series link: %s", link_title)

                    # Check subjects for series patterns
                    subjects = work.get("subjects", [])
                    for subj in subjects:
                        if isinstance(subj, str):
                            # Look for "Series Name, Book N" patterns
                            m = re.match(r'^(.+?),?\s*(?:Book|Vol|#)\s*(\d+\.?\d*)', subj, re.IGNORECASE)
                            if m:
                                return m.group(1).strip(), m.group(2)
    except Exception as e:
        logger.debug("[Web-classify] Open Library error: %s", e)

    # --- Google Books ---
    try:
        q = f'intitle:"{clean_title}"'
        if author:
            q += f' inauthor:"{author}"'
        r = requests.get(
            "https://www.googleapis.com/books/v1/volumes",
            params={"q": q, "maxResults": 5, "printType": "books"},
            timeout=10,
        )
        if r.status_code == 200:
            items = r.json().get("items", [])
            for item in items[:3]:
                vol = item.get("volumeInfo", {})
                gb_title = vol.get("title", "")
                # Check if title is a reasonable match
                if not _fuzzy_title_match(clean_title, gb_title):
                    continue

                # Google Books sometimes has series in the title or subtitle
                subtitle = vol.get("subtitle", "")
                description = vol.get("description", "")

                # Check subtitle for series info: "Book 3 of The Stormlight Archive"
                for text in [subtitle, gb_title]:
                    m = re.search(
                        r'(?:Book|Volume|#)\s*(\d+\.?\d*)\s*(?:of|in|:)\s*(?:the\s+)?(.+?)(?:\s*series)?$',
                        text, re.IGNORECASE,
                    )
                    if m:
                        return m.group(2).strip(), m.group(1)
                    # "Series Name #N"
                    m2 = re.search(r'(.+?)\s*#(\d+\.?\d*)$', text)
                    if m2:
                        return m2.group(1).strip(), m2.group(2)

                # Check description for series references
                if description:
                    # "the third book in the Cradle series"
                    ordinals = {'first': '1', 'second': '2', 'third': '3', 'fourth': '4',
                                'fifth': '5', 'sixth': '6', 'seventh': '7', 'eighth': '8',
                                'ninth': '9', 'tenth': '10', 'eleventh': '11', 'twelfth': '12'}
                    m = re.search(
                        r'(?:the\s+)?(\w+)\s+(?:book|novel|volume|installment)\s+(?:in|of)\s+(?:the\s+)?(.+?)\s*(?:series|saga|trilogy|sequence)',
                        description, re.IGNORECASE,
                    )
                    if m:
                        ordinal = m.group(1).lower()
                        series_name = m.group(2).strip()
                        # Strip marketing adjectives from series name
                        series_name = re.sub(r'^(?:wildly\s+)?(?:popular|acclaimed|bestselling|award-winning|beloved|epic|exciting|incredible|amazing|new|addictive|hit|smash)(?:\s+and\s+\w+)?\s+', '', series_name, flags=re.IGNORECASE).strip()
                        seq = ordinals.get(ordinal, "")
                        if not seq:
                            # Try numeric
                            try:
                                seq = str(int(ordinal))
                            except ValueError:
                                seq = ""
                        if seq and series_name:
                            return series_name, seq

                    # "Book N in the Series Name"
                    m2 = re.search(
                        r'(?:Book|Volume)\s+(\d+)\s+(?:in|of)\s+(?:the\s+)?(.+?)\s*(?:series|saga|trilogy|$)',
                        description, re.IGNORECASE,
                    )
                    if m2:
                        return m2.group(2).strip().rstrip('.'), m2.group(1)

                    # "the X series" pattern - most reliable
                    m3 = re.search(
                        r'(?:the|this)\s+(.+?)\s+series',
                        description, re.IGNORECASE,
                    )
                    if m3:
                        candidate = m3.group(1).strip()
                        # Clean up adjectives before the series name
                        candidate = re.sub(r'^(?:wildly\s+)?(?:popular|acclaimed|bestselling|award-winning|beloved|epic|exciting|incredible|amazing|new|addictive)(?:\s+and\s+\w+)?\s+', '', candidate, flags=re.IGNORECASE).strip()
                        if 2 <= len(candidate) <= 60 and candidate[0].isupper():
                            # Try to find the book number
                            seq = ''
                            # "first/second/third book in the X series"
                            ordinals = {'first': '1', 'second': '2', 'third': '3', 'fourth': '4',
                                        'fifth': '5', 'sixth': '6', 'seventh': '7', 'eighth': '8',
                                        'ninth': '9', 'tenth': '10', 'eleventh': '11', 'twelfth': '12',
                                        'thirteenth': '13', 'fourteenth': '14'}
                            for w, n in ordinals.items():
                                if w in description.lower():
                                    seq = n
                                    break
                            if not seq:
                                bm = re.search(r'[Bb]ook\s+(\d+)', description)
                                if bm:
                                    seq = bm.group(1)
                            return candidate, seq
    except Exception as e:
        logger.debug("[Web-classify] Google Books error: %s", e)

    return None, None


def _fuzzy_title_match(query, candidate):
    """Check if two titles are roughly the same book."""
    import re
    def normalize(t):
        t = t.lower()
        t = re.sub(r'[^a-z0-9\s]', '', t)
        t = re.sub(r'\s+', ' ', t).strip()
        return t
    q = normalize(query)
    c = normalize(candidate)
    if q in c or c in q:
        return True
    q_words = set(q.split()) - {'the', 'a', 'an', 'of', 'in', 'on', 'at', 'to', 'for', 'and', 'or', 'book'}
    c_words = set(c.split()) - {'the', 'a', 'an', 'of', 'in', 'on', 'at', 'to', 'for', 'and', 'or', 'book'}
    if not q_words:
        return False
    overlap = len(q_words & c_words) / len(q_words)
    return overlap >= 0.7


def _auto_classify_series(title, author, media_type, logger):
    """After a book is added, scan ABS for unclassified items and try to assign series.

    Strategy:
    1. Match by title pattern against known series in ABS (fast, local)
    2. Look up series info from Open Library / Google Books APIs (slower, comprehensive)
    """
    try:
        import config
        if not config.has_audiobookshelf():
            return

        import requests, re, threading

        def _classify_worker():
            try:
                abs_url = config.ABS_URL
                token = config.ABS_TOKEN
                headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}

                lib_ids = []
                if media_type in ("ebook", "novel"):
                    if config.ABS_EBOOK_LIBRARY_ID:
                        lib_ids.append(config.ABS_EBOOK_LIBRARY_ID)
                elif media_type == "audiobook":
                    if config.ABS_LIBRARY_ID:
                        lib_ids.append(config.ABS_LIBRARY_ID)
                else:
                    if config.ABS_EBOOK_LIBRARY_ID:
                        lib_ids.append(config.ABS_EBOOK_LIBRARY_ID)
                    if config.ABS_LIBRARY_ID:
                        lib_ids.append(config.ABS_LIBRARY_ID)

                for lib_id in lib_ids:
                    all_items = []
                    page = 0
                    while True:
                        r = requests.get(
                            f"{abs_url}/api/libraries/{lib_id}/items",
                            params={"limit": 100, "page": page},
                            headers=headers, timeout=15,
                        )
                        if r.status_code != 200:
                            break
                        results = r.json().get("results", [])
                        all_items.extend(results)
                        if len(results) < 100:
                            break
                        page += 1

                    # Phase 1: Build series index from already-classified items
                    known_series = {}  # series_name -> set of title keywords
                    for item in all_items:
                        iid = item["id"]
                        full_r = requests.get(f"{abs_url}/api/items/{iid}", headers=headers, timeout=8)
                        if full_r.status_code != 200:
                            continue
                        full = full_r.json()
                        meta = full.get("media", {}).get("metadata", {})
                        series_list = meta.get("series", [])
                        item_title = meta.get("title", "")

                        if series_list:
                            sname = series_list[0].get("name", "")
                            if sname:
                                if sname not in known_series:
                                    known_series[sname] = set()
                                known_series[sname].add(item_title.lower())

                    # Phase 2: Classify unclassified items
                    for item in all_items:
                        iid = item["id"]
                        full_r = requests.get(f"{abs_url}/api/items/{iid}", headers=headers, timeout=8)
                        if full_r.status_code != 200:
                            continue
                        full = full_r.json()
                        meta = full.get("media", {}).get("metadata", {})
                        series_list = meta.get("series", [])
                        item_title = meta.get("title", "")
                        item_author = meta.get("authorName", "")

                        if series_list:
                            continue
                        if not item_title or item_title.lower() == "incoming":
                            continue

                        matched_series = None
                        matched_seq = ""

                        # Strategy A: Match against known series by title pattern
                        for sname in known_series:
                            pattern = re.search(
                                rf'(?:\(|^|\s){re.escape(sname)}\s*(?:book\s*)?#?(\d+\.?\d*)(?:\)|$|\s)',
                                item_title, re.IGNORECASE,
                            )
                            if pattern:
                                matched_series = sname
                                matched_seq = pattern.group(1)
                                break
                            pattern2 = re.search(
                                rf'{re.escape(sname)}\s*(?:,\s*)?(?:book\s*)?#?(\d+\.?\d*)',
                                item_title, re.IGNORECASE,
                            )
                            if pattern2:
                                matched_series = sname
                                matched_seq = pattern2.group(1)
                                break

                        # Strategy A2: Check filenames in item directory for book numbers
                        if not matched_series and known_series:
                            import subprocess, os
                            item_path = full.get("path", "")
                            if item_path:
                                # Get files in the directory via ABS
                                lib_files = full.get("libraryFiles", [])
                                for lf in lib_files:
                                    fname = lf.get("metadata", {}).get("filename", "")
                                    for sname in known_series:
                                        # Check filename for "Series Name N" pattern
                                        pattern = re.search(
                                            rf'{re.escape(sname)}\s*(?:book\s*)?#?(\d+\.?\d*)',
                                            fname, re.IGNORECASE,
                                        )
                                        if pattern:
                                            matched_series = sname
                                            matched_seq = pattern.group(1)
                                            break
                                        # Check for just "N" when file is clearly part of the series author
                                        if item_author:
                                            auth_in_path = any(w.lower() in item_path.lower() for w in item_author.split()[:1])
                                            sname_words = sname.lower().split()
                                            title_has_series_words = sum(1 for w in sname_words if w in item_title.lower()) >= len(sname_words) * 0.5
                                            if auth_in_path and title_has_series_words:
                                                num_m = re.search(r'(\d+)', fname)
                                                if num_m:
                                                    matched_series = sname
                                                    matched_seq = num_m.group(1)
                                                    break
                                    if matched_series:
                                        break

                        # Strategy B: Web lookup via Open Library / Google Books
                        if not matched_series:
                            web_series, web_seq = _lookup_series_from_web(
                                item_title, item_author, logger,
                            )
                            if web_series:
                                matched_series = web_series
                                matched_seq = web_seq or ""
                                logger.info(
                                    "[Web-classify] Found: %s -> %s #%s",
                                    item_title, web_series, web_seq,
                                )

                        if matched_series:
                            payload = {"metadata": {
                                "series": [{"name": matched_series, "sequence": matched_seq}],
                            }}
                            r = requests.patch(
                                f"{abs_url}/api/items/{iid}/media",
                                json=payload, headers=headers, timeout=10,
                            )
                            if r.status_code == 200:
                                logger.info(
                                    "[Auto-classify] %s -> %s #%s",
                                    item_title, matched_series, matched_seq,
                                )
            except Exception as e:
                logger.warning("[Auto-classify] Failed: %s", e)

        threading.Thread(target=_classify_worker, daemon=True).start()
    except Exception as e:
        logger.warning("[Auto-classify] Setup failed: %s", e)



def run_pipeline(file_path, title="", author="", media_type="ebook",
                 source="", source_id="", job_id="", library_db=None, target_names=None):
    """Full post-processing: organize → import → track.

    Args:
        file_path: Path to the downloaded file.
        title: Book title.
        author: Author name.
        media_type: 'ebook' or 'audiobook'.
        source: Source name (e.g. 'annas', 'prowlarr').
        source_id: Unique identifier for duplicate detection (md5, hash, url).
        job_id: Download job ID for activity log cross-reference.
        library_db: LibraryDB instance (optional — skips tracking if None).

    Returns:
        dict with pipeline results, or None if skipped (duplicate).
    """
    result = {
        "organized": False,
        "imports": {},
        "verifications": {},
        "verification_failed_targets": [],
        "tracked": False,
    }

    # Duplicate check
    if library_db and source_id and library_db.has_source_id(source_id):
        logger.info(f"Duplicate skipped: {title} (source_id={source_id})")
        library_db.log_event("skip", title=title,
                             detail=f"Duplicate (source: {source})",
                             job_id=job_id)
        telemetry.emit_event("job_duplicate_skipped", {
            "job_id": job_id,
            "title": title,
            "source": source,
            "source_id": source_id,
        })
        return None

    # Organize
    original_path = file_path
    file_size = 0
    try:
        if os.path.isfile(file_path):
            file_size = os.path.getsize(file_path)
        elif os.path.isdir(file_path):
            # Sum all files in directory (e.g. audiobook folders)
            for dirpath, _, filenames in os.walk(file_path):
                for f in filenames:
                    file_size += os.path.getsize(os.path.join(dirpath, f))
    except OSError:
        pass

    organized_path = organize_file(file_path, title, author, media_type)
    result["organized"] = organized_path != original_path

    if library_db and result["organized"]:
        library_db.log_event("organize", title=title,
                             detail=f"→ {organized_path}", job_id=job_id)

    # Import to enabled targets
    enabled_names = _resolve_target_names(media_type, source, requested_target_names=target_names)
    enabled = [t for t in targets.get_enabled_targets() if t.name in enabled_names]
    for target in enabled:
        try:
            import_result = target.import_book(
                organized_path, title=title, author=author, media_type=media_type
            )
            if import_result:
                result["imports"][target.name] = import_result
                verify = {"ok": None, "mode": "unsupported"}
                if hasattr(target, "verify_import"):
                    try:
                        verify = target.verify_import(
                            organized_path,
                            title=title,
                            author=author,
                            media_type=media_type,
                            import_result=import_result,
                        ) or {"ok": None, "mode": "unknown"}
                    except Exception as e:
                        verify = {"ok": False, "mode": "exception", "reason": str(e)}
                result["verifications"][target.name] = verify
                verify_ok = verify.get("ok")
                telemetry.metrics.inc(
                    "librarr_import_verifications_total",
                    target=target.name,
                    result="ok" if verify_ok is True else "failed" if verify_ok is False else "skipped",
                )
                telemetry.emit_event(
                    "import_verified" if verify_ok is not False else "import_verification_failed",
                    {
                        "job_id": job_id,
                        "title": title,
                        "author": author,
                        "target": target.name,
                        "media_type": media_type,
                        "verification": verify,
                    },
                )
                if verify_ok is False:
                    result["verification_failed_targets"].append(target.name)
                if library_db:
                    library_db.log_event(
                        "import", title=title,
                        detail=f"Imported to {target.label}",
                        job_id=job_id,
                    )
                    if verify_ok is False:
                        library_db.log_event(
                            "error", title=title,
                            detail=f"{target.label} verification failed",
                            job_id=job_id,
                        )
        except Exception as e:
            logger.error(f"Target {target.name} failed: {e}")
            if library_db:
                library_db.log_event(
                    "error", title=title,
                    detail=f"{target.label} import failed: {e}",
                    job_id=job_id,
                )

    if result["verification_failed_targets"]:
        failed = ", ".join(result["verification_failed_targets"])
        raise RuntimeError(f"Post-import verification failed for: {failed}")

    # Track in library
    file_format = os.path.splitext(organized_path)[1].lstrip(".").lower()
    item_id = None
    if library_db:
        item_id = library_db.add_item(
            title=title, author=author,
            file_path=organized_path, original_path=original_path,
            file_size=file_size, file_format=file_format,
            media_type=media_type, source=source, source_id=source_id,
            metadata=result["imports"],
        )
        library_db.log_event(
            "download", title=title,
            detail=f"Added to library ({source})",
            library_item_id=item_id, job_id=job_id,
        )
        result["tracked"] = True

    # Clean up ABS duplicates and stale covers, then auto-classify
    _cleanup_abs_after_import(title, media_type, logger)
    _auto_classify_series(title, author, media_type, logger)

    return result

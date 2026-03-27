from __future__ import annotations

import csv
import io
import threading
import uuid
from concurrent.futures import ThreadPoolExecutor, as_completed
from io import BytesIO

import requests
from flask import Blueprint, jsonify, request, send_file


def _fetch_abs_item(abs_url, token, iid):
    """Fetch full item details from ABS to get series metadata."""
    try:
        r = requests.get(
            f"{abs_url}/api/items/{iid}",
            headers={"Authorization": f"Bearer {token}"},
            timeout=8,
        )
        return r.json() if r.status_code == 200 else None
    except Exception:
        return None


def _fetch_all_abs_items(abs_url, token, library_id, search=""):
    """Fetch ALL items from an ABS library (handles pagination internally)."""
    all_results = []
    page = 0
    while True:
        params = {"limit": 100, "page": page, "sort": "media.metadata.title", "desc": 0}
        if search:
            params["filter"] = f"search={search}"
        r = requests.get(
            f"{abs_url}/api/libraries/{library_id}/items",
            params=params,
            headers={"Authorization": f"Bearer {token}"},
            timeout=15,
        )
        if r.status_code != 200:
            break
        data = r.json()
        results = data.get("results", [])
        all_results.extend(results)
        if len(results) < 100:
            break
        page += 1
    return all_results


def _enrich_with_series(abs_url, token, items):
    """Batch-fetch full item details to get series metadata."""
    item_ids = [item["id"] for item in items]
    full_items = {}
    with ThreadPoolExecutor(max_workers=8) as ex:
        futs = {ex.submit(_fetch_abs_item, abs_url, token, iid): iid for iid in item_ids}
        for fut in as_completed(futs):
            iid = futs[fut]
            result = fut.result()
            if result:
                full_items[iid] = result
    return full_items


def create_blueprint(ctx):
    bp = Blueprint("library_routes", __name__)
    config = ctx["config"]
    logger = ctx["logger"]
    library = ctx["library"]

    @bp.route("/api/library")
    def api_library():
        if not config.has_audiobookshelf() or not config.ABS_EBOOK_LIBRARY_ID:
            return jsonify({"books": [], "total": 0, "page": 1, "pages": 1})
        search = request.args.get("q", "").strip()
        try:
            # Fetch ALL items for proper series grouping
            all_items = _fetch_all_abs_items(
                config.ABS_URL, config.ABS_TOKEN, config.ABS_EBOOK_LIBRARY_ID, search
            )
            # Enrich with series data
            full_items = _enrich_with_series(config.ABS_URL, config.ABS_TOKEN, all_items)

            books = []
            for item in all_items:
                iid = item["id"]
                full = full_items.get(iid, item)
                media = full.get("media", {})
                meta = media.get("metadata", {})
                title = meta.get("title", "Unknown")
                if title == "incoming":
                    continue
                series_list = meta.get("series", [])
                series_name = series_list[0].get("name", "") if series_list else ""
                series_seq = series_list[0].get("sequence", "") if series_list else ""
                books.append({
                    "id": iid,
                    "title": title,
                    "authors": meta.get("authorName", "Unknown"),
                    "has_cover": bool(media.get("coverPath")),
                    "cover_url": f"/api/cover/{iid}",
                    "added": item.get("addedAt", ""),
                    "series": series_name,
                    "series_sequence": series_seq,
                })
            return jsonify({
                "books": books,
                "total": len(books),
                "page": 1,
                "pages": 1,
            })
        except Exception as e:
            logger.error("Library error: %s", e)
            return jsonify({"books": [], "total": 0, "page": 1, "pages": 1})

    @bp.route("/api/cover/<item_id>")
    def api_cover(item_id):
        if not config.has_audiobookshelf():
            return "", 404
        try:
            resp = requests.get(
                f"{config.ABS_URL}/api/items/{item_id}/cover",
                headers={"Authorization": f"Bearer {config.ABS_TOKEN}"},
                timeout=10,
            )
            if resp.status_code == 200:
                return send_file(
                    BytesIO(resp.content),
                    mimetype=resp.headers.get("Content-Type", "image/jpeg"),
                )
        except Exception:
            pass
        return "", 404

    @bp.route("/api/library/book/<item_id>", methods=["DELETE"])
    def api_delete_book(item_id):
        if not config.has_audiobookshelf():
            return jsonify({"success": False, "error": "Audiobookshelf not configured"}), 500
        try:
            resp = requests.delete(
                f"{config.ABS_URL}/api/items/{item_id}?hard=1",
                headers={"Authorization": f"Bearer {config.ABS_TOKEN}"},
                timeout=10,
            )
            success = resp.status_code == 200
            if success:
                logger.info("Deleted ebook %s from Audiobookshelf", item_id)
            return jsonify({"success": success})
        except Exception as e:
            logger.error("Delete ebook error: %s", e)
            return jsonify({"success": False, "error": str(e)}), 500

    @bp.route("/api/library/audiobook/<item_id>", methods=["DELETE"])
    def api_delete_audiobook(item_id):
        if not config.has_audiobookshelf():
            return jsonify({"success": False, "error": "Audiobookshelf not configured"}), 500
        try:
            resp = requests.delete(
                f"{config.ABS_URL}/api/items/{item_id}?hard=1",
                headers={"Authorization": f"Bearer {config.ABS_TOKEN}"},
                timeout=10,
            )
            success = resp.status_code == 200
            if success:
                logger.info("Deleted audiobook %s from Audiobookshelf", item_id)
            return jsonify({"success": success})
        except Exception as e:
            logger.error("Delete audiobook error: %s", e)
            return jsonify({"success": False, "error": str(e)}), 500

    @bp.route("/api/external-urls")
    def api_external_urls():
        return jsonify({"abs_url": config.ABS_PUBLIC_URL})

    @bp.route("/api/library/audiobooks")
    def api_library_audiobooks():
        if not config.has_audiobookshelf() or not config.ABS_LIBRARY_ID:
            return jsonify({"audiobooks": [], "total": 0})
        search = request.args.get("q", "").strip()
        try:
            # Fetch ALL audiobooks for series grouping
            all_items = _fetch_all_abs_items(
                config.ABS_URL, config.ABS_TOKEN, config.ABS_LIBRARY_ID, search
            )
            # Enrich with series data
            full_items = _enrich_with_series(config.ABS_URL, config.ABS_TOKEN, all_items)

            audiobooks = []
            for item in all_items:
                iid = item["id"]
                full = full_items.get(iid, item)
                media = full.get("media", {})
                meta = media.get("metadata", {})
                duration = media.get("duration", 0)
                hours = int(duration // 3600)
                mins = int((duration % 3600) // 60)
                series_list = meta.get("series", [])
                series_name = series_list[0].get("name", "") if series_list else ""
                series_seq = series_list[0].get("sequence", "") if series_list else ""
                audiobooks.append({
                    "id": iid,
                    "title": meta.get("title", "Unknown"),
                    "authors": meta.get("authorName", "Unknown"),
                    "narrator": meta.get("narratorName", ""),
                    "duration": f"{hours}h {mins}m" if duration else "",
                    "num_chapters": media.get("numChapters", 0),
                    "cover_url": f"/api/audiobook/cover/{iid}",
                    "has_cover": bool(media.get("coverPath")),
                    "series": series_name,
                    "series_sequence": series_seq,
                })
            return jsonify({
                "audiobooks": audiobooks,
                "total": len(audiobooks),
                "page": 1,
                "pages": 1,
            })
        except Exception as e:
            logger.error("Audiobook library error: %s", e)
            return jsonify({"audiobooks": [], "total": 0, "page": 1, "pages": 1})

    @bp.route("/api/audiobook/cover/<item_id>")
    def api_audiobook_cover(item_id):
        if not config.has_audiobookshelf():
            return "", 404
        try:
            resp = requests.get(
                f"{config.ABS_URL}/api/items/{item_id}/cover",
                headers={"Authorization": f"Bearer {config.ABS_TOKEN}"},
                timeout=10,
            )
            if resp.status_code == 200:
                return send_file(
                    BytesIO(resp.content),
                    mimetype=resp.headers.get("Content-Type", "image/jpeg"),
                )
        except Exception:
            pass
        return "", 404

    @bp.route("/api/activity")
    def api_activity():
        limit = request.args.get("limit", 50, type=int)
        offset = request.args.get("offset", 0, type=int)
        events = library.get_activity(limit=limit, offset=offset)
        total = library.count_activity()
        return jsonify({"events": events, "total": total})

    @bp.route("/api/library/tracked")
    def api_library_tracked():
        media_type = request.args.get("type", None)
        limit = request.args.get("limit", 50, type=int)
        offset = request.args.get("offset", 0, type=int)
        items = library.get_items(media_type=media_type, limit=limit, offset=offset)
        total = library.count_items(media_type=media_type)
        return jsonify({"items": items, "total": total})

    @bp.route("/api/import/csv", methods=["POST"])
    def api_import_csv():
        """Parse a Goodreads or StoryGraph CSV export and queue downloads."""
        f = request.files.get("csv_file")
        if not f:
            return jsonify({"error": "csv_file is required"}), 400

        shelf_filter = request.form.get("shelf", "to-read").lower()
        media_type = request.form.get("media_type", "ebook")
        limit = min(int(request.form.get("limit", 50)), 200)

        try:
            text = f.read().decode("utf-8-sig", errors="replace")
        except Exception as e:
            return jsonify({"error": f"Could not read file: {e}"}), 400

        reader = csv.DictReader(io.StringIO(text))
        headers = [h.lower().strip() for h in (reader.fieldnames or [])]
        is_goodreads = "exclusive shelf" in headers
        is_storygraph = "read status" in headers

        queued = []
        skipped = 0

        for row in reader:
            if len(queued) >= limit:
                break

            def _get(*keys):
                for k in keys:
                    for h, v in row.items():
                        if h.lower().strip() == k:
                            return (v or "").strip()
                return ""

            title = _get("title")
            author = _get("author", "authors")
            if not title:
                skipped += 1
                continue

            if is_goodreads:
                shelf = _get("exclusive shelf").lower()
                if shelf_filter and shelf != shelf_filter:
                    skipped += 1
                    continue
            elif is_storygraph:
                read_status = _get("read status").lower()
                sg_map = {"to-read": "to-read", "currently-reading": "reading", "read": "read"}
                mapped = sg_map.get(read_status, read_status)
                if shelf_filter and mapped != shelf_filter and read_status != shelf_filter:
                    skipped += 1
                    continue

            search_title = f"{title} {author}".strip()
            if library.find_by_title(title):
                skipped += 1
                continue

            job_id = str(uuid.uuid4())[:8]
            ctx["download_jobs"][job_id] = ctx["base_job_fields"](
                title,
                "csv_import",
                type="search_import",
                author=author,
                query=search_title,
                media_type=media_type,
            )
            queued.append({"job_id": job_id, "title": title, "author": author})

        if queued:
            threading.Thread(target=ctx["process_csv_import_jobs"], daemon=True).start()

        return jsonify({
            "queued": len(queued),
            "skipped": skipped,
            "items": queued,
            "format": "goodreads" if is_goodreads else "storygraph" if is_storygraph else "unknown",
        })

    @bp.route("/api/library/manga")
    def api_library_manga():
        if not config.has_kavita():
            return jsonify({"series": [], "total": 0, "page": 1, "pages": 1})
        page = int(request.args.get("page", 1))
        per_page = 24
        search = request.args.get("q", "").strip()
        try:
            # Authenticate with Kavita
            auth_resp = requests.post(
                f"{config.KAVITA_URL}/api/Plugin/authenticate",
                params={"apiKey": config.KAVITA_API_KEY, "pluginName": "Librarr"},
                timeout=10,
            )
            if auth_resp.status_code != 200:
                return jsonify({"series": [], "total": 0, "page": 1, "pages": 1, "error": "Kavita auth failed"})
            token = auth_resp.json().get("token", "")
            kvt_headers = {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}

            # Use the manga library ID, fall back to main library ID
            lib_id = config.KAVITA_MANGA_LIBRARY_ID or config.KAVITA_LIBRARY_ID
            if not lib_id:
                return jsonify({"series": [], "total": 0, "page": 1, "pages": 1, "error": "No Kavita library ID configured"})

            # Kavita v0.8+ Series endpoint with pagination
            body = {
                "statements": [],
                "combination": 1,
                "limitTo": 0,
                "sortOptions": {"sortField": 4, "isAscending": True},
            }
            if search:
                body["statements"] = [{"comparison": 6, "field": 1, "value": search}]

            resp = requests.post(
                f"{config.KAVITA_URL}/api/Series/v2?pageNumber={page}&pageSize={per_page}&libraryId={lib_id}",
                json=body,
                headers=kvt_headers,
                timeout=15,
            )

            if resp.status_code != 200:
                resp = requests.get(
                    f"{config.KAVITA_URL}/api/Series?libraryId={lib_id}&pageNumber={page}&pageSize={per_page}",
                    headers=kvt_headers,
                    timeout=15,
                )

            data = resp.json() if isinstance(resp.json(), list) else resp.json()
            total = 0
            try:
                import json as _json
                pag_header = resp.headers.get("Pagination", "{}")
                pag_data = _json.loads(pag_header)
                total = int(pag_data.get("totalItems", 0))
            except Exception:
                try:
                    total = int(resp.headers.get("X-Pagination-TotalItems", "0"))
                except (ValueError, TypeError):
                    total = 0

            series_list = data if isinstance(data, list) else data.get("results", data.get("series", []))
            series = []
            for s in series_list:
                series.append({
                    "id": s.get("id"),
                    "name": s.get("name", "Unknown"),
                    "sortName": s.get("sortName", ""),
                    "pages": s.get("pages", 0),
                    "cover_url": f"/api/manga/cover/{s.get('id')}",
                    "libraryId": s.get("libraryId", lib_id),
                })
            return jsonify({
                "series": series,
                "total": total if total else len(series),
                "page": page,
                "pages": max(1, ((total or len(series)) + per_page - 1) // per_page),
                "kavita_url": config.KAVITA_URL,
            })
        except Exception as e:
            logger.error("Manga library error: %s", e)
            return jsonify({"series": [], "total": 0, "page": 1, "pages": 1, "error": str(e)})

    @bp.route("/api/manga/cover/<int:series_id>")
    def api_manga_cover(series_id):
        if not config.has_kavita():
            return "", 404
        try:
            auth_resp = requests.post(
                f"{config.KAVITA_URL}/api/Plugin/authenticate",
                params={"apiKey": config.KAVITA_API_KEY, "pluginName": "Librarr"},
                timeout=10,
            )
            if auth_resp.status_code != 200:
                return "", 404
            token = auth_resp.json().get("token", "")
            resp = requests.get(
                f"{config.KAVITA_URL}/api/image/series-cover?seriesId={series_id}",
                headers={"Authorization": f"Bearer {token}"},
                timeout=10,
            )
            if resp.status_code == 200:
                return send_file(
                    BytesIO(resp.content),
                    mimetype=resp.headers.get("Content-Type", "image/jpeg"),
                )
        except Exception:
            pass
        return "", 404


    return bp

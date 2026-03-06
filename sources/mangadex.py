"""MangaDex — manga search and chapter download via public API."""
import logging

from .base import Source

logger = logging.getLogger("librarr")

MANGADEX_API = "https://api.mangadex.org"


class MangaDexSource(Source):
    name = "mangadex"
    label = "MangaDex"
    color = "#f47067"
    download_type = "custom"
    search_tab = "manga"

    def enabled(self):
        return True  # Public API, no key needed

    def search(self, query):
        import requests
        try:
            resp = requests.get(
                f"{MANGADEX_API}/manga",
                params={
                    "title": query,
                    "limit": 20,
                    "includes[]": "cover_art",
                    "availableTranslatedLanguage[]": "en",
                    "contentRating[]": ["safe", "suggestive", "erotica", "pornographic"],
                },
                timeout=15,
            )
            resp.raise_for_status()
            data = resp.json()
        except Exception as e:
            logger.warning(f"MangaDex search failed: {e}")
            return []

        results = []
        for item in data.get("data", []):
            attrs = item.get("attributes", {})
            manga_id = item.get("id", "")

            # Title (prefer English)
            title_map = attrs.get("title", {})
            title = title_map.get("en") or next(iter(title_map.values()), "Unknown")

            # Author from relationships
            author = ""
            for rel in item.get("relationships", []):
                if rel.get("type") == "author":
                    rel_attrs = rel.get("attributes") or {}
                    author = rel_attrs.get("name", "")
                    break

            # Cover art
            cover_url = ""
            for rel in item.get("relationships", []):
                if rel.get("type") == "cover_art":
                    rel_attrs = rel.get("attributes") or {}
                    fname = rel_attrs.get("fileName", "")
                    if fname:
                        cover_url = f"https://uploads.mangadex.org/covers/{manga_id}/{fname}.256.jpg"
                    break

            chapter_count = attrs.get("lastChapter") or "?"
            status = attrs.get("status", "")

            results.append({
                "title": title,
                "author": author,
                "manga_id": manga_id,
                "cover_url": cover_url,
                "chapter_count": chapter_count,
                "status": status,
                "source": self.name,
                "site": "MangaDex",
            })
        return results

    def download(self, result, job):
        from manga_workers import start_mangadex_download
        manga_id = result.get("manga_id", "")
        title = result.get("title", "Unknown")
        if not manga_id:
            job["error"] = "No manga ID"
            return False
        job["manga_id"] = manga_id
        return start_mangadex_download(job, manga_id, title)

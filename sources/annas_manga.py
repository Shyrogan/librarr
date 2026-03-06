"""Anna's Archive — CBZ/CBR manga/comic search and direct download."""
import logging
import re

import requests

from .base import Source

logger = logging.getLogger("librarr")

USER_AGENT = "Mozilla/5.0 (compatible; Librarr/1.0)"


class AnnasMangaSource(Source):
    name = "annas_manga"
    label = "Anna's Archive"
    color = "#a29bfe"
    download_type = "direct"
    search_tab = "manga"

    def enabled(self):
        return True

    def search(self, query):
        results = []
        for ext in ("cbz", "cbr"):
            try:
                resp = requests.get(
                    "https://annas-archive.li/search",
                    params={"q": query, "ext": ext},
                    headers={"User-Agent": USER_AGENT},
                    timeout=15,
                )
                if resp.status_code != 200:
                    continue
                html = resp.text
                blocks = re.findall(
                    r'<div class="flex\s+pt-3 pb-3 border-b.*?">(.*?)(?=<div class="flex\s+pt-3 pb-3 border-b|<footer)',
                    html, re.DOTALL,
                )
                for block in blocks[:10]:
                    md5 = re.search(r"/md5/([a-f0-9]+)", block)
                    if not md5:
                        continue
                    title_m = re.search(r"font-semibold text-lg[^>]*>(.*?)</a>", block, re.DOTALL)
                    author_m = re.search(r"user-edit[^>]*></span>\s*(.*?)</a>", block, re.DOTALL)
                    size_m = re.search(r"(\d+[\.\d]*\s*[KMG]i?B)", block)
                    title = re.sub(r"<[^>]+>", "", title_m.group(1)).strip() if title_m else ""
                    author = re.sub(r"<[^>]+>", "", author_m.group(1)).strip() if author_m else ""
                    if not title:
                        continue
                    results.append({
                        "title": title,
                        "author": author,
                        "size_human": size_m.group(1) if size_m else "",
                        "md5": md5.group(1),
                        "ext": ext,
                        "source": self.name,
                        "site": "Anna's Archive",
                    })
            except Exception as e:
                logger.warning(f"Anna's manga search ({ext}) failed: {e}")
        # Deduplicate by md5
        seen = set()
        deduped = []
        for r in results:
            if r["md5"] not in seen:
                seen.add(r["md5"])
                deduped.append(r)
        return deduped[:20]

    def download(self, result, job):
        from app import _download_from_annas
        md5 = result.get("md5", "")
        title = result.get("title", "Unknown")
        if not md5:
            job["error"] = "No MD5 hash"
            return False
        return _download_from_annas(job._job_id, md5, title, media_type="manga")

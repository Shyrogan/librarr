"""Nyaa.si — manga torrent search (Literature/English-translated category)."""
import logging
import xml.etree.ElementTree as ET

from .base import Source

logger = logging.getLogger("librarr")

NYAA_RSS = "https://nyaa.si/"
# Category 3_1 = Literature / English-translated (manga scanlations)
MANGA_CATEGORY = "3_1"


class NyaaMangaSource(Source):
    name = "nyaa_manga"
    label = "Nyaa"
    color = "#3e9e3e"
    download_type = "torrent"
    search_tab = "manga"

    def enabled(self):
        return True  # No config needed

    def search(self, query):
        import requests
        try:
            resp = requests.get(
                NYAA_RSS,
                params={"f": "0", "c": MANGA_CATEGORY, "q": query, "page": "rss"},
                timeout=20,
                headers={"User-Agent": "Librarr/1.0"},
            )
            resp.raise_for_status()
            root = ET.fromstring(resp.content)
        except Exception as e:
            logger.warning(f"Nyaa manga search failed: {e}")
            return []

        ns = {"nyaa": "https://nyaa.si/xmlns/nyaa"}
        results = []
        for item in root.findall(".//item"):
            title = (item.findtext("title") or "").strip()
            link = (item.findtext("link") or "").strip()
            magnet = item.findtext("nyaa:magnetUri", namespaces=ns) or ""
            seeders_txt = item.findtext("nyaa:seeders", namespaces=ns) or "0"
            size_txt = item.findtext("nyaa:size", namespaces=ns) or ""
            info_hash = item.findtext("nyaa:infoHash", namespaces=ns) or ""

            if not title:
                continue

            try:
                seeders = int(seeders_txt)
            except ValueError:
                seeders = 0

            results.append({
                "title": title,
                "download_url": magnet or link,
                "magnet_url": magnet,
                "info_hash": info_hash,
                "seeders": seeders,
                "size_human": size_txt,
                "indexer": "Nyaa",
                "site": "nyaa.si",
                "source": self.name,
            })
        return results[:20]

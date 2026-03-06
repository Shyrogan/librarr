"""Prowlarr — manga torrent indexer search."""
import config
from .base import Source


class ProwlarrMangaSource(Source):
    name = "prowlarr_manga"
    label = "Prowlarr"
    color = "#e17055"
    download_type = "torrent"
    search_tab = "manga"

    def enabled(self):
        return config.has_prowlarr()

    def search(self, query):
        from app import search_prowlarr_manga
        return search_prowlarr_manga(query)

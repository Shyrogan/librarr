"""Import targets — library apps that Librarr can send books to."""
import logging
import os
import re
import subprocess
import time

import requests

import config

logger = logging.getLogger("librarr")


def _safe_name(name, max_len=80):
    name = re.sub(r'[<>:"/\\|?*]', "", (name or ""))
    name = re.sub(r"\s+", " ", name).strip().strip(".")
    if len(name) > max_len:
        name = name[:max_len].rstrip()
    return name or "Unknown"


class CalibreTarget:
    """Import into Calibre-Web via docker exec calibredb add."""

    name = "calibre"
    label = "Calibre-Web"

    def enabled(self):
        return config.has_calibre()

    def import_book(self, file_path, title="", author="", media_type="ebook"):
        if media_type != "ebook":
            return None

        container_path = file_path
        if file_path.startswith("/books-incoming"):
            container_path = file_path.replace("/books-incoming", "/books/incoming", 1)
        else:
            container_path = file_path.replace(
                config.CALIBRE_LIBRARY, config.CALIBRE_LIBRARY_CONTAINER
            )
        try:
            result = subprocess.run(
                [
                    "docker", "exec", config.CALIBRE_CONTAINER,
                    "calibredb", "add", container_path,
                    "--library-path", config.CALIBRE_LIBRARY_CONTAINER,
                ],
                capture_output=True, text=True, timeout=120,
            )
            match = re.search(r"Added book ids: (\d+)", result.stdout)
            if match:
                book_id = match.group(1)
                if author or title:
                    meta_cmd = [
                        "docker", "exec", config.CALIBRE_CONTAINER,
                        "calibredb", "set_metadata", book_id,
                        "--library-path", config.CALIBRE_LIBRARY_CONTAINER,
                    ]
                    if author:
                        meta_cmd.extend(["--field", f"authors:{author}"])
                    if title:
                        meta_cmd.extend(["--field", f"title:{title}"])
                    subprocess.run(meta_cmd, capture_output=True, timeout=30)
                logger.info(f"Calibre import: {title} (ID: {book_id})")
                return {"calibre_id": book_id}
            logger.error(f"Calibre import failed: {result.stderr}")
            return None
        except Exception as e:
            logger.error(f"Calibre import error: {e}")
            return None

    def scan(self):
        pass  # calibredb add handles it

    def verify_import(self, file_path, title="", author="", media_type="ebook", import_result=None):
        if media_type != "ebook":
            return {"ok": None, "mode": "unsupported"}
        book_id = str((import_result or {}).get("calibre_id", "")).strip()
        if not book_id:
            return {"ok": False, "mode": "calibredb", "reason": "missing_calibre_id"}
        try:
            result = subprocess.run(
                [
                    "docker", "exec", config.CALIBRE_CONTAINER,
                    "calibredb", "show_metadata", book_id,
                    "--library-path", config.CALIBRE_LIBRARY_CONTAINER,
                ],
                capture_output=True, text=True, timeout=60,
            )
            ok = result.returncode == 0
            return {
                "ok": ok,
                "mode": "calibredb",
                "book_id": book_id,
                "reason": "" if ok else (result.stderr.strip() or "show_metadata_failed"),
            }
        except Exception as e:
            return {"ok": False, "mode": "calibredb", "book_id": book_id, "reason": str(e)}


class KavitaTarget:
    """Import into Kavita by triggering a library scan."""

    name = "kavita"
    label = "Kavita"

    def __init__(self):
        self._jwt_token = None

    def enabled(self):
        return config.has_kavita()

    def _authenticate(self, force=False):
        if self._jwt_token and not force:
            return self._jwt_token
        try:
            resp = requests.post(
                f"{config.KAVITA_URL}/api/Plugin/authenticate",
                params={"apiKey": config.KAVITA_API_KEY, "pluginName": "Librarr"},
                timeout=10,
            )
            if resp.status_code == 200:
                data = resp.json()
                self._jwt_token = data.get("token", "")
                return self._jwt_token
        except Exception as e:
            logger.error(f"Kavita auth failed: {e}")
        return None

    def _headers(self):
        token = self._authenticate()
        return {"Authorization": f"Bearer {token}", "Content-Type": "application/json"}

    def import_book(self, file_path, title="", author="", media_type="ebook"):
        if media_type == "manga":
            self.scan(library_id=config.KAVITA_MANGA_LIBRARY_ID)
            return {"kavita_manga_scanned": True}
        if media_type != "ebook":
            return None
        # File should already be copied to KAVITA_LIBRARY_PATH by the pipeline
        self.scan()
        return {"kavita_scanned": True}

    def verify_import(self, file_path, title="", author="", media_type="ebook", import_result=None):
        if media_type == "manga":
            if not config.KAVITA_MANGA_LIBRARY_PATH:
                return {"ok": None, "mode": "filesystem", "reason": "kavita_manga_library_path_not_configured"}
            safe_series = _safe_name(title or "Unknown")
            fname = os.path.basename(file_path)
            expected = os.path.join(config.KAVITA_MANGA_LIBRARY_PATH, safe_series, fname)
            return {"ok": os.path.exists(expected), "mode": "filesystem", "path": expected}
        if media_type != "ebook":
            return {"ok": None, "mode": "unsupported"}
        if not config.KAVITA_LIBRARY_PATH:
            return {"ok": None, "mode": "filesystem", "reason": "kavita_library_path_not_configured"}
        safe_author = _safe_name(author or "Unknown")
        safe_title = _safe_name(title or "Unknown")
        ext = os.path.splitext(file_path)[1].lower() or ".epub"
        expected = os.path.join(config.KAVITA_LIBRARY_PATH, safe_author, safe_title, f"{safe_title}{ext}")
        return {"ok": os.path.exists(expected), "mode": "filesystem", "path": expected}

    def scan(self, library_id=None):
        lib_id = library_id or config.KAVITA_LIBRARY_ID
        if not lib_id:
            return
        try:
            resp = requests.post(
                f"{config.KAVITA_URL}/api/Library/scan",
                headers=self._headers(),
                params={"libraryId": int(lib_id)},
                timeout=10,
            )
            if resp.status_code == 401:
                # Token expired — re-authenticate and retry once
                self._authenticate(force=True)
                resp = requests.post(
                    f"{config.KAVITA_URL}/api/Library/scan",
                    headers=self._headers(),
                    params={"libraryId": int(lib_id)},
                    timeout=10,
                )
            if resp.status_code == 200:
                logger.info("Kavita library scan triggered")
            else:
                logger.warning(f"Kavita scan returned HTTP {resp.status_code}")
        except Exception as e:
            logger.error(f"Kavita scan failed: {e}")


class AudiobookshelfTarget:
    """Trigger Audiobookshelf library scan."""

    name = "audiobookshelf"
    label = "Audiobookshelf"

    def enabled(self):
        return config.has_audiobookshelf()

    def import_book(self, file_path, title="", author="", media_type="ebook"):
        if media_type == "ebook" and config.ABS_EBOOK_LIBRARY_ID:
            self._scan_library(config.ABS_EBOOK_LIBRARY_ID)
            return {"abs_ebook_scanned": True}
        elif media_type == "audiobook" and config.ABS_LIBRARY_ID:
            self._scan_library(config.ABS_LIBRARY_ID)
            return {"abs_audiobook_scanned": True}
        return None

    def _scan_library(self, library_id):
        try:
            requests.post(
                f"{config.ABS_URL}/api/libraries/{library_id}/scan",
                headers={"Authorization": f"Bearer {config.ABS_TOKEN}"},
                timeout=10,
            )
            logger.info(f"Audiobookshelf scan triggered: {library_id}")
        except Exception as e:
            logger.error(f"ABS scan failed: {e}")

    def scan(self):
        if config.ABS_EBOOK_LIBRARY_ID:
            self._scan_library(config.ABS_EBOOK_LIBRARY_ID)
        if config.ABS_LIBRARY_ID:
            self._scan_library(config.ABS_LIBRARY_ID)

    def verify_import(self, file_path, title="", author="", media_type="ebook", import_result=None):
        # ABS indexing is async and API search varies by version; verify the handoff path exists.
        deadline = time.time() + 5
        while time.time() < deadline:
            if os.path.exists(file_path):
                return {"ok": True, "mode": "filesystem", "path": file_path}
            time.sleep(0.25)
        return {"ok": False, "mode": "filesystem", "path": file_path, "reason": "path_missing"}


class KomgaTarget:
    """Import manga/comics into Komga by triggering a library scan."""

    name = "komga"
    label = "Komga"

    def enabled(self):
        return config.has_komga()

    def _auth(self):
        return (config.KOMGA_USERNAME, config.KOMGA_PASSWORD)

    def import_book(self, file_path, title="", author="", media_type="ebook"):
        if media_type != "manga":
            return None
        if config.KOMGA_LIBRARY_PATH:
            try:
                safe_series = _safe_name(title or "Unknown")
                dest_dir = os.path.join(config.KOMGA_LIBRARY_PATH, safe_series)
                os.makedirs(dest_dir, exist_ok=True)
                import shutil
                dest = os.path.join(dest_dir, os.path.basename(file_path))
                if os.path.abspath(file_path) != os.path.abspath(dest):
                    shutil.copy2(file_path, dest)
                    logger.info(f"Copied to Komga library: {dest}")
            except Exception as e:
                logger.warning(f"Komga copy failed: {e}")
        self._scan()
        return {"komga_scanned": True}

    def _scan(self):
        if not config.KOMGA_LIBRARY_ID:
            return
        try:
            resp = requests.post(
                f"{config.KOMGA_URL}/api/v1/libraries/{config.KOMGA_LIBRARY_ID}/scan",
                auth=self._auth(),
                timeout=15,
            )
            if resp.status_code in (200, 202):
                logger.info("Komga library scan triggered")
            else:
                logger.warning(f"Komga scan returned HTTP {resp.status_code}")
        except Exception as e:
            logger.error(f"Komga scan failed: {e}")

    def verify_import(self, file_path, title="", author="", media_type="ebook", import_result=None):
        if media_type != "manga":
            return {"ok": None, "mode": "unsupported"}
        if not config.KOMGA_LIBRARY_PATH:
            return {"ok": None, "mode": "filesystem", "reason": "komga_library_path_not_configured"}
        safe_series = _safe_name(title or "Unknown")
        fname = os.path.basename(file_path)
        expected = os.path.join(config.KOMGA_LIBRARY_PATH, safe_series, fname)
        return {"ok": os.path.exists(expected), "mode": "filesystem", "path": expected}

    def scan(self):
        self._scan()


# --- Target Registry ---

ALL_TARGETS = {
    "calibre": CalibreTarget(),
    "kavita": KavitaTarget(),
    "audiobookshelf": AudiobookshelfTarget(),
    "komga": KomgaTarget(),
}


def get_enabled_targets():
    """Return list of enabled target instances whose names are in ENABLED_TARGETS."""
    enabled_names = config.get_enabled_target_names()
    return [t for t in ALL_TARGETS.values() if t.enabled() and t.name in enabled_names]


def get_target(name):
    """Get a target by name."""
    return ALL_TARGETS.get(name)

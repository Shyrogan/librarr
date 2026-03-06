"""MangaDex chapter download worker — images → per-chapter CBZ, then volume CBZ merge."""
from __future__ import annotations

import logging
import os
import shutil
import tempfile
import time
import zipfile
from collections import defaultdict

import requests

logger = logging.getLogger("librarr")

MANGADEX_API = "https://api.mangadex.org"
REQUEST_DELAY = 0.5  # seconds between requests (be polite to the API)


def _fetch_chapter_list(manga_id: str) -> list[dict]:
    """Fetch all English chapters for a manga, sorted by chapter number."""
    chapters = []
    offset = 0
    limit = 100
    while True:
        try:
            resp = requests.get(
                f"{MANGADEX_API}/manga/{manga_id}/feed",
                params={
                    "translatedLanguage[]": "en",
                    "order[chapter]": "asc",
                    "limit": limit,
                    "offset": offset,
                    "includes[]": "scanlation_group",
                },
                timeout=20,
            )
            resp.raise_for_status()
            data = resp.json()
        except Exception as e:
            logger.error(f"MangaDex chapter list fetch failed: {e}")
            break

        batch = data.get("data", [])
        if not batch:
            break
        chapters.extend(batch)
        total = data.get("total", 0)
        offset += len(batch)
        if offset >= total:
            break
        time.sleep(REQUEST_DELAY)
    return chapters


def _fetch_chapter_images(chapter_id: str) -> list[str]:
    """Get image URLs for a chapter via the MangaDex@Home API."""
    try:
        resp = requests.get(
            f"{MANGADEX_API}/at-home/server/{chapter_id}",
            timeout=15,
        )
        resp.raise_for_status()
        data = resp.json()
        base = data["baseUrl"]
        chapter_data = data["chapter"]
        hash_ = chapter_data["hash"]
        pages = chapter_data.get("data", [])
        return [f"{base}/data/{hash_}/{p}" for p in pages]
    except Exception as e:
        logger.error(f"MangaDex@Home fetch failed for {chapter_id}: {e}")
        return []


def _download_images_to_dir(image_urls: list[str], dest_dir: str) -> int:
    """Download all images into dest_dir. Returns number of images downloaded."""
    os.makedirs(dest_dir, exist_ok=True)
    downloaded = 0
    for i, url in enumerate(image_urls):
        ext = os.path.splitext(url.split("?")[0])[1] or ".jpg"
        fname = f"{i + 1:04d}{ext}"
        dest = os.path.join(dest_dir, fname)
        try:
            r = requests.get(url, timeout=30, stream=True)
            r.raise_for_status()
            with open(dest, "wb") as f:
                for chunk in r.iter_content(chunk_size=65536):
                    f.write(chunk)
            downloaded += 1
        except Exception as e:
            logger.warning(f"Failed to download image {url}: {e}")
        time.sleep(0.1)
    return downloaded


def _pack_cbz(image_dir: str, cbz_path: str) -> bool:
    """Pack all images from image_dir into a CBZ file."""
    images = sorted(
        f for f in os.listdir(image_dir)
        if f.lower().endswith((".jpg", ".jpeg", ".png", ".gif", ".webp"))
    )
    if not images:
        return False
    try:
        with zipfile.ZipFile(cbz_path, "w", zipfile.ZIP_DEFLATED) as zf:
            for img in images:
                zf.write(os.path.join(image_dir, img), img)
        return True
    except Exception as e:
        logger.error(f"CBZ pack failed: {e}")
        return False


def _merge_chapters_to_volume(chapter_cbz_paths: list[str], volume_cbz_path: str) -> bool:
    """Merge multiple chapter CBZs into a single volume CBZ.

    Images are re-numbered globally across chapters to keep reading order.
    """
    try:
        with zipfile.ZipFile(volume_cbz_path, "w", zipfile.ZIP_DEFLATED) as vol_zf:
            global_idx = 1
            for cbz_path in chapter_cbz_paths:
                try:
                    with zipfile.ZipFile(cbz_path, "r") as ch_zf:
                        for name in sorted(ch_zf.namelist()):
                            ext = os.path.splitext(name)[1] or ".jpg"
                            with ch_zf.open(name) as img_f:
                                vol_zf.writestr(f"{global_idx:05d}{ext}", img_f.read())
                            global_idx += 1
                except Exception as e:
                    logger.warning(f"Skipping chapter CBZ {cbz_path}: {e}")
        return True
    except Exception as e:
        logger.error(f"Volume merge failed: {e}")
        return False


def _safe_name(name: str) -> str:
    import re
    name = re.sub(r'[<>:"/\\|?*]', "", name)
    name = re.sub(r"\s+", " ", name).strip().strip(".")
    return name[:80] or "Unknown"


def start_mangadex_download(job, manga_id: str, title: str) -> bool:
    """Download all English chapters of a manga, pack as CBZ, merge into volumes."""
    import config
    import pipeline

    safe_title = _safe_name(title)
    series_dir = os.path.join(config.MANGA_ORGANIZED_DIR, safe_title)
    os.makedirs(series_dir, exist_ok=True)

    job["status"] = "downloading"
    job["detail"] = "Fetching chapter list from MangaDex…"

    chapters = _fetch_chapter_list(manga_id)
    if not chapters:
        job["status"] = "error"
        job["error"] = "No English chapters found on MangaDex"
        return False

    # Deduplicate by chapter number (keep first scanlation group per chapter)
    seen_chapters: dict[str, dict] = {}
    for ch in chapters:
        attrs = ch.get("attributes", {})
        ch_num = attrs.get("chapter") or attrs.get("translatedLanguage", "?")
        if ch_num not in seen_chapters:
            seen_chapters[ch_num] = ch
    chapters = list(seen_chapters.values())

    total = len(chapters)
    job["detail"] = f"Downloading {total} chapters…"
    logger.info(f"MangaDex: {total} chapters for '{title}' ({manga_id})")

    # Group chapter IDs by volume for merging later
    volume_chapters: dict[str, list[tuple[str, str]]] = defaultdict(list)
    # (volume_label -> [(chapter_num, cbz_path)])

    with tempfile.TemporaryDirectory() as tmpdir:
        completed_cbzs: list[str] = []

        for idx, ch in enumerate(chapters, 1):
            attrs = ch.get("attributes", {})
            ch_id = ch.get("id", "")
            ch_num = attrs.get("chapter") or str(idx)
            volume = attrs.get("volume") or "no-volume"

            try:
                ch_num_float = float(ch_num)
                ch_label = f"{ch_num_float:07.2f}"
            except (ValueError, TypeError):
                ch_label = f"{idx:04d}"

            cbz_name = f"{safe_title} - Chapter {ch_label}.cbz"
            cbz_path = os.path.join(series_dir, cbz_name)

            job["detail"] = f"Chapter {ch_num} ({idx}/{total})…"

            # Skip if already downloaded
            if os.path.exists(cbz_path):
                volume_chapters[volume].append((ch_label, cbz_path))
                completed_cbzs.append(cbz_path)
                continue

            image_urls = _fetch_chapter_images(ch_id)
            if not image_urls:
                logger.warning(f"No images for chapter {ch_num}, skipping")
                continue

            img_dir = os.path.join(tmpdir, f"ch_{idx}")
            downloaded = _download_images_to_dir(image_urls, img_dir)
            if downloaded == 0:
                logger.warning(f"No images downloaded for chapter {ch_num}")
                continue

            if _pack_cbz(img_dir, cbz_path):
                logger.info(f"Created: {cbz_name}")
                volume_chapters[volume].append((ch_label, cbz_path))
                completed_cbzs.append(cbz_path)
                shutil.rmtree(img_dir, ignore_errors=True)

            time.sleep(REQUEST_DELAY)

    # Merge chapters into volume CBZs (only when volume info is available)
    volume_cbzs: list[str] = []
    for vol_label, ch_list in volume_chapters.items():
        if vol_label == "no-volume" or not ch_list:
            continue
        # Sort chapters within the volume
        ch_list_sorted = sorted(ch_list, key=lambda x: x[0])
        cbz_paths = [p for _, p in ch_list_sorted]

        try:
            vol_num = float(vol_label)
            vol_name_num = f"{vol_num:04.1f}"
        except (ValueError, TypeError):
            vol_name_num = vol_label

        vol_cbz_name = f"{safe_title} - Volume {vol_name_num}.cbz"
        vol_cbz_path = os.path.join(series_dir, vol_cbz_name)

        if not os.path.exists(vol_cbz_path):
            job["detail"] = f"Merging Volume {vol_label}…"
            if _merge_chapters_to_volume(cbz_paths, vol_cbz_path):
                logger.info(f"Created volume CBZ: {vol_cbz_name}")
                volume_cbzs.append(vol_cbz_path)

    job["detail"] = "Running pipeline…"

    # Run pipeline for each CBZ produced (chapters + volumes)
    all_cbzs = completed_cbzs + volume_cbzs
    if not all_cbzs:
        job["status"] = "error"
        job["error"] = "No CBZ files were created"
        return False

    from app import library as library_db

    for cbz_path in all_cbzs:
        if not os.path.exists(cbz_path):
            continue
        try:
            pipeline.run_pipeline(
                cbz_path,
                title=title,
                author="",
                media_type="manga",
                source="mangadex",
                source_id=f"mangadex:{manga_id}:{os.path.basename(cbz_path)}",
                job_id=job._job_id,
                library_db=library_db,
            )
        except Exception as e:
            logger.error(f"Pipeline error for {cbz_path}: {e}")

    job["status"] = "completed"
    job["detail"] = f"Downloaded {len(completed_cbzs)} chapters, {len(volume_cbzs)} volumes"
    return True

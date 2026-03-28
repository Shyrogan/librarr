package download

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/librarr/internal/config"
	"github.com/JeremiahM37/librarr/internal/db"
	"github.com/JeremiahM37/librarr/internal/models"
	"github.com/JeremiahM37/librarr/internal/organize"
	"github.com/JeremiahM37/librarr/internal/search"
)

// Watcher monitors qBittorrent for completed torrents and runs the import pipeline.
type Watcher struct {
	cfg        *config.Config
	db         *db.DB
	qb         *QBittorrentClient
	organizer  *organize.Organizer
	targets    *organize.LibraryTargets
	health     *search.HealthTracker

	processing sync.Map // hash -> struct{}, tracks in-progress imports
	imported   sync.Map // hash -> struct{}, tracks already-imported hashes
}

// NewWatcher creates a new torrent completion watcher.
func NewWatcher(cfg *config.Config, database *db.DB, qb *QBittorrentClient, organizer *organize.Organizer, targets *organize.LibraryTargets, health *search.HealthTracker) *Watcher {
	return &Watcher{
		cfg:       cfg,
		db:        database,
		qb:        qb,
		organizer: organizer,
		targets:   targets,
		health:    health,
	}
}

// Start begins the background watcher loop. It blocks until ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) {
	if !w.cfg.HasQBittorrent() {
		slog.Info("torrent watcher disabled (qBittorrent not configured)")
		return
	}

	slog.Info("torrent completion watcher started", "interval", "30s")
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Run once immediately.
	w.checkCompleted()

	for {
		select {
		case <-ctx.Done():
			slog.Info("torrent watcher stopping")
			return
		case <-ticker.C:
			w.checkCompleted()
		}
	}
}

func (w *Watcher) checkCompleted() {
	categories := []struct {
		name      string
		mediaType string
	}{
		{w.cfg.QBCategory, "ebook"},
		{w.cfg.QBAudiobookCategory, "audiobook"},
		{w.cfg.QBMangaCategory, "manga"},
	}

	for _, cat := range categories {
		torrents, err := w.qb.GetTorrents(cat.name)
		if err != nil {
			continue
		}
		for _, t := range torrents {
			if t.Progress < 1.0 {
				continue
			}

			// Skip already imported.
			if _, ok := w.imported.Load(t.Hash); ok {
				continue
			}
			// Skip currently processing.
			if _, loaded := w.processing.LoadOrStore(t.Hash, struct{}{}); loaded {
				continue
			}

			go w.importTorrent(t, cat.mediaType)
		}
	}
}

func (w *Watcher) importTorrent(t TorrentInfo, mediaType string) {
	defer w.processing.Delete(t.Hash)

	slog.Info("importing completed torrent", "name", t.Name, "hash", t.Hash, "type", mediaType)

	savePath := w.resolveLocalPath(t, mediaType)

	var importErr error
	switch mediaType {
	case "ebook":
		importErr = w.importEbook(t, savePath)
	case "audiobook":
		importErr = w.importAudiobook(t, savePath)
	case "manga":
		importErr = w.importManga(t, savePath)
	}

	if importErr != nil {
		slog.Error("torrent import failed", "name", t.Name, "type", mediaType, "error", importErr)
		return
	}

	// Remove torrent from qBit (keep files).
	if err := w.qb.DeleteTorrent(t.Hash, false); err != nil {
		slog.Warn("failed to remove torrent after import", "hash", t.Hash, "error", err)
	} else {
		slog.Info("removed completed torrent", "name", t.Name)
	}

	// Mark as imported.
	w.imported.Store(t.Hash, struct{}{})

	// Log the import.
	_ = w.db.LogEvent("torrent_import", t.Name, fmt.Sprintf("Imported %s from torrent", mediaType), nil, t.Hash)
}

// resolveLocalPath maps qBittorrent container paths to local paths.
func (w *Watcher) resolveLocalPath(t TorrentInfo, mediaType string) string {
	// Use content_path if available via the name field; otherwise use category save paths.
	// Since we only get basic TorrentInfo, construct from category config.
	switch mediaType {
	case "ebook":
		return filepath.Join(w.cfg.IncomingDir, t.Name)
	case "audiobook":
		return filepath.Join(w.cfg.AudiobookDir, t.Name)
	case "manga":
		return filepath.Join(w.cfg.MangaIncomingDir, t.Name)
	default:
		return filepath.Join(w.cfg.IncomingDir, t.Name)
	}
}

func (w *Watcher) importEbook(t TorrentInfo, savePath string) error {
	bookFiles := findFilesByExt(savePath, []string{".epub", ".mobi", ".pdf", ".azw3"})
	if len(bookFiles) == 0 {
		return fmt.Errorf("no ebook files found at %s", savePath)
	}

	for _, bf := range bookFiles {
		destPath, err := w.organizer.OrganizeEbook(bf, t.Name, "")
		if err != nil {
			slog.Warn("organize ebook failed", "file", bf, "error", err)
			destPath = bf
		}

		// Try to extract author from EPUB metadata.
		author := ""
		if strings.HasSuffix(strings.ToLower(destPath), ".epub") {
			if meta, err := organize.ExtractEPUBMeta(destPath); err == nil && meta.Author != "" {
				author = meta.Author
			}
		}

		w.db.AddItem(&models.LibraryItem{
			Title:     t.Name,
			Author:    author,
			FilePath:  destPath,
			FileSize:  t.TotalSize,
			MediaType: "ebook",
			Source:    "torrent",
			SourceID:  t.Hash,
		})

		// Import to external libraries.
		w.targets.ImportEbook(destPath, t.Name, author)
	}

	return nil
}

func (w *Watcher) importAudiobook(t TorrentInfo, savePath string) error {
	// Extract author from torrent name if possible.
	author := ""
	title := t.Name
	if strings.Contains(title, " - ") {
		parts := strings.SplitN(title, " - ", 2)
		author = strings.TrimSpace(parts[0])
		title = strings.TrimSpace(parts[1])
	}
	if author == "" {
		author = "Unknown"
	}

	destPath, err := w.organizer.OrganizeAudiobook(savePath, title, author)
	if err != nil {
		slog.Warn("organize audiobook failed", "path", savePath, "error", err)
		destPath = savePath
	}

	w.db.AddItem(&models.LibraryItem{
		Title:     title,
		Author:    author,
		FilePath:  destPath,
		FileSize:  t.TotalSize,
		MediaType: "audiobook",
		Source:    "torrent",
		SourceID:  t.Hash,
	})

	w.targets.ImportAudiobook()

	return nil
}

func (w *Watcher) importManga(t TorrentInfo, savePath string) error {
	mangaFiles := findFilesByExt(savePath, []string{".cbz", ".cbr", ".zip", ".pdf", ".epub"})
	if len(mangaFiles) == 0 {
		return fmt.Errorf("no manga files found at %s", savePath)
	}

	for _, mf := range mangaFiles {
		destPath, err := w.organizer.OrganizeManga(mf, t.Name)
		if err != nil {
			slog.Warn("organize manga failed", "file", mf, "error", err)
			destPath = mf
		}

		w.db.AddItem(&models.LibraryItem{
			Title:     t.Name,
			FilePath:  destPath,
			FileSize:  t.TotalSize,
			MediaType: "manga",
			Source:    "torrent",
			SourceID:  t.Hash,
		})

		w.targets.ImportManga(destPath, t.Name)
	}

	return nil
}

// findFilesByExt recursively finds files with given extensions.
func findFilesByExt(root string, exts []string) []string {
	var files []string

	info, err := os.Stat(root)
	if err != nil {
		return files
	}

	if !info.IsDir() {
		lower := strings.ToLower(root)
		for _, ext := range exts {
			if strings.HasSuffix(lower, ext) {
				return []string{root}
			}
		}
		return files
	}

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		lower := strings.ToLower(path)
		for _, ext := range exts {
			if strings.HasSuffix(lower, ext) {
				files = append(files, path)
				break
			}
		}
		return nil
	})

	return files
}

package download

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/librarr/internal/config"
	"github.com/JeremiahM37/librarr/internal/db"
	"github.com/JeremiahM37/librarr/internal/models"
	"github.com/JeremiahM37/librarr/internal/organize"
	"github.com/JeremiahM37/librarr/internal/search"
)

// Manager coordinates downloads, background jobs, and the post-download pipeline.
type Manager struct {
	cfg        *config.Config
	db         *db.DB
	qb         *QBittorrentClient
	sab        *SABnzbdClient
	direct     *DirectDownloader
	organizer  *organize.Organizer
	targets    *organize.LibraryTargets
	health     *search.HealthTracker

	mu   sync.Mutex
	jobs map[string]*models.DownloadJob
}

// NewManager creates a download manager.
func NewManager(cfg *config.Config, database *db.DB, qb *QBittorrentClient, sab *SABnzbdClient, direct *DirectDownloader, organizer *organize.Organizer, targets *organize.LibraryTargets, health *search.HealthTracker) *Manager {
	m := &Manager{
		cfg:       cfg,
		db:        database,
		qb:        qb,
		sab:       sab,
		direct:    direct,
		organizer: organizer,
		targets:   targets,
		health:    health,
		jobs:      make(map[string]*models.DownloadJob),
	}

	// Load existing jobs from database.
	existingJobs, err := database.GetJobs()
	if err == nil {
		for _, j := range existingJobs {
			j := j
			m.jobs[j.ID] = &j
		}
		if len(existingJobs) > 0 {
			slog.Info("loaded existing download jobs", "count", len(existingJobs))
		}
	}

	return m
}

// StartAnnasDownload starts a background download from Anna's Archive.
func (m *Manager) StartAnnasDownload(md5, title string) (*models.DownloadJob, error) {
	job := m.createJob(title, "annas", fmt.Sprintf("https://%s/md5/%s", m.cfg.AnnasArchiveDomain, md5))
	job.MD5 = md5
	job.MediaType = "ebook"

	if err := m.db.SaveJob(job); err != nil {
		return nil, err
	}

	go m.runAnnasDownload(job)
	return job, nil
}

// StartTorrentDownload adds a torrent to qBittorrent.
func (m *Manager) StartTorrentDownload(torrentURL, title, savePath, category string) error {
	return m.qb.AddTorrent(torrentURL, title, savePath, category)
}

// StartNZBDownload sends an NZB URL to SABnzbd.
func (m *Manager) StartNZBDownload(nzbURL, title string) (string, error) {
	if m.sab == nil {
		return "", fmt.Errorf("SABnzbd not configured")
	}
	return m.sab.AddNZB(nzbURL, title)
}

// StartDirectDownload starts a background download from a direct URL.
func (m *Manager) StartDirectDownload(fileURL, title, source, sourceID string) (*models.DownloadJob, error) {
	job := m.createJob(title, source, fileURL)
	job.MediaType = "ebook"

	if err := m.db.SaveJob(job); err != nil {
		return nil, err
	}

	go m.runDirectDownload(job, fileURL, sourceID)
	return job, nil
}

func (m *Manager) createJob(title, source, url string) *models.DownloadJob {
	id := fmt.Sprintf("%x", time.Now().UnixNano()%0xFFFFFFFF)[:8]

	job := &models.DownloadJob{
		ID:         id,
		Title:      title,
		Source:     source,
		Status:     "queued",
		URL:        url,
		MediaType:  "ebook",
		MaxRetries: m.cfg.MaxRetries,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	return job
}

// validTransitions defines which status transitions are allowed.
var validTransitions = map[string]map[string]bool{
	"queued":      {"searching": true, "downloading": true, "error": true, "dead_letter": true},
	"searching":   {"downloading": true, "error": true, "dead_letter": true, "queued": true},
	"downloading": {"importing": true, "error": true, "dead_letter": true, "retry_wait": true, "completed": true},
	"importing":   {"completed": true, "error": true, "dead_letter": true},
	"retry_wait":  {"downloading": true, "searching": true, "queued": true, "error": true, "dead_letter": true},
	"error":       {"queued": true, "dead_letter": true}, // manual retry or dead letter
	"dead_letter": {"queued": true},                       // manual retry only
	"completed":   {},                                     // terminal state
}

func (m *Manager) updateJob(job *models.DownloadJob, status, detail, errMsg string) {
	// Validate transition.
	if allowed, ok := validTransitions[job.Status]; ok {
		if !allowed[status] && status != job.Status {
			slog.Warn("invalid status transition rejected",
				"job_id", job.ID,
				"from", job.Status,
				"to", status,
			)
			return
		}
	}

	// Record status history (keep last 25).
	transition := models.StatusTransition{
		From:      job.Status,
		To:        status,
		Detail:    detail,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	job.StatusHistory = append(job.StatusHistory, transition)
	if len(job.StatusHistory) > 25 {
		job.StatusHistory = job.StatusHistory[len(job.StatusHistory)-25:]
	}

	job.Status = status
	job.Detail = detail
	job.Error = errMsg
	job.UpdatedAt = time.Now()
	_ = m.db.UpdateJobStatus(job.ID, status, detail, errMsg)
}

// RetryDeadLetterJob manually retries a dead letter job.
func (m *Manager) RetryDeadLetterJob(jobID string) error {
	m.mu.Lock()
	job, ok := m.jobs[jobID]
	m.mu.Unlock()

	if !ok {
		// Try from DB.
		dbJob, err := m.db.GetJob(jobID)
		if err != nil {
			return fmt.Errorf("job not found: %s", jobID)
		}
		job = dbJob
		m.mu.Lock()
		m.jobs[jobID] = job
		m.mu.Unlock()
	}

	if job.Status != "dead_letter" && job.Status != "error" {
		return fmt.Errorf("job %s is in status %s, not dead_letter or error", jobID, job.Status)
	}

	job.RetryCount = 0
	m.updateJob(job, "queued", "Manual retry", "")

	// Restart download based on source.
	if job.MD5 != "" {
		go m.runAnnasDownload(job)
	} else if job.URL != "" {
		go m.runDirectDownload(job, job.URL, "")
	}

	return nil
}

func (m *Manager) runAnnasDownload(job *models.DownloadJob) {
	m.updateJob(job, "downloading", "Downloading from Anna's Archive...", "")

	filePath, fileSize, err := m.direct.DownloadFromAnnas(job.MD5, job.Title, func(detail string) {
		job.Detail = detail
		job.UpdatedAt = time.Now()
	})
	if err != nil {
		slog.Error("anna's archive download failed", "title", job.Title, "error", err)
		m.health.RecordFailure("annas", err.Error(), "download")
		if job.RetryCount < job.MaxRetries {
			job.RetryCount++
			m.updateJob(job, "retry_wait", fmt.Sprintf("Retry %d/%d scheduled", job.RetryCount, job.MaxRetries), err.Error())
			go func() {
				time.Sleep(time.Duration(m.cfg.RetryBackoffSeconds) * time.Second)
				m.runAnnasDownload(job)
			}()
		} else {
			m.updateJob(job, "dead_letter", "Max retries exceeded", err.Error())
		}
		return
	}

	m.health.RecordSuccess("annas", "download")

	// Run post-download pipeline.
	m.updateJob(job, "importing", "Organizing file...", "")

	// Try to extract author from EPUB metadata.
	author := ""
	if strings.HasSuffix(strings.ToLower(filePath), ".epub") {
		if meta, err := organize.ExtractEPUBMeta(filePath); err == nil && meta.Author != "" {
			author = meta.Author
		}
	}

	destPath, err := m.organizer.OrganizeEbook(filePath, job.Title, author)
	if err != nil {
		slog.Warn("organize failed, keeping in place", "error", err)
		destPath = filePath
	}

	// Record in library.
	_, _ = m.db.AddItem(&models.LibraryItem{
		Title:        job.Title,
		Author:       author,
		FilePath:     destPath,
		OriginalPath: filePath,
		FileSize:     fileSize,
		FileFormat:   "epub",
		MediaType:    "ebook",
		Source:       "annas",
		SourceID:     job.MD5,
	})

	// Trigger library imports.
	if m.targets != nil {
		m.targets.ImportEbook(destPath, job.Title, author)
	}

	_ = m.db.LogEvent("download_complete", job.Title, fmt.Sprintf("Downloaded from Anna's Archive (%s)", search.HumanSize(fileSize)), nil, job.ID)

	m.updateJob(job, "completed", fmt.Sprintf("Done (%s)", search.HumanSize(fileSize)), "")
	slog.Info("download completed", "title", job.Title, "source", "annas", "size", fileSize)
}

func (m *Manager) runDirectDownload(job *models.DownloadJob, fileURL, sourceID string) {
	m.updateJob(job, "downloading", "Downloading...", "")

	filePath, fileSize, err := m.direct.DownloadFromURL(fileURL, job.Title, func(detail string) {
		job.Detail = detail
		job.UpdatedAt = time.Now()
	})
	if err != nil {
		slog.Error("direct download failed", "title", job.Title, "error", err)
		m.updateJob(job, "error", "", err.Error())
		return
	}

	m.updateJob(job, "importing", "Organizing file...", "")

	// Try to extract author from EPUB metadata.
	author := ""
	if strings.HasSuffix(strings.ToLower(filePath), ".epub") {
		if meta, err := organize.ExtractEPUBMeta(filePath); err == nil && meta.Author != "" {
			author = meta.Author
		}
	}

	destPath, err := m.organizer.OrganizeEbook(filePath, job.Title, author)
	if err != nil {
		slog.Warn("organize failed, keeping in place", "error", err)
		destPath = filePath
	}

	_, _ = m.db.AddItem(&models.LibraryItem{
		Title:        job.Title,
		Author:       author,
		FilePath:     destPath,
		OriginalPath: filePath,
		FileSize:     fileSize,
		FileFormat:   "epub",
		MediaType:    "ebook",
		Source:       job.Source,
		SourceID:     sourceID,
	})

	// Trigger library imports.
	if m.targets != nil {
		m.targets.ImportEbook(destPath, job.Title, author)
	}

	_ = m.db.LogEvent("download_complete", job.Title, fmt.Sprintf("Downloaded (%s)", search.HumanSize(fileSize)), nil, job.ID)

	m.updateJob(job, "completed", fmt.Sprintf("Done (%s)", search.HumanSize(fileSize)), "")
	slog.Info("download completed", "title", job.Title, "source", job.Source, "size", fileSize)
}

// GetDownloads returns combined download status from qBittorrent and background jobs.
func (m *Manager) GetDownloads() []models.DownloadStatus {
	var downloads []models.DownloadStatus

	// qBittorrent torrents.
	if m.cfg.HasQBittorrent() {
		for _, cat := range []struct {
			name  string
			label string
		}{
			{m.cfg.QBCategory, "torrent"},
			{m.cfg.QBAudiobookCategory, "audiobook"},
			{m.cfg.QBMangaCategory, "manga"},
		} {
			torrents, err := m.qb.GetTorrents(cat.name)
			if err != nil {
				continue
			}
			for _, t := range torrents {
				downloads = append(downloads, models.DownloadStatus{
					Source:   cat.label,
					Title:    t.Name,
					Status:   MapTorrentStatus(t.State),
					Progress: float64(int(t.Progress*1000)) / 10, // round to 1 decimal
					Size:     search.HumanSize(t.TotalSize),
					Speed:    search.HumanSize(t.DlSpeed) + "/s",
					Hash:     t.Hash,
				})
			}
		}
	}

	// SABnzbd queue.
	if m.cfg.HasSABnzbd() && m.sab != nil {
		slots, err := m.sab.GetQueue()
		if err == nil {
			for _, slot := range slots {
				downloads = append(downloads, models.DownloadStatus{
					Source: "nzb",
					Title:  slot.Filename,
					Status: mapSABStatus(slot.Status),
					Size:   slot.Size,
					Detail: fmt.Sprintf("%s%% - %s left", slot.Percentage, slot.Timeleft),
					Hash:   slot.NzoID,
				})
			}
		}
	}

	// Background jobs.
	m.mu.Lock()
	for _, job := range m.jobs {
		downloads = append(downloads, models.DownloadStatus{
			Source:     job.Source,
			Title:      job.Title,
			Status:     job.Status,
			JobID:      job.ID,
			Error:      job.Error,
			Detail:     job.Detail,
			RetryCount: job.RetryCount,
			MaxRetries: job.MaxRetries,
		})
	}
	m.mu.Unlock()

	return downloads
}

func mapSABStatus(status string) string {
	switch strings.ToLower(status) {
	case "downloading":
		return "downloading"
	case "paused":
		return "paused"
	case "queued":
		return "queued"
	case "completed":
		return "completed"
	default:
		return status
	}
}

// DeleteTorrent removes a torrent from qBittorrent.
func (m *Manager) DeleteTorrent(hash string) error {
	return m.qb.DeleteTorrent(hash, true)
}

// DeleteJob removes a background download job.
func (m *Manager) DeleteJob(jobID string) error {
	m.mu.Lock()
	delete(m.jobs, jobID)
	m.mu.Unlock()
	return m.db.DeleteJob(jobID)
}

// ClearFinished removes completed/error/dead_letter jobs.
func (m *Manager) ClearFinished() (int, int, error) {
	m.mu.Lock()
	var jobsCleared int
	for id, job := range m.jobs {
		if job.Status == "completed" || job.Status == "error" || job.Status == "dead_letter" {
			delete(m.jobs, id)
			jobsCleared++
		}
	}
	m.mu.Unlock()

	dbCleared, err := m.db.ClearFinishedJobs()
	if err != nil {
		return jobsCleared, 0, err
	}

	// Clear completed qBittorrent torrents.
	torrentsCleared := 0
	if m.cfg.HasQBittorrent() {
		for _, cat := range []string{m.cfg.QBCategory, m.cfg.QBAudiobookCategory, m.cfg.QBMangaCategory} {
			torrents, err := m.qb.GetTorrents(cat)
			if err != nil {
				continue
			}
			for _, t := range torrents {
				status := MapTorrentStatus(t.State)
				if status == "completed" || t.State == "error" || t.State == "missingFiles" {
					if err := m.qb.DeleteTorrent(t.Hash, false); err == nil {
						torrentsCleared++
					}
				}
			}
		}
	}

	if dbCleared > jobsCleared {
		jobsCleared = dbCleared
	}
	return jobsCleared, torrentsCleared, nil
}

// HasSourceID checks if a source ID already exists in the library.
func (m *Manager) HasSourceID(sourceID string) bool {
	return m.db.HasSourceID(sourceID)
}

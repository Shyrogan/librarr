package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/JeremiahM37/librarr/internal/models"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "Invalid request body",
		})
		return
	}

	if req.Title == "" {
		req.Title = "Unknown"
	}

	username, _ := r.Context().Value(ctxUsername).(string)
	s.db.LogActivity(username, "download_start", req.Title, fmt.Sprintf("Download started from %s", req.Source))

	source := s.searchMgr.GetSource(req.Source)

	// Determine download type.
	downloadType := "direct"
	if source != nil {
		downloadType = source.DownloadType()
	} else if req.Source == "torrent" || req.Source == "audiobook" || req.Source == "prowlarr_manga" {
		downloadType = "torrent"
	}

	// Check if this is an NZB result that should go to SABnzbd.
	if isNZBResult(req) && s.cfg.HasSABnzbd() {
		s.handleNZBDownload(w, req)
		return
	}

	switch downloadType {
	case "torrent":
		s.handleTorrentDownload(w, req)
	default:
		s.handleDirectDownloadReq(w, req)
	}
}

func (s *Server) handleTorrentDownload(w http.ResponseWriter, req models.DownloadRequest) {
	if !s.cfg.HasQBittorrent() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "qBittorrent not configured",
		})
		return
	}

	url := resolveTorrentURL(req)
	if url == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "No download URL",
		})
		return
	}

	// Check for duplicates.
	sourceID := extractSourceID(req)
	if sourceID != "" && !req.Force && s.downloadMgr.HasSourceID(sourceID) {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"success": false,
			"error":   "Duplicate detected",
		})
		return
	}

	// Determine save path and category based on media type.
	savePath := s.cfg.QBSavePath
	category := s.cfg.QBCategory
	switch req.MediaType {
	case "audiobook":
		savePath = s.cfg.QBAudiobookSavePath
		category = s.cfg.QBAudiobookCategory
	case "manga":
		savePath = s.cfg.QBMangaSavePath
		category = s.cfg.QBMangaCategory
	}

	err := s.downloadMgr.StartTorrentDownload(url, req.Title, savePath, category)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": err == nil,
		"title":   req.Title,
		"error":   errString(err),
	})
}

func (s *Server) handleDirectDownloadReq(w http.ResponseWriter, req models.DownloadRequest) {
	// Anna's Archive download.
	if req.MD5 != "" {
		sourceID := req.MD5
		if !req.Force && s.downloadMgr.HasSourceID(sourceID) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"success": false,
				"error":   "Duplicate detected",
			})
			return
		}

		job, err := s.downloadMgr.StartAnnasDownload(req.MD5, req.Title)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"job_id":  job.ID,
			"title":   req.Title,
		})
		return
	}

	// Generic URL download.
	if req.URL != "" || req.DownloadURL != "" {
		dlURL := req.URL
		if dlURL == "" {
			dlURL = req.DownloadURL
		}
		sourceID := extractSourceID(req)
		if sourceID != "" && !req.Force && s.downloadMgr.HasSourceID(sourceID) {
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"success": false,
				"error":   "Duplicate detected",
			})
			return
		}

		job, err := s.downloadMgr.StartDirectDownload(dlURL, req.Title, req.Source, sourceID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"job_id":  job.ID,
			"title":   req.Title,
		})
		return
	}

	writeJSON(w, http.StatusBadRequest, map[string]interface{}{
		"success": false,
		"error":   "No download source specified (need md5, url, or download_url)",
	})
}

func (s *Server) handleDownloadTorrent(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Title == "" {
		req.Title = "Unknown"
	}
	req.MediaType = "ebook"
	s.handleTorrentDownload(w, req)
}

func (s *Server) handleDownloadAnnas(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Title == "" {
		req.Title = "Unknown"
	}
	if req.MD5 == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "No MD5 hash"})
		return
	}
	s.handleDirectDownloadReq(w, req)
}

func (s *Server) handleDownloadAudiobook(w http.ResponseWriter, r *http.Request) {
	var req models.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid request"})
		return
	}
	if req.Title == "" {
		req.Title = "Unknown"
	}
	req.MediaType = "audiobook"
	s.handleTorrentDownload(w, req)
}

func (s *Server) handleGetDownloads(w http.ResponseWriter, _ *http.Request) {
	downloads := s.downloadMgr.GetDownloads()
	if downloads == nil {
		downloads = []models.DownloadStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"downloads": downloads,
	})
}

func (s *Server) handleDeleteTorrent(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	err := s.downloadMgr.DeleteTorrent(hash)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": err == nil,
	})
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobID")
	err := s.downloadMgr.DeleteJob(jobID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{
			"success": false,
			"error":   "Job not found",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (s *Server) handleClearFinished(w http.ResponseWriter, _ *http.Request) {
	jobsCleared, torrentsCleared, err := s.downloadMgr.ClearFinished()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":          err == nil,
		"cleared_jobs":     jobsCleared,
		"cleared_torrents": torrentsCleared,
	})
}

func (s *Server) handleCheckDuplicate(w http.ResponseWriter, r *http.Request) {
	sourceID := r.URL.Query().Get("source_id")
	if sourceID == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"duplicate": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"duplicate": s.downloadMgr.HasSourceID(sourceID),
	})
}

func resolveTorrentURL(req models.DownloadRequest) string {
	if req.DownloadURL != "" {
		return req.DownloadURL
	}
	if strings.HasPrefix(req.GUID, "magnet:") {
		return req.GUID
	}
	if req.MagnetURL != "" {
		return req.MagnetURL
	}
	if req.InfoHash != "" {
		return fmt.Sprintf("magnet:?xt=urn:btih:%s", req.InfoHash)
	}
	return ""
}

func extractSourceID(req models.DownloadRequest) string {
	if req.MD5 != "" {
		return req.MD5
	}
	if req.InfoHash != "" {
		return req.InfoHash
	}
	if req.GUID != "" {
		return req.GUID
	}
	if req.URL != "" {
		return req.URL
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isNZBResult checks if a download request should be routed to SABnzbd.
func isNZBResult(req models.DownloadRequest) bool {
	if req.DownloadProtocol == "nzb" {
		return true
	}
	dl := strings.ToLower(req.DownloadURL)
	return strings.HasSuffix(dl, ".nzb") ||
		strings.Contains(dl, "/nzb/") ||
		strings.Contains(dl, "nzb?") ||
		strings.Contains(dl, "&t=get&")
}

func (s *Server) handleNZBDownload(w http.ResponseWriter, req models.DownloadRequest) {
	if !s.cfg.HasSABnzbd() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "SABnzbd not configured",
		})
		return
	}

	nzbURL := req.DownloadURL
	if nzbURL == "" {
		nzbURL = req.URL
	}
	if nzbURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "No NZB download URL",
		})
		return
	}

	nzoID, err := s.downloadMgr.StartNZBDownload(nzbURL, req.Title)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": err == nil,
		"title":   req.Title,
		"nzo_id":  nzoID,
		"error":   errString(err),
	})
}

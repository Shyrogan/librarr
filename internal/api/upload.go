package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// allowedUploadExts defines accepted file extensions for upload.
var allowedUploadExts = map[string]string{
	".epub": "ebook",
	".pdf":  "ebook",
	".mobi": "ebook",
	".azw3": "ebook",
	".m4b":  "audiobook",
	".mp3":  "audiobook",
	".zip":  "archive",
	".rar":  "archive",
}

// maxUploadSize is 500MB.
const maxUploadSize = 500 << 20

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	// Enforce size limit.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "File too large or invalid multipart form (max 500MB)",
		})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   "No file provided (use form field 'file')",
		})
		return
	}
	defer file.Close()

	ext := strings.ToLower(filepath.Ext(header.Filename))
	fileType, ok := allowedUploadExts[ext]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Unsupported file type: %s (accepted: .epub, .pdf, .mobi, .azw3, .m4b, .mp3, .zip, .rar)", ext),
		})
		return
	}

	// Determine media type from form field or extension.
	mediaType := r.FormValue("media_type")
	if mediaType == "" {
		if fileType == "archive" {
			mediaType = "ebook" // default for archives
		} else {
			mediaType = fileType
		}
	}

	// Save to temp file.
	tmpDir := os.TempDir()
	tmpFile, err := os.CreateTemp(tmpDir, "librarr-upload-*"+ext)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to create temp file",
		})
		return
	}
	tmpPath := tmpFile.Name()

	written, err := io.Copy(tmpFile, file)
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   "Failed to save uploaded file",
		})
		return
	}

	username, _ := r.Context().Value(ctxUsername).(string)
	title := r.FormValue("title")
	author := r.FormValue("author")
	if title == "" {
		// Use filename without extension as title.
		title = strings.TrimSuffix(header.Filename, ext)
	}

	// Organize the file.
	var organizedPath string
	var orgErr error

	switch mediaType {
	case "ebook":
		organizedPath, orgErr = s.organizer.OrganizeEbook(tmpPath, title, author)
		if orgErr == nil && s.targets != nil {
			s.targets.ImportEbook(organizedPath, title, author)
		}
	case "audiobook":
		organizedPath, orgErr = s.organizer.OrganizeAudiobook(tmpPath, title, author)
		if orgErr == nil && s.targets != nil {
			s.targets.ImportAudiobook()
		}
	default:
		organizedPath = tmpPath
	}

	status := "completed"
	errMsg := ""
	if orgErr != nil {
		slog.Warn("upload organize failed", "error", orgErr)
		status = "error"
		errMsg = orgErr.Error()
		organizedPath = tmpPath
	}

	// Record in database.
	s.db.SaveUpload(username, filepath.Base(organizedPath), header.Filename, mediaType, written, organizedPath, status, errMsg)

	// Log activity.
	s.db.LogActivity(username, "upload", header.Filename, fmt.Sprintf("Uploaded %s (%s, %d bytes)", header.Filename, mediaType, written))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success":  orgErr == nil,
		"filename": filepath.Base(header.Filename),
		"type":     mediaType,
		"size":     written,
		"error":    errMsg,
	})
}

func (s *Server) handleListUploads(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	uploads, err := s.db.GetUploads(limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	if uploads == nil {
		uploads = nil
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uploads": uploads,
		"limit":   limit,
		"offset":  offset,
	})
}

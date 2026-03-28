package api

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/JeremiahM37/librarr/internal/db"
)

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	// If ABS ebook library is configured, pull from there (has covers + series).
	// Otherwise fall back to local DB.
	if s.cfg.HasAudiobookshelf() && s.cfg.ABSEbookLibraryID != "" {
		s.handleLibraryEbooksFromABS(w, r)
		return
	}

	mediaType := r.URL.Query().Get("type")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	items, err := s.db.GetItems(mediaType, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	total, _ := s.db.CountItems(mediaType)

	var jsonItems []map[string]interface{}
	for _, item := range items {
		jsonItems = append(jsonItems, db.ItemToJSON(item))
	}
	if jsonItems == nil {
		jsonItems = []map[string]interface{}{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items":  jsonItems,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// handleLibraryEbooksFromABS pulls ebooks from ABS ebook library (with covers + series).
func (s *Server) handleLibraryEbooksFromABS(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "page", 1)
	query := r.URL.Query().Get("q")
	limit := 100 // Larger page size so series group together

	absURL := fmt.Sprintf("%s/api/libraries/%s/items", s.cfg.ABSURL, s.cfg.ABSEbookLibraryID)
	params := url.Values{
		"page":  {strconv.Itoa(page - 1)},
		"limit": {strconv.Itoa(limit)},
		"sort":  {"media.metadata.seriesName"},
	}
	if query != "" {
		params.Set("filter", "search="+url.QueryEscape(query))
	}

	req, err := http.NewRequest("GET", absURL+"?"+params.Encode(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.ABSToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": "Failed to reach ABS"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error": fmt.Sprintf("ABS returned HTTP %d", resp.StatusCode),
		})
		return
	}

	var absResp absLibraryResponse
	if err := json.NewDecoder(resp.Body).Decode(&absResp); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "Failed to parse ABS response"})
		return
	}

	publicURL := s.cfg.ABSPublicURL
	if publicURL == "" {
		publicURL = s.cfg.ABSURL
	}

	var items []map[string]interface{}
	for _, item := range absResp.Results {
		author := item.Media.Metadata.AuthorName
		if author == "" && len(item.Media.Metadata.Authors) > 0 {
			author = item.Media.Metadata.Authors[0].Name
		}
		series := item.Media.Metadata.SeriesName
		if series == "" && len(item.Media.Metadata.Series) > 0 {
			series = item.Media.Metadata.Series[0].Name
		}

		coverURL := fmt.Sprintf("%s/api/items/%s/cover", publicURL, item.ID)

		items = append(items, map[string]interface{}{
			"id":        item.ID,
			"title":     item.Media.Metadata.Title,
			"author":    author,
			"series":    series,
			"cover_url": coverURL,
			"abs_url":   fmt.Sprintf("%s/item/%s", publicURL, item.ID),
		})
	}
	if items == nil {
		items = []map[string]interface{}{}
	}

	totalPages := absResp.NumPages
	if totalPages == 0 && absResp.Total > 0 {
		totalPages = int(math.Ceil(float64(absResp.Total) / float64(limit)))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"items": items,
		"total": absResp.Total,
		"page":  page,
		"pages": totalPages,
	})
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	stats, err := s.db.GetStats()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	events, err := s.db.GetActivity(limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
			"error": err.Error(),
		})
		return
	}

	total, _ := s.db.CountActivity()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"events": events,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (s *Server) handleSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.searchMgr.SourceMeta())
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

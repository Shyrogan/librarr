package api

import (
	"fmt"
	"net/http"
)

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"results": []interface{}{},
			"error":   "No query provided",
		})
		return
	}

	username, _ := r.Context().Value(ctxUsername).(string)
	s.db.LogActivity(username, "search", query, fmt.Sprintf("Ebook search: %s", query))

	author := r.URL.Query().Get("author")
	results, elapsed := s.searchMgr.SearchWithAuthor(r.Context(), "main", query, author)
	if results == nil {
		results = nil // ensure null doesn't slip through
	}

	resp := map[string]interface{}{
		"results":        results,
		"search_time_ms": elapsed,
		"sources":        s.searchMgr.SourceMeta(),
	}

	// Fetch metadata for the query from Open Library.
	if s.metadataClient != nil {
		meta, err := s.metadataClient.FetchMetadataCtx(r.Context(), query, author)
		if err == nil && meta != nil {
			resp["metadata"] = meta
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSearchAudiobooks(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"results": []interface{}{},
			"error":   "No query provided",
		})
		return
	}

	author := r.URL.Query().Get("author")
	results, elapsed := s.searchMgr.SearchWithAuthor(r.Context(), "audiobook", query, author)

	resp := map[string]interface{}{
		"results":        results,
		"search_time_ms": elapsed,
		"sources":        s.searchMgr.SourceMeta(),
	}

	if s.metadataClient != nil {
		meta, err := s.metadataClient.FetchMetadataCtx(r.Context(), query, author)
		if err == nil && meta != nil {
			resp["metadata"] = meta
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSearchManga(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"results": []interface{}{},
			"error":   "No query provided",
		})
		return
	}

	author := r.URL.Query().Get("author")
	results, elapsed := s.searchMgr.SearchWithAuthor(r.Context(), "manga", query, author)

	resp := map[string]interface{}{
		"results":        results,
		"search_time_ms": elapsed,
		"sources":        s.searchMgr.SourceMeta(),
	}

	if s.metadataClient != nil {
		meta, err := s.metadataClient.FetchMetadataCtx(r.Context(), query, author)
		if err == nil && meta != nil {
			resp["metadata"] = meta
		}
	}

	writeJSON(w, http.StatusOK, resp)
}


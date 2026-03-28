package api

import (
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

	results, elapsed := s.searchMgr.Search(r.Context(), "main", query)
	if results == nil {
		results = nil // ensure null doesn't slip through
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results":        results,
		"search_time_ms": elapsed,
		"sources":        s.searchMgr.SourceMeta(),
	})
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

	results, elapsed := s.searchMgr.Search(r.Context(), "audiobook", query)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results":        results,
		"search_time_ms": elapsed,
		"sources":        s.searchMgr.SourceMeta(),
	})
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

	results, elapsed := s.searchMgr.Search(r.Context(), "manga", query)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results":        results,
		"search_time_ms": elapsed,
		"sources":        s.searchMgr.SourceMeta(),
	})
}

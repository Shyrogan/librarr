package torznab

import (
	"context"
	"crypto/subtle"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/JeremiahM37/librarr/internal/config"
	"github.com/JeremiahM37/librarr/internal/models"
	"github.com/JeremiahM37/librarr/internal/search"
)

// Handler serves Torznab API endpoints.
type Handler struct {
	cfg     *config.Config
	manager *search.Manager
}

// NewHandler creates a new Torznab handler.
func NewHandler(cfg *config.Config, manager *search.Manager) *Handler {
	return &Handler{cfg: cfg, manager: manager}
}

// ServeHTTP handles GET /torznab/api requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Validate API key if configured.
	if h.cfg.TorznabAPIKey != "" {
		apikey := r.URL.Query().Get("apikey")
		if subtle.ConstantTimeCompare([]byte(apikey), []byte(h.cfg.TorznabAPIKey)) != 1 {
			h.writeError(w, "100", "Invalid API Key", http.StatusUnauthorized)
			return
		}
	}

	t := r.URL.Query().Get("t")
	switch t {
	case "caps":
		h.handleCaps(w, r)
	case "search":
		h.handleSearch(w, r, "main")
	case "book":
		h.handleSearch(w, r, "main")
	case "audio":
		h.handleSearch(w, r, "audiobook")
	case "tvsearch", "movie":
		// Not supported, but return empty results instead of error.
		h.writeEmptyResults(w)
	default:
		h.writeError(w, "202", fmt.Sprintf("No such function (%s)", t), http.StatusBadRequest)
	}
}

func (h *Handler) handleCaps(w http.ResponseWriter, _ *http.Request) {
	caps := BuildCaps()
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(caps)
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request, tab string) {
	query := r.URL.Query().Get("q")
	if query == "" {
		// Try book-specific parameters.
		title := r.URL.Query().Get("title")
		author := r.URL.Query().Get("author")
		if title != "" {
			query = title
			if author != "" {
				query = title + " " + author
			}
		}
	}

	if query == "" {
		h.writeError(w, "100", "Missing parameter (q)", http.StatusBadRequest)
		return
	}

	slog.Info("torznab search", "query", query, "tab", tab, "remote", r.RemoteAddr)

	results, _ := h.manager.Search(context.Background(), tab, query)

	// Determine the base URL for download links.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, r.Host)

	// Convert results to Torznab items.
	var items []models.TorznabItem
	for _, result := range results {
		items = append(items, ResultToItem(result, baseURL))
	}

	rss := models.TorznabRSS{
		Version: "2.0",
		Xmlns:   "http://torznab.com/schemas/2015/feed",
		Channel: models.TorznabChannel{
			Title:       "Librarr",
			Description: "Book search results from Librarr",
			Items:       items,
		},
	}

	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(rss)
}

func (h *Handler) writeEmptyResults(w http.ResponseWriter) {
	rss := models.TorznabRSS{
		Version: "2.0",
		Xmlns:   "http://torznab.com/schemas/2015/feed",
		Channel: models.TorznabChannel{
			Title:       "Librarr",
			Description: "No results",
		},
	}

	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(rss)
}

func (h *Handler) writeError(w http.ResponseWriter, code, description string, httpStatus int) {
	errResp := models.TorznabError{
		Code:        code,
		Description: description,
	}
	w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
	w.WriteHeader(httpStatus)
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(errResp)
}

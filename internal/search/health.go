package search

import (
	"log/slog"
	"sync"
	"time"
)

// HealthTracker implements circuit breaker logic per source.
type HealthTracker struct {
	mu        sync.Mutex
	data      map[string]*sourceHealth
	threshold int
	openSec   int
}

type sourceHealth struct {
	SearchOK         int     `json:"search_ok"`
	SearchFail       int     `json:"search_fail"`
	DownloadOK       int     `json:"download_ok"`
	DownloadFail     int     `json:"download_fail"`
	SearchFailStreak int     `json:"search_fail_streak"`
	DownloadFailStrk int     `json:"download_fail_streak"`
	CircuitOpenUntil float64 `json:"circuit_open_until"`
	LastError        string  `json:"last_error"`
	LastErrorKind    string  `json:"last_error_kind"`
	LastErrorAt      float64 `json:"last_error_at"`
	LastSuccessAt    float64 `json:"last_success_at"`
	Score            float64 `json:"score"`
}

// NewHealthTracker creates a new health tracker.
func NewHealthTracker(threshold, openSeconds int) *HealthTracker {
	if threshold < 1 {
		threshold = 3
	}
	if openSeconds < 1 {
		openSeconds = 300
	}
	return &HealthTracker{
		data:      make(map[string]*sourceHealth),
		threshold: threshold,
		openSec:   openSeconds,
	}
}

func (h *HealthTracker) getOrCreate(name string) *sourceHealth {
	s, ok := h.data[name]
	if !ok {
		s = &sourceHealth{Score: 100.0}
		h.data[name] = s
	}
	return s
}

// CanSearch returns true if the source circuit is closed (healthy).
func (h *HealthTracker) CanSearch(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.getOrCreate(name)
	return time.Now().Unix() >= int64(s.CircuitOpenUntil)
}

// RecordSuccess records a successful operation.
func (h *HealthTracker) RecordSuccess(name, kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.getOrCreate(name)
	now := float64(time.Now().Unix())

	switch kind {
	case "search":
		s.SearchOK++
		s.SearchFailStreak = 0
	case "download":
		s.DownloadOK++
		s.DownloadFailStrk = 0
	}
	s.LastSuccessAt = now

	wasOpen := now < s.CircuitOpenUntil
	s.CircuitOpenUntil = 0
	h.recompute(s)

	if wasOpen {
		slog.Info("source recovered", "source", name, "kind", kind)
	}
}

// RecordFailure records a failed operation.
func (h *HealthTracker) RecordFailure(name, errMsg, kind string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.getOrCreate(name)
	now := float64(time.Now().Unix())

	switch kind {
	case "search":
		s.SearchFail++
		s.SearchFailStreak++
		if s.SearchFailStreak >= h.threshold {
			s.CircuitOpenUntil = now + float64(h.openSec)
			slog.Warn("source circuit opened", "source", name, "streak", s.SearchFailStreak)
		}
	case "download":
		s.DownloadFail++
		s.DownloadFailStrk++
	}

	s.LastError = errMsg
	if len(s.LastError) > 400 {
		s.LastError = s.LastError[:400]
	}
	s.LastErrorKind = kind
	s.LastErrorAt = now
	h.recompute(s)
}

func (h *HealthTracker) recompute(s *sourceHealth) {
	total := s.SearchOK + s.SearchFail + s.DownloadOK + s.DownloadFail
	if total == 0 {
		s.Score = 100.0
		return
	}
	ok := float64(s.SearchOK + s.DownloadOK)
	fail := float64(s.SearchFail + s.DownloadFail)
	streak := s.SearchFailStreak
	if s.DownloadFailStrk > streak {
		streak = s.DownloadFailStrk
	}
	s.Score = (ok/float64(total))*100 - (fail/float64(total))*10 - float64(5*streak)
	if s.Score < 0 {
		s.Score = 0
	}
}

// Snapshot returns a copy of all source health data.
func (h *HealthTracker) Snapshot() map[string]map[string]interface{} {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := float64(time.Now().Unix())
	out := make(map[string]map[string]interface{})
	for name, s := range h.data {
		circuitOpen := now < s.CircuitOpenUntil
		retryIn := 0
		if circuitOpen {
			retryIn = int(s.CircuitOpenUntil - now)
		}
		out[name] = map[string]interface{}{
			"search_ok":            s.SearchOK,
			"search_fail":          s.SearchFail,
			"download_ok":          s.DownloadOK,
			"download_fail":        s.DownloadFail,
			"search_fail_streak":   s.SearchFailStreak,
			"download_fail_streak": s.DownloadFailStrk,
			"circuit_open":         circuitOpen,
			"circuit_retry_in_sec": retryIn,
			"last_error":           s.LastError,
			"last_success_at":      s.LastSuccessAt,
			"score":                s.Score,
		}
	}
	return out
}

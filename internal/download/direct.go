package download

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JeremiahM37/librarr/internal/config"
	"github.com/JeremiahM37/librarr/internal/organize"
)

// DirectDownloader handles direct HTTP file downloads (Anna's Archive, Gutenberg, etc.).
type DirectDownloader struct {
	cfg    *config.Config
	client *http.Client
}

// NewDirectDownloader creates a new direct file downloader.
func NewDirectDownloader(cfg *config.Config, client *http.Client) *DirectDownloader {
	return &DirectDownloader{cfg: cfg, client: client}
}

var getLinkRe = regexp.MustCompile(`href="(get\.php\?md5=[^"]+)"`)

// DownloadFromAnnas downloads a file from Anna's Archive via libgen.
// Returns the local file path and size, or an error.
func (d *DirectDownloader) DownloadFromAnnas(md5, title string, progressFn func(string)) (string, int64, error) {
	if progressFn != nil {
		progressFn("Fetching download link from Anna's Archive...")
	}

	// Step 1: Get the download key from libgen ads page.
	adsURL := fmt.Sprintf("https://libgen.li/ads.php?md5=%s", md5)
	req, err := http.NewRequest("GET", adsURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", d.cfg.UserAgent)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("fetch libgen ads page: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("libgen ads page HTTP %d", resp.StatusCode)
	}

	match := getLinkRe.FindSubmatch(body)
	if len(match) < 2 {
		// Try alternative MD5 hashes by re-searching Anna's Archive.
		if progressFn != nil {
			progressFn("No direct link found, trying alternative mirrors...")
		}
		altURL, altErr := d.tryAltMD5(title, md5, progressFn)
		if altErr != nil {
			return "", 0, fmt.Errorf("no get.php link found on libgen for md5=%s (alt search also failed: %v)", md5, altErr)
		}
		return d.downloadFile(altURL, title, progressFn)
	}

	downloadURL := fmt.Sprintf("https://libgen.li/%s", string(match[1]))
	slog.Info("found libgen download link", "title", title, "url", downloadURL[:60])

	if progressFn != nil {
		progressFn("Downloading...")
	}

	// Step 2: Download the file.
	return d.downloadFile(downloadURL, title, progressFn)
}

// DownloadFromURL downloads a file from any direct URL.
func (d *DirectDownloader) DownloadFromURL(fileURL, title string, progressFn func(string)) (string, int64, error) {
	return d.downloadFile(fileURL, title, progressFn)
}

func (d *DirectDownloader) downloadFile(fileURL, title string, progressFn func(string)) (string, int64, error) {
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", d.cfg.UserAgent)

	resp, err := d.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", 0, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	// If we got an HTML response, try to find the actual download link.
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		body, _ := io.ReadAll(resp.Body)
		bodyStr := string(body)

		if strings.Contains(bodyStr, "File not found") || strings.Contains(bodyStr, "Error</h1>") {
			return "", 0, fmt.Errorf("file not found on server")
		}

		// Look for a GET link on the page.
		getLink := regexp.MustCompile(`href="(https?://[^"]+)"[^>]*>GET</a>`).FindStringSubmatch(bodyStr)
		if len(getLink) < 2 {
			fileLink := regexp.MustCompile(`href="(https?://[^"]*\.(epub|pdf|mobi)[^"]*)"`).FindStringSubmatch(bodyStr)
			if len(fileLink) < 2 {
				return "", 0, fmt.Errorf("no download link found in HTML response")
			}
			return d.downloadFile(fileLink[1], title, progressFn)
		}
		return d.downloadFile(getLink[1], title, progressFn)
	}

	// Save to incoming directory.
	safeTitle := sanitizeFilename(title, 80)
	if err := os.MkdirAll(d.cfg.IncomingDir, 0755); err != nil {
		return "", 0, fmt.Errorf("create incoming dir: %w", err)
	}

	// Determine file extension from Content-Type.
	ext := ".epub"
	if strings.Contains(contentType, "pdf") {
		ext = ".pdf"
	}

	filePath := filepath.Join(d.cfg.IncomingDir, safeTitle+ext)
	f, err := os.Create(filePath)
	if err != nil {
		return "", 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		os.Remove(filePath)
		return "", 0, fmt.Errorf("write file: %w", err)
	}

	if written < 1000 {
		os.Remove(filePath)
		return "", 0, fmt.Errorf("downloaded file too small (%d bytes)", written)
	}

	slog.Info("file downloaded", "title", title, "size", written, "path", filePath)

	// EPUB verification: validate ZIP and title match.
	if strings.HasSuffix(strings.ToLower(filePath), ".epub") {
		if err := d.verifyEPUB(filePath, title); err != nil {
			os.Remove(filePath)
			return "", 0, fmt.Errorf("EPUB verification failed: %w", err)
		}
	}

	return filePath, written, nil
}

// verifyEPUB validates that an EPUB file is a valid ZIP and its title matches.
func (d *DirectDownloader) verifyEPUB(filePath, expectedTitle string) error {
	// Validate ZIP structure.
	if _, err := os.Stat(filePath); err != nil {
		return err
	}

	// Check title overlap (60% threshold).
	ok, actualTitle, err := organize.VerifyEPUBTitle(filePath, expectedTitle, 0.6)
	if err != nil {
		slog.Warn("EPUB metadata extraction failed (allowing download)", "error", err)
		return nil // Can't verify, let it pass.
	}
	if !ok {
		return fmt.Errorf("wrong book: expected %q, got %q", expectedTitle, actualTitle)
	}
	return nil
}

// tryAltMD5 searches Anna's Archive for alternative MD5 hashes and tries them.
func (d *DirectDownloader) tryAltMD5(title, originalMD5 string, progressFn func(string)) (string, error) {
	ctx := context.Background()
	annas := &annasSearchHelper{cfg: d.cfg, client: d.client}
	results, err := annas.searchForTitle(ctx, title)
	if err != nil {
		return "", err
	}

	tried := 0
	for _, r := range results {
		if r.MD5 == "" || r.MD5 == originalMD5 {
			continue
		}
		if tried >= 3 {
			break
		}
		tried++

		if progressFn != nil {
			progressFn(fmt.Sprintf("Trying alt mirror %d/3...", tried))
		}

		adsURL := fmt.Sprintf("https://libgen.li/ads.php?md5=%s", r.MD5)
		req, err := http.NewRequest("GET", adsURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", d.cfg.UserAgent)

		resp, err := d.client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			continue
		}

		match := getLinkRe.FindSubmatch(body)
		if len(match) >= 2 {
			downloadURL := fmt.Sprintf("https://libgen.li/%s", string(match[1]))
			slog.Info("found alt libgen download link", "title", title, "alt_md5", r.MD5)
			return downloadURL, nil
		}
	}

	return "", fmt.Errorf("no alternative MD5 hashes had working download links")
}

// annasSearchHelper is a minimal helper to search Anna's Archive for alt MD5s.
type annasSearchHelper struct {
	cfg    *config.Config
	client *http.Client
}

func (a *annasSearchHelper) searchForTitle(ctx context.Context, title string) ([]struct{ MD5 string }, error) {
	baseURL := fmt.Sprintf("https://%s/search", a.cfg.AnnasArchiveDomain)
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("q", title)
	q.Set("ext", "epub")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("User-Agent", a.cfg.UserAgent)

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	md5Re := regexp.MustCompile(`/md5/([a-f0-9]+)`)
	matches := md5Re.FindAllStringSubmatch(string(body), -1)

	seen := make(map[string]bool)
	var results []struct{ MD5 string }
	for _, m := range matches {
		if len(m) >= 2 && !seen[m[1]] {
			seen[m[1]] = true
			results = append(results, struct{ MD5 string }{m[1]})
		}
	}
	return results, nil
}

var unsafeCharsRe = regexp.MustCompile(`[^\w\s-]`)

func sanitizeFilename(name string, maxLen int) string {
	name = unsafeCharsRe.ReplaceAllString(name, "")
	name = strings.TrimSpace(name)
	if len(name) > maxLen {
		name = name[:maxLen]
	}
	if name == "" {
		name = "book"
	}
	return name
}

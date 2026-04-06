package organize

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
)

type comicInfo struct {
	Series       string `xml:"Series"`
	Title        string `xml:"Title"`
	Number       string `xml:"Number"`
	Volume       string `xml:"Volume"`
	Count        string `xml:"Count"`
	Writer       string `xml:"Writer"`
	Penciller    string `xml:"Penciller"`
	Inker        string `xml:"Inker"`
	Colorist     string `xml:"Colorist"`
	Letterer     string `xml:"Letterer"`
	CoverArtist  string `xml:"CoverArtist"`
	Editor       string `xml:"Editor"`
	Publisher    string `xml:"Publisher"`
	Genre        string `xml:"Genre"`
	Tags         string `xml:"Tags"`
	Summary      string `xml:"Summary"`
	Year         string `xml:"Year"`
	Month        string `xml:"Month"`
	Day          string `xml:"Day"`
	Manga        string `xml:"Manga"`
	Characters   string `xml:"Characters"`
	Teams        string `xml:"Teams"`
	Locations    string `xml:"Locations"`
	PageCount    string `xml:"PageCount"`
	ScanInfo     string `xml:"ScanInformation"`
	StoryArc     string `xml:"StoryArc"`
	SeriesGroup  string `xml:"SeriesGroup"`
	AgeRating    string `xml:"AgeRating"`
	Format       string `xml:"Format"`
	LanguageISO  string `xml:"LanguageISO"`
	Imprint      string `xml:"Imprint"`
	Web          string `xml:"Web"`
	BlackAndWhite string `xml:"BlackAndWhite"`
	GTIN         string `xml:"GTIN"`
	AlternateSeries string `xml:"AlternateSeries"`
	AlternateNumber string `xml:"AlternateNumber"`
	AlternateCount  string `xml:"AlternateCount"`
	Notes        string `xml:"Notes"`
}

func parseComicInfo(filePath string) *comicInfo {
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext != ".cbz" && ext != ".zip" {
		return nil
	}

	r, err := zip.OpenReader(filePath)
	if err != nil {
		slog.Debug("failed to open comic as zip", "file", filePath, "error", err)
		return nil
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "ComicInfo.xml" {
			rc, err := f.Open()
			if err != nil {
				slog.Debug("failed to open ComicInfo.xml", "error", err)
				return nil
			}
			defer rc.Close()

			data, err := io.ReadAll(rc)
			if err != nil {
				slog.Debug("failed to read ComicInfo.xml", "error", err)
				return nil
			}

			var ci comicInfo
			if err := xml.Unmarshal(data, &ci); err != nil {
				slog.Debug("failed to parse ComicInfo.xml", "error", err)
				return nil
			}
			return &ci
		}
	}

	return nil
}

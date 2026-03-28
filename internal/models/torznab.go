package models

import "encoding/xml"

// TorznabCaps represents the capabilities response.
type TorznabCaps struct {
	XMLName    xml.Name          `xml:"caps"`
	Server     TorznabServer     `xml:"server"`
	Limits     TorznabLimits     `xml:"limits"`
	Searching  TorznabSearching  `xml:"searching"`
	Categories TorznabCategories `xml:"categories"`
}

type TorznabServer struct {
	Title string `xml:"title,attr"`
}

type TorznabLimits struct {
	Max     int `xml:"max,attr"`
	Default int `xml:"default,attr"`
}

type TorznabSearching struct {
	Search      TorznabSearchCap `xml:"search"`
	BookSearch  TorznabSearchCap `xml:"book-search"`
	AudioSearch TorznabSearchCap `xml:"audio-search"`
}

type TorznabSearchCap struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type TorznabCategories struct {
	Categories []TorznabCategory `xml:"category"`
}

type TorznabCategory struct {
	ID   string              `xml:"id,attr"`
	Name string              `xml:"name,attr"`
	Subs []TorznabSubCategory `xml:"subcat,omitempty"`
}

type TorznabSubCategory struct {
	ID   string `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

// TorznabRSS is the RSS feed envelope for search results.
type TorznabRSS struct {
	XMLName xml.Name       `xml:"rss"`
	Version string         `xml:"version,attr"`
	Xmlns   string         `xml:"xmlns:torznab,attr"`
	Channel TorznabChannel `xml:"channel"`
}

type TorznabChannel struct {
	Title       string        `xml:"title"`
	Description string        `xml:"description"`
	Items       []TorznabItem `xml:"item"`
}

type TorznabItem struct {
	Title     string             `xml:"title"`
	GUID      string             `xml:"guid"`
	Size      int64              `xml:"size"`
	Link      string             `xml:"link"`
	Category  string             `xml:"category,omitempty"`
	PubDate   string             `xml:"pubDate,omitempty"`
	Enclosure *TorznabEnclosure  `xml:"enclosure,omitempty"`
	Attrs     []TorznabAttr      `xml:"torznab:attr,omitempty"`
}

type TorznabEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type TorznabAttr struct {
	XMLName xml.Name `xml:"torznab:attr"`
	Name    string   `xml:"name,attr"`
	Value   string   `xml:"value,attr"`
}

// TorznabError is an error response.
type TorznabError struct {
	XMLName     xml.Name `xml:"error"`
	Code        string   `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

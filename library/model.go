package library

import "time"

// Library is an admin-configured root folder with an access whitelist.
type Library struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	RootPath  string     `json:"rootPath"`
	CreatedAt time.Time  `json:"createdAt"`
	LastScan  *time.Time `json:"lastScan,omitempty"`
	Whitelist []string   `json:"whitelist"`
	// CollectionCount is populated for list views.
	CollectionCount int    `json:"collectionCount"`
	CoverPhoto      string `json:"coverPhoto,omitempty"`
	BackgroundPhoto string `json:"backgroundPhoto,omitempty"`
	// User-editable metadata.
	SortTitle string `json:"sortTitle,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// Node is a single folder in the recursive library tree. Every folder under a
// library root is a node. A node can contain sub-nodes (collection view) and/or
// photos directly (album view), and may be both at once. ParentID is empty for
// top-level nodes directly under the library root.
type Node struct {
	ID              string `json:"id"`
	LibraryID       string `json:"libraryId"`
	ParentID        string `json:"parentId,omitempty"`
	Name            string `json:"name"`
	FSPath          string `json:"-"`
	Depth           int    `json:"depth"`
	PhotoCount      int    `json:"photoCount"`
	ChildCount      int    `json:"childCount"`
	HasChildren     bool   `json:"hasChildren"`
	// Type is derived from contents: "album" for a leaf folder holding photos,
	// otherwise "collection". Used by the UI to pick a poster vs landscape card.
	Type string `json:"type"`
	CoverPhoto      string `json:"coverPhoto,omitempty"`
	BackgroundPhoto string `json:"backgroundPhoto,omitempty"`
	// User-editable metadata, stored separately from the folder name so it
	// survives rescans.
	SortTitle     string `json:"sortTitle,omitempty"`
	Summary       string `json:"summary,omitempty"`
	ContentRating string `json:"contentRating,omitempty"`
	Year          string `json:"year,omitempty"`
	Studio        string `json:"studio,omitempty"`
	// FolderPath is the folder path relative to the owning library root,
	// exposed read-only for display.
	FolderPath string `json:"folderPath,omitempty"`
	// Favorite is populated for the current user in some listings.
	Favorite bool `json:"favorite"`
}

// HomeNode is a node enriched with navigation context for home-page cards
// (which span multiple libraries).
type HomeNode struct {
	Node
	LibraryName string `json:"libraryName"`
}

// Photo is a single image file within an album.
type Photo struct {
	Name string `json:"name"`
	// Path is a URL token derived from the photo's absolute filesystem path
	// (leading slash and any drive letter stripped), used for thumb/photo URLs.
	// Access is authorized per request against the owning library root.
	Path string `json:"path"`
}

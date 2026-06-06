package unit

import (
	"path/filepath"
	"testing"

	"plex-photos/library"
)

func TestIsImage(t *testing.T) {
	cases := map[string]bool{
		"a.jpg":       true,
		"a.JPG":       true,
		"a.jpeg":      true,
		"a.png":       true,
		"a.gif":       true,
		"a.webp":      true,
		"a.txt":       false,
		"a.mp4":       false,
		"noext":       false,
		"dir/b.JpEg":  true,
		"archive.zip": false,
	}
	for name, want := range cases {
		if got := library.IsImage(name); got != want {
			t.Errorf("IsImage(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestResolveUnderRootValid(t *testing.T) {
	root := filepath.FromSlash("/photos")
	got, err := library.ResolveUnderRoot(root, "famille/montreal/IMG_001.jpg")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(root, "famille", "montreal", "IMG_001.jpg")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveUnderRootBlocksTraversal(t *testing.T) {
	root := filepath.FromSlash("/photos")
	bad := []string{
		"../secret.txt",
		"../../etc/passwd",
		"famille/../../escape.jpg",
		"/../../etc/shadow",
	}
	for _, p := range bad {
		if _, err := library.ResolveUnderRoot(root, p); err == nil {
			t.Errorf("ResolveUnderRoot(%q) should have been rejected", p)
		}
	}
}

func TestRelToRoot(t *testing.T) {
	root := filepath.FromSlash("/photos")
	full := filepath.Join(root, "famille", "montreal", "IMG.jpg")
	rel, err := library.RelToRoot(root, full)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != "famille/montreal/IMG.jpg" {
		t.Errorf("got %q, want famille/montreal/IMG.jpg", rel)
	}
}

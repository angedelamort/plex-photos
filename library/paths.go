package library

import (
	"errors"
	"path/filepath"
	"strings"
)

var imageExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
}

// IsImage reports whether name has a supported image extension.
func IsImage(name string) bool {
	return imageExts[strings.ToLower(filepath.Ext(name))]
}

// ErrUnsafePath is returned when a path escapes its allowed root.
var ErrUnsafePath = errors.New("unsafe path")

// ResolveUnderRoot joins root with a (untrusted) relative path and guarantees
// the result stays confined under root, defending against directory traversal.
func ResolveUnderRoot(root, rel string) (string, error) {
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	cleaned := filepath.Clean(filepath.Join(root, rel))
	rootClean := filepath.Clean(root)
	if cleaned != rootClean && !strings.HasPrefix(cleaned, rootClean+string(filepath.Separator)) {
		return "", ErrUnsafePath
	}
	return cleaned, nil
}

// underRoot reports whether target is root itself or nested under it.
func underRoot(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	return target == root || strings.HasPrefix(target, root+string(filepath.Separator))
}

// RelToRoot returns the slash-separated path of target relative to root.
func RelToRoot(root, target string) (string, error) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || rel == ".." {
		return "", ErrUnsafePath
	}
	return rel, nil
}

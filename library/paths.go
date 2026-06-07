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

// systemDirs are directory names that some platforms/NAS devices create
// alongside real media (e.g. Synology's "@eaDir" thumbnail caches). They hold
// no user content and must never be scanned as sub-collections, otherwise a
// flat album folder is misreported as having children.
var systemDirs = map[string]bool{
	"@eaDir":                    true, // Synology DSM thumbnail/metadata cache
	"#recycle":                  true, // Synology recycle bin
	"$RECYCLE.BIN":              true, // Windows recycle bin
	"System Volume Information": true, // Windows
	"lost+found":                true, // Linux fsck
}

// SkipDirName reports whether a directory should be ignored when walking a
// library tree. Hidden (dot-prefixed) directories and known system/junk
// directories are skipped so they never become phantom "collections".
func SkipDirName(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	return systemDirs[name]
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

// AbsToURLPath converts an absolute filesystem path into a slash-separated,
// leading-slash-stripped token suitable for embedding in a thumb/photo URL. It
// is a faithful, round-trippable encoding of the absolute path (the leading
// separator is dropped for clean URLs; on Windows the drive letter is kept so
// the path can be reconstructed). It is the inverse of URLPathToAbs.
func AbsToURLPath(abs string) string {
	p := filepath.ToSlash(filepath.Clean(abs))
	return strings.TrimPrefix(p, "/")
}

// URLPathToAbs reconstructs an absolute filesystem path from a URL token
// produced by AbsToURLPath. The result is cleaned but NOT yet authorized; the
// caller must confine it to an accessible library root before use.
func URLPathToAbs(token string) string {
	token = strings.TrimPrefix(filepath.ToSlash(token), "/")
	native := filepath.FromSlash(token)
	// On POSIX, prepend the root separator. On Windows the token already starts
	// with a drive letter (e.g. "C:/..."), so Clean alone yields an absolute path.
	if filepath.IsAbs(native) {
		return filepath.Clean(native)
	}
	if vol := filepath.VolumeName(native); vol != "" {
		return filepath.Clean(native)
	}
	return filepath.Clean(string(filepath.Separator) + native)
}

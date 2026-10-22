package sprobot

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Saved-file kinds, as they appear in web paths.
const (
	FileKindMod      = "mod"
	FileKindShortcut = "shortcut"
	FileKindSticky   = "sticky"
)

// Saved-file kinds mapped to their bucket prefixes. Files are both saved and
// served through FileKey, so the bots and the web viewer can't disagree on
// where a kind lives.
var fileKindPrefixes = map[string]string{
	FileKindMod:      "mod_files",
	FileKindShortcut: "shortcut_files",
	FileKindSticky:   "sticky_files",
}

// FilePrefix returns the bucket prefix for a file kind.
func FilePrefix(kind string) (string, bool) {
	p, ok := fileKindPrefixes[kind]
	return p, ok
}

// ValidID reports whether s has the form of a Discord ID: 1 to 20 digits.
func ValidID(s string) bool {
	return idRE.MatchString(s)
}

var (
	idRE = regexp.MustCompile(`^[0-9]{1,20}$`)
	// 30 random characters plus the original extension. Files saved by the
	// old Python bot have a doubled dot before the extension.
	fileNameRE = regexp.MustCompile(`^[A-Za-z0-9]{30}(\.{1,2}[A-Za-z0-9]{1,10})?$`)
	fileExtRE  = regexp.MustCompile(`^\.[A-Za-z0-9]{1,10}$`)
)

// SafeFileExt returns ext if it can be part of a saved file name, else "".
func SafeFileExt(ext string) string {
	if fileExtRE.MatchString(ext) {
		return ext
	}
	return ""
}

// FileKey returns the bucket key for a saved file, validating every part.
func FileKey(kind, guildID, name string) (string, bool) {
	prefix, ok := fileKindPrefixes[kind]
	if !ok || !idRE.MatchString(guildID) || !fileNameRE.MatchString(name) {
		return "", false
	}
	return prefix + "/" + guildID + "/" + name, true
}

// ParseFileKey is the inverse of FileKey.
func ParseFileKey(key string) (kind, guildID, name string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 {
		return "", "", "", false
	}
	for k, prefix := range fileKindPrefixes {
		if parts[0] == prefix {
			if _, valid := FileKey(k, parts[1], parts[2]); valid {
				return k, parts[1], parts[2], true
			}
		}
	}
	return "", "", "", false
}

// ProfileImageS3Path is the key an uploaded profile image is stored under. ext
// includes the dot, e.g. ".png".
func ProfileImageS3Path(guildID, templateName, userID, ext string) string {
	return "images/" + guildID + "/" + templateName + "/" + userID + ext
}

// ParseProfileImageKey is the inverse of ProfileImageS3Path.
func ParseProfileImageKey(key string) (guildID, templateName, userID string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "images" || !idRE.MatchString(parts[1]) || parts[2] == "" {
		return "", "", "", false
	}
	userID = strings.TrimSuffix(parts[3], path.Ext(parts[3]))
	if !idRE.MatchString(userID) {
		return "", "", "", false
	}
	return parts[1], parts[2], userID, true
}

// FileWebPath is the permanent web route for a saved file.
func FileWebPath(kind, guildID, name string) string {
	return fmt.Sprintf("files/%s/%s/%s", kind, guildID, name)
}

// FormatBytes renders a byte count in binary units, e.g. "312.4 MiB".
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Content types for files that may be shown inline, keyed by lowercase
// extension. Kept here rather than using mime.TypeByExtension, which knows no
// video types in a distroless image. Anything not listed is only ever offered
// as a download.
var inlineContentTypes = map[string]string{
	".bmp":  "image/bmp",
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".mov":  "video/quicktime",
	".mp4":  "video/mp4",
	".webm": "video/webm",
}

// InlineContentType returns the content type for a file name that may be
// displayed inline, or "" if it may only be downloaded.
func InlineContentType(name string) string {
	return inlineContentTypes[strings.ToLower(path.Ext(name))]
}

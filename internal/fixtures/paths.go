package fixtures

import (
	"path/filepath"
	"regexp"
	"strings"
)

// driveStart matches where a Windows path begins: a drive letter, a colon and
// a backslash.
var driveStart = regexp.MustCompile(`\b[A-Za-z]:\\`)

// OSPath gives a path written the Windows way, or a text with such paths in
// it, the form it has on this system. On Windows that is the text itself.
// Anywhere else D:\Music\A becomes /d/Music/A: a path as long as the one it
// stands for, so that a screen lays out the same on every system and one set
// of golden files serves them all.
//
// Tests write their made-up paths the Windows way and pass them through
// OSPath, since a backslash in a text can only be a separator, while a slash
// may be one or may be the one in "files/s".
func OSPath(s string) string {
	if filepath.Separator == '\\' {
		return s
	}
	s = driveStart.ReplaceAllStringFunc(s, func(drive string) string {
		return "/" + strings.ToLower(drive[:1]) + "/"
	})
	return strings.ReplaceAll(s, `\`, "/")
}

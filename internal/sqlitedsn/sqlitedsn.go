// Package sqlitedsn builds file URIs accepted by SQLite on every supported OS.
package sqlitedsn

import (
	"net/url"
	"strings"
)

// File returns a SQLite file URI without interpreting any path bytes as query data.
func File(path string) string {
	normalized := path
	if isWindowsDrivePath(path) || strings.HasPrefix(path, `\\`) {
		normalized = strings.ReplaceAll(path, `\`, "/")
	}
	if isWindowsDrivePath(normalized) {
		normalized = "/" + normalized
	}
	return (&url.URL{Scheme: "file", Path: normalized}).String()
}

func isWindowsDrivePath(path string) bool {
	return len(path) >= 3 && path[1] == ':' &&
		((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) &&
		(path[2] == '/' || path[2] == '\\')
}

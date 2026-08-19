// Package consoleui serves Rin's dependency-free local management console.
package consoleui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var assets embed.FS

func NewHandler() http.Handler {
	static, err := fs.Sub(assets, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		if request.URL.Path == "/console" {
			http.Redirect(response, request, "/console/", http.StatusPermanentRedirect)
			return
		}
		path := strings.TrimPrefix(request.URL.Path, "/console/")
		if path == "" {
			path = "/"
		} else {
			path = "/" + path
		}
		request.URL.Path = path
		files.ServeHTTP(response, request)
	})
}

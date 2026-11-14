package main

import (
	"embed"
	"io/fs"
)

// The web UI is embedded, not installed. One binary, no asset directory to find
// or to have a stale copy of, and `go install` keeps working exactly as before.
//
//go:embed webui
var webUIRoot embed.FS

// webAssets is the same tree without the webui/ prefix, so index.html, app.css
// and assets/ resolve from the root.
var webAssets = mustSub(webUIRoot, "webui")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("embed " + dir + ": " + err.Error())
	}
	return sub
}

package api

import (
	"embed"
	"io/fs"
)

//go:embed web/index.html
var webFiles embed.FS

// webFS serves the embedded UI rooted at web/ so index.html is at "/".
var webFS = mustSub(webFiles, "web")

func mustSub(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

//go:build prod

package main

import (
	"embed"
	"io/fs"
	"log"
)

//go:embed webui/dist
var webUI embed.FS

func webFS() fs.FS {
	f, err := fs.Sub(webUI, "webui/dist")
	if err != nil {
		log.Fatalf("could not load embedded web UI: %v", err)
	}
	return f
}

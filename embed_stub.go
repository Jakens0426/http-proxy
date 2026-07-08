//go:build !prod

package main

import (
	"io/fs"
	"os"
)

func webFS() fs.FS {
	if _, err := os.Stat("webui/dist"); err == nil {
		return os.DirFS("webui/dist")
	}
	return emptyFS{}
}

type emptyFS struct{}

func (emptyFS) Open(name string) (fs.File, error) { return nil, fs.ErrNotExist }

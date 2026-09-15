package minidashboard

import (
	"embed"
	"io/fs"
)

//go:embed web
var embeddedWeb embed.FS

func WebAssets() fs.FS {
	assets, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		panic(err)
	}
	return assets
}

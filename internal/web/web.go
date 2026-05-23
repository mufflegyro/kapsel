package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var embedded embed.FS

func Static() fs.FS {
	static, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err)
	}

	return static
}

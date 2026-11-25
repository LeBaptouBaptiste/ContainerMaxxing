// Package web embarque le frontend dans le binaire (go:embed) :
// l'image finale n'a besoin d'aucun fichier à côté du binaire.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var files embed.FS

func Static() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// Package examples holds Fabrica's examples. The only Go in it builds the
// starter factory into the fabrica binary, so `fabrica new` can write it out
// on machines that don't have a copy of this repo.
package examples

import (
	"embed"
	"io/fs"
)

//go:embed starter-factory
var starter embed.FS

// StarterFactory is examples/starter-factory, rooted at the factory itself.
func StarterFactory() fs.FS {
	sub, err := fs.Sub(starter, "starter-factory")
	if err != nil {
		panic(err) // can't happen: the folder is embedded above
	}
	return sub
}

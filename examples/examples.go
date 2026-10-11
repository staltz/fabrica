// Package examples holds Fabrica's examples. The only Go in it builds the
// starter blueprint into the fabrica binary, so `fabrica new` can write it out
// on machines that don't have a copy of this repo.
package examples

import (
	"embed"
	"io/fs"
)

//go:embed starter-blueprint
var starter embed.FS

// StarterBlueprint is examples/starter-blueprint, rooted at the blueprint itself.
func StarterBlueprint() fs.FS {
	sub, err := fs.Sub(starter, "starter-blueprint")
	if err != nil {
		panic(err) // can't happen: the folder is embedded above
	}
	return sub
}

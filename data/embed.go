// Package data embeds the world files, for builds that cannot read the
// disk (the WebAssembly demo). The server still loads data/world from disk
// with -world, so the world can change without a rebuild.
package data

import "embed"

// World holds world/*.json.
//
//go:embed world/*.json
var World embed.FS

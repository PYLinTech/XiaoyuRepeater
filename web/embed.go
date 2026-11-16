// Package webassets embeds the compiled control panel.
package webassets

import "embed"

//go:embed all:dist
var FS embed.FS

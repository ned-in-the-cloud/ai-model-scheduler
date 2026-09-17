// Package web embeds the UI templates and static assets into the binary.
package web

import "embed"

//go:embed templates static
var FS embed.FS

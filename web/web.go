// Package web embeds the static views and shared app.js so the server ships
// as one binary with no separate frontend build step.
package web

import "embed"

//go:embed create results vote app.js
var FS embed.FS

package web

import "embed"

// Files contains the complete WebConsole frontend.
//
//go:embed index.html app.js style.css
var Files embed.FS

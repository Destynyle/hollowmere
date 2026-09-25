// Package web holds the browser client, embedded in the server binary.
package web

import "embed"

// Assets is the web client (HTML, CSS, JS).
//
//go:embed index.html style.css app.js
var Assets embed.FS

#!/bin/sh
# Builds the static demo into site/: the regular web client, plus the game
# server compiled to WebAssembly. Serve site/ with any static host.
set -eu

GO=${GO:-go}
OUT=${1:-site}
REPO_URL=${REPO_URL:-https://github.com/Destynyle/hollowmere}

rm -rf "$OUT"
mkdir -p "$OUT"
GOOS=js GOARCH=wasm "$GO" build -trimpath -ldflags="-s -w" -o "$OUT/hollowmere.wasm" ./cmd/demo
cp "$("$GO" env GOROOT)/lib/wasm/wasm_exec.js" "$OUT/"
cp internal/web/style.css internal/web/app.js web-demo/demo.js "$OUT/"

# index.html: load the engine before the client, and say what this is.
BANNER="<div id=\"demo-banner\" class=\"demo-banner\">Demo: the whole server runs in your browser (Go → WebAssembly), so this world is yours alone. For the multiplayer server, see <a href=\"$REPO_URL\">the repository</a>.</div>"
sed \
	-e "s#<script src=\"app.js\"></script>#<script src=\"wasm_exec.js\"></script>\n<script src=\"demo.js\"></script>\n<script src=\"app.js\"></script>#" \
	-e "s#<body>#<body>\n$BANNER#" \
	internal/web/index.html > "$OUT/index.html"
cat >> "$OUT/style.css" <<'CSS'

/* demo build only */
.demo-banner { padding: 6px 16px; background: #1d140c; color: #f3e6c8; font: 15px/1.4 ui-monospace, monospace; text-align: center; }
.demo-banner a { color: #ffd46b; }
a[href="/top"] { display: none; } /* no leaderboard page without a server */
CSS
touch "$OUT/.nojekyll"
echo "demo built in $OUT/ ($(du -h "$OUT/hollowmere.wasm" | cut -f1) of WebAssembly)"

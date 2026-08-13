// Package webui embeds the browser and Desktop renderer assets served by the
// local app-server.
package webui

import "embed"

// Assets contains the shared same-origin client.
//
//go:embed assets
var Assets embed.FS

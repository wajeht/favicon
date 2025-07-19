package assets

import "embed"

//go:embed "static" "migrations"
var Embeddedfiles embed.FS

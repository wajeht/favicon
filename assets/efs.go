package assets

import "embed"

//go:embed "static" "templates" "migrations"
var Embeddedfiles embed.FS

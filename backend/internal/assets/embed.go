package assets

import (
	"embed"
	"io/fs"
)

//go:embed frontend
var frontendEmbed embed.FS

// GetFrontendAssets returns the filesystem for the frontend assets.
// It strips the "frontend" prefix so index.html is at the root.
func GetFrontendAssets() (fs.FS, error) {
	return fs.Sub(frontendEmbed, "frontend")
}

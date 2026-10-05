// Package assets embeds the built Vue dashboard so the single Porter
// binary can serve it. It must live at the module root: go:embed paths are
// relative to the package directory and cannot contain "..", and web/dist
// is produced by `npm run build` at backend/web/dist.
package assets

import "embed"

//go:embed web/dist
var Dist embed.FS

// Package api embeds the public, versioned client contracts shipped in the binary.
package api

import "embed"

// Files deliberately contains only client documentation and synthetic fixtures.
//
//go:embed openapi.json schemas/*.json fixtures/*.json llms.txt llms-full.txt
var Files embed.FS

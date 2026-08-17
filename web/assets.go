package web

import "embed"

//go:embed operator overlay
var embedded embed.FS

// ReadAsset reads from immutable, independently rooted operator and overlay bundles.
func ReadAsset(name string) ([]byte, error) { return embedded.ReadFile(name) }

// Assets exposes the embedded tree for package-local conformance tests.
func Assets() embed.FS { return embedded }

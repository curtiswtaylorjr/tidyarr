// Package nodepath holds the single canonical triviality/min-depth rule shared
// by every node path-mapping validator — both the server-side node-auth gate
// (internal/api) and the node-side daemon gate (cmd/sakms-node).
//
// Before Stage 4 this rule existed as two independently-written near-duplicates
// (internal/api/nodes.go's mediaRootMinDepth/trivialMediaRoot and
// cmd/sakms-node/control_validate.go's pathMinDepth/trivialPath), each carrying
// its own magic "2". Stage 4 consolidates them here so there is structurally one
// implementation to reason about and test: the depth a nodePath (the mapping
// value) or a self-reported mediaRoot must reach to provide real containment.
package nodepath

import (
	"path/filepath"
	"strings"
)

// MinDepth is the minimum number of non-empty path segments a node path-mapping
// value (nodePath) or a self-reported mediaRoot must have to provide real
// containment. A filesystem root ("/") or a single-segment path ("/mnt") is
// trivially broad — a mapping to (or a mediaRoot at) such a path defeats the
// required-mediaRoot safeguard while still being "non-empty" (D9). Two segments
// ("/mnt/media") is the minimum that scopes to a real subtree.
const MinDepth = 2

// Trivial reports whether p is too shallow to provide meaningful containment:
// blank (after trimming), "/", ".", or fewer than MinDepth non-empty path
// segments after cleaning (so "/mnt", "/mnt/", "/mnt/." and "//" all count as
// the single-segment "/mnt", and "/" / "" are always trivial). It is a pure
// lexical check — it never touches the filesystem — so it is safe to call while
// holding a lock and identical on every GOOS.
func Trivial(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return true
	}
	cleaned := filepath.Clean(p)
	if cleaned == "/" || cleaned == "." {
		return true
	}
	segments := 0
	for _, seg := range strings.Split(cleaned, "/") {
		if seg != "" {
			segments++
		}
	}
	return segments < MinDepth
}

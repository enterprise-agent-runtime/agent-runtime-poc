//go:build !windows

package platform

// goldenSuffix is "-" + the first 8 hex of sha256("/tmp/Warden-Golden"):
// Unix paths are case-sensitive, so the key is cleaned but not lower-cased.
const goldenSuffix = "-865b998b"

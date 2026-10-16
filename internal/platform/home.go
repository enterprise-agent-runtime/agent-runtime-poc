package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultHome is the Warden home when WARDEN_HOME is not set: ~/.warden on
// Linux/macOS and %LOCALAPPDATA%\Warden on Windows.
func DefaultHome() (string, error) {
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "Warden"), nil
		}
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(u, ".warden"), nil
}

// HomeSuffix is "" for the default home and "-<8 hex>" for any other, so
// everything named after a home (the named pipe, the keychain service) of a
// test or second home never collides with the user's real one (D-011, M2-1).
func HomeSuffix(home string) string {
	def, err := DefaultHome()
	if err != nil {
		def = ""
	}
	return homeSuffix(home, def)
}

func homeSuffix(home, defaultHome string) string {
	if defaultHome != "" && sameHome(home, defaultHome) {
		return ""
	}
	h := sha256.Sum256([]byte(homeKey(home)))
	return "-" + hex.EncodeToString(h[:])[:8]
}

// homeKey normalizes a home for comparison and hashing; Windows paths are
// case-insensitive.
func homeKey(home string) string {
	k := filepath.Clean(home)
	if runtime.GOOS == "windows" {
		k = strings.ToLower(k)
	}
	return k
}

func sameHome(a, b string) bool { return homeKey(a) == homeKey(b) }

// KeychainService is the OS keychain service name for a home: "warden" for
// the default home, "warden-<suffix>" for any other, so a separate home has
// separate secrets (Secret Service, Keychain and Credential Manager alike).
func KeychainService(home string) string {
	def, err := DefaultHome()
	if err != nil {
		def = ""
	}
	return keychainService(home, def)
}

func keychainService(home, defaultHome string) string {
	return "warden" + homeSuffix(home, defaultHome)
}

package mcp

import (
	"runtime"
	"strings"
)

// CanonicalEnvironmentKey returns the environment-variable identity used by
// the current host process. Windows environment names are case-insensitive;
// other supported hosts retain exact spelling.
func CanonicalEnvironmentKey(key string) string {
	return canonicalEnvironmentKeyForOS(runtime.GOOS, key)
}

func canonicalEnvironmentKeyForOS(goos, key string) string {
	if goos == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

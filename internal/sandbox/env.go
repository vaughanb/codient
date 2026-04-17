// Package sandbox provides subprocess isolation: environment scrubbing and optional OS/container runners.
package sandbox

import (
	"os"
	"runtime"
	"strings"
)

// ScrubEnv returns a copy of environ with secrets removed and only safe + passthrough variables kept.
// It implements deny-by-default: variables not in the safe baseline and not listed in passthrough are dropped.
// Keys are compared case-sensitively on Unix and case-insensitively on Windows.
func ScrubEnv(environ []string, passthrough []string) []string {
	pt := make(map[string]struct{}, len(passthrough))
	for _, p := range passthrough {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		pt[normalizeKey(p)] = struct{}{}
	}

	var out []string
	seen := make(map[string]struct{})
	for _, pair := range environ {
		key, val, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		nk := normalizeKey(key)
		if _, dup := seen[nk]; dup {
			continue
		}
		if !keepEnvKey(key, nk, pt) {
			continue
		}
		seen[nk] = struct{}{}
		out = append(out, key+"="+val)
	}
	return out
}

// ScrubOSEnviron is ScrubEnv(os.Environ(), passthrough).
func ScrubOSEnviron(passthrough []string) []string {
	return ScrubEnv(os.Environ(), passthrough)
}

func normalizeKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(strings.TrimSpace(key))
	}
	return strings.TrimSpace(key)
}

func keepEnvKey(key, norm string, passthrough map[string]struct{}) bool {
	if _, ok := passthrough[norm]; ok {
		return true
	}
	if isSensitiveKey(norm) {
		return false
	}
	return isSafeBaselineKey(norm)
}

func isSensitiveKey(k string) bool {
	// Exact matches
	switch k {
	case "SSH_AUTH_SOCK", "NPM_TOKEN", "NPM_CONFIG_USERCONFIG", "NPM_CONFIG_GLOBALCONFIG":
		return true
	}
	// Prefixes (cloud / CI secrets)
	for _, p := range []string{
		"AWS_", "AZURE_", "AZUREHTTP_", "GCP_", "GOOGLE_APPLICATION_CREDENTIALS",
		"GITHUB_TOKEN", "GITLAB_TOKEN", "SLACK_TOKEN", "DOCKER_AUTH_CONFIG",
	} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	// GitHub Actions secrets often use GITHUB_ prefix for tokens
	if strings.HasPrefix(k, "GITHUB_") && (strings.Contains(k, "TOKEN") || strings.Contains(k, "SECRET")) {
		return true
	}
	// Suffixes
	for _, suf := range []string{
		"_TOKEN", "_SECRET", "_PASSWORD", "_CREDENTIALS", "_API_KEY", "_PRIVATE_KEY",
		"_ACCESS_KEY", "_SECRET_KEY", "_AUTH", "_BEARER",
	} {
		if strings.HasSuffix(k, suf) {
			return true
		}
	}
	// Broad "KEY" suffix (e.g. OPENAI_API_KEY) — keep GOPATH, GOROOT by excluding those
	if strings.HasSuffix(k, "_KEY") && k != "GOPATH" && k != "GOROOT" {
		return true
	}
	return false
}

func isSafeBaselineKey(k string) bool {
	switch k {
	case "PATH", "PATHEXT", "HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
		"USER", "USERNAME", "LOGNAME", "SHELL", "TERM", "TMPDIR", "TMP", "TEMP",
		"GOPATH", "GOROOT", "GOMODCACHE", "GOCACHE", "GOPROXY", "GOSUMDB", "GO111MODULE",
		"CGO_ENABLED", "CC", "CXX",
		"LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "LC_COLLATE",
		"SYSTEMROOT", "WINDIR", "COMSPEC", "PROMPT", "NUMBER_OF_PROCESSORS",
		"PROCESSOR_ARCHITECTURE", "OS", "COMPUTERNAME",
		"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME",
		"CI", "CONTINUOUS_INTEGRATION":
		return true
	}
	return strings.HasPrefix(k, "LC_")
}

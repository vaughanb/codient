package sandbox

import (
	"runtime"
	"strings"
	"testing"
)

func TestScrubEnv_StripsKnownSecrets(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"AWS_SECRET_ACCESS_KEY=secret",
		"AWS_ACCESS_KEY_ID=key",
		"GITHUB_TOKEN=ghp_x",
		"SSH_AUTH_SOCK=/tmp/sock",
		"NPM_TOKEN=npmsecret",
		"OPENAI_API_KEY=sk-xxx",
		"MY_CUSTOM_TOKEN=tok",
		"SAFE_VAR=keep", // not in baseline — should be dropped
	}
	out := ScrubEnv(in, nil)
	m := envMap(out)
	if m["AWS_SECRET_ACCESS_KEY"] != "" {
		t.Errorf("AWS_SECRET_ACCESS_KEY should be stripped")
	}
	if m["GITHUB_TOKEN"] != "" {
		t.Errorf("GITHUB_TOKEN should be stripped")
	}
	if m["SSH_AUTH_SOCK"] != "" {
		t.Errorf("SSH_AUTH_SOCK should be stripped")
	}
	if m["NPM_TOKEN"] != "" {
		t.Errorf("NPM_TOKEN should be stripped")
	}
	if m["OPENAI_API_KEY"] != "" {
		t.Errorf("OPENAI_API_KEY should be stripped")
	}
	if m["MY_CUSTOM_TOKEN"] != "" {
		t.Errorf("MY_CUSTOM_TOKEN should be stripped")
	}
	if m["SAFE_VAR"] != "" {
		t.Errorf("unknown SAFE_VAR should be dropped by deny-default")
	}
}

func TestScrubEnv_KeepsSafeDefaults(t *testing.T) {
	in := []string{
		"PATH=/x",
		"HOME=/h",
		"GOPATH=/go",
		"GOROOT=/goroot",
		"TERM=xterm",
		"LANG=en_US.UTF-8",
		"LC_ALL=C",
		"TMPDIR=/tmp",
		"USERPROFILE=C:\\Users\\x",
		"SYSTEMROOT=C:\\Windows",
	}
	out := ScrubEnv(in, nil)
	m := envMap(out)
	for _, k := range []string{"PATH", "HOME", "GOPATH", "GOROOT", "TERM", "LANG", "LC_ALL", "TMPDIR"} {
		if m[k] == "" && k != "USERPROFILE" && k != "SYSTEMROOT" {
			t.Errorf("expected %s to be kept, got %v", k, m[k])
		}
	}
	if runtime.GOOS == "windows" {
		if m["USERPROFILE"] == "" || m["SYSTEMROOT"] == "" {
			t.Errorf("expected USERPROFILE and SYSTEMROOT on Windows")
		}
	}
}

func TestScrubEnv_PassthroughOverride(t *testing.T) {
	in := []string{
		"PATH=/bin",
		"MY_SECRET_TOKEN=bad",
		"CUSTOM_SAFE=ok",
	}
	out := ScrubEnv(in, []string{"MY_SECRET_TOKEN", "CUSTOM_SAFE"})
	m := envMap(out)
	if m["MY_SECRET_TOKEN"] != "bad" {
		t.Errorf("passthrough should allow MY_SECRET_TOKEN: %v", m)
	}
	if m["CUSTOM_SAFE"] != "ok" {
		t.Errorf("passthrough should allow CUSTOM_SAFE: %v", m)
	}
}

func TestScrubEnv_PatternMatching(t *testing.T) {
	tests := []struct {
		key   string
		strip bool
	}{
		{"AWS_DEFAULT_REGION", true},
		{"AZURE_CLIENT_SECRET", true},
		{"RANDOM_SECRET", true},
		{"GOPATH", false},
		{"PATH", false},
	}
	for _, tt := range tests {
		in := []string{tt.key + "=v"}
		out := ScrubEnv(in, nil)
		m := envMap(out)
		_, kept := m[tt.key]
		if tt.strip && kept {
			t.Errorf("%s: expected stripped, kept", tt.key)
		}
		if !tt.strip && !kept {
			t.Errorf("%s: expected kept, stripped", tt.key)
		}
	}
}

func TestScrubEnv_EmptyAndEdgeCases(t *testing.T) {
	if len(ScrubEnv(nil, nil)) != 0 {
		t.Fatal("nil env")
	}
	in := []string{
		"",
		"NOEQUAL",
		"PATH=/a",
		"PATH=/b",
	}
	out := ScrubEnv(in, nil)
	m := envMap(out)
	if m["PATH"] != "/a" {
		t.Errorf("first PATH wins: got %q", m["PATH"])
	}
	// Value may contain '=' (split on first '=' only)
	if got := envMap(ScrubEnv([]string{"PATH=x=y=z"}, nil)); got["PATH"] != "x=y=z" {
		t.Errorf("PATH value with extra =: %v", got)
	}
}

func envMap(pairs []string) map[string]string {
	m := make(map[string]string)
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	return m
}

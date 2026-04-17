package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("CODIENT_STATE_DIR", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != defaultBaseURL {
		t.Fatalf("BaseURL: got %q", c.BaseURL)
	}
	if c.APIKey != defaultAPIKey {
		t.Fatalf("APIKey: got %q", c.APIKey)
	}
	if c.Model != "" {
		t.Fatalf("Model should be empty by default: got %q", c.Model)
	}
	if c.MaxConcurrent != defaultMaxConcurrent {
		t.Fatalf("MaxConcurrent: got %d", c.MaxConcurrent)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wantWS, err := filepath.Abs(wd)
	if err != nil {
		t.Fatal(err)
	}
	if c.Workspace != wantWS {
		t.Fatalf("Workspace default cwd: got %q want %q", c.Workspace, wantWS)
	}
	if len(c.ExecAllowlist) != 3 || c.ExecAllowlist[0] != "go" || c.ExecAllowlist[1] != "git" {
		t.Fatalf("default ExecAllowlist: %#v", c.ExecAllowlist)
	}
	wantShell := "sh"
	if runtime.GOOS == "windows" {
		wantShell = "cmd"
	}
	if c.ExecAllowlist[2] != wantShell {
		t.Fatalf("default ExecAllowlist shell: got %q want %q", c.ExecAllowlist[2], wantShell)
	}
	if !c.FetchPreapproved {
		t.Fatal("expected FetchPreapproved true by default")
	}
	if !c.StreamReply {
		t.Fatal("expected StreamReply true by default")
	}
	if !c.DesignSave {
		t.Fatal("expected DesignSave true by default")
	}
	if c.AutoCompactPct != defaultAutoCompactPct {
		t.Fatalf("AutoCompactPct: got %d", c.AutoCompactPct)
	}
	if c.FetchWebRatePerSec != 0 || c.FetchWebRateBurst != 0 {
		t.Fatalf("default fetch web rate should be off: got %d/%d", c.FetchWebRatePerSec, c.FetchWebRateBurst)
	}
	if !c.GitAutoCommit {
		t.Fatal("expected GitAutoCommit true by default")
	}
	if len(c.GitProtectedBranches) != 3 || c.GitProtectedBranches[0] != "main" {
		t.Fatalf("default GitProtectedBranches: %#v", c.GitProtectedBranches)
	}
	if c.SandboxMode != "off" {
		t.Fatalf("SandboxMode default: got %q", c.SandboxMode)
	}
}

func TestValidateSandbox_Off(t *testing.T) {
	c := &Config{SandboxMode: "off"}
	if err := ValidateSandbox(c); err != nil {
		t.Fatal(err)
	}
}

func TestParseGitProtectedBranches(t *testing.T) {
	got := ParseGitProtectedBranches(" Main , develop ,main ")
	if len(got) != 2 || got[0] != "main" || got[1] != "develop" {
		t.Fatalf("got %#v", got)
	}
}

func TestLoad_FromConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	pc := &PersistentConfig{
		BaseURL:       "http://example.com/v1/",
		APIKey:        "secret",
		Model:         "m1",
		Workspace:     "/w",
		MaxConcurrent: 2,
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://example.com/v1" {
		t.Fatalf("BaseURL trim: got %q", c.BaseURL)
	}
	if c.APIKey != "secret" || c.Model != "m1" {
		t.Fatalf("credentials: %+v", c)
	}
	if c.MaxConcurrent != 2 {
		t.Fatalf("MaxConcurrent: got %d", c.MaxConcurrent)
	}
	if c.Workspace != "/w" {
		t.Fatalf("workspace: got %q", c.Workspace)
	}
}

func TestLoad_FetchWebRateClamped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{
		FetchWebRatePerSec: 500,
		FetchWebRateBurst:  200,
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.FetchWebRatePerSec != MaxFetchWebRatePerSec {
		t.Fatalf("rate: got %d want %d", c.FetchWebRatePerSec, MaxFetchWebRatePerSec)
	}
	if c.FetchWebRateBurst != MaxFetchWebRateBurst {
		t.Fatalf("burst: got %d want %d", c.FetchWebRateBurst, MaxFetchWebRateBurst)
	}
}

func TestLoad_InvalidMaxConcurrent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{MaxConcurrent: -1}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRequireModel(t *testing.T) {
	c := &Config{Model: ""}
	if err := c.RequireModel(); err == nil {
		t.Fatal("expected error")
	}
	if err := c.RequireModel(); err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
	c.Model = "x"
	if err := c.RequireModel(); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_ExecDisable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{ExecDisable: true}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ExecAllowlist) != 0 {
		t.Fatalf("expected empty allowlist when disabled: %#v", c.ExecAllowlist)
	}
}

func TestLoad_ExecAllowlist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{
		ExecAllowlist:   "go, Git ,GO.exe, git",
		ExecTimeoutSec:  45,
		ExecMaxOutBytes: 4096,
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ExecAllowlist) != 2 {
		t.Fatalf("deduped allowlist: %#v", c.ExecAllowlist)
	}
	if c.ExecAllowlist[0] != "go" || c.ExecAllowlist[1] != "git" {
		t.Fatalf("order/content: %#v", c.ExecAllowlist)
	}
	if c.ExecTimeoutSeconds != 45 || c.ExecMaxOutputBytes != 4096 {
		t.Fatalf("exec limits: timeout=%d out=%d", c.ExecTimeoutSeconds, c.ExecMaxOutputBytes)
	}
}

func TestLoad_ExecTimeoutClamp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{
		ExecTimeoutSec:  999999,
		ExecMaxOutBytes: 999999999,
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ExecTimeoutSeconds != maxExecTimeoutSec {
		t.Fatalf("timeout clamp: got %d", c.ExecTimeoutSeconds)
	}
	if c.ExecMaxOutputBytes != maxExecMaxOutputBytes {
		t.Fatalf("output clamp: got %d", c.ExecMaxOutputBytes)
	}
}

func TestEffectiveWorkspace(t *testing.T) {
	c := &Config{Workspace: "/a"}
	if c.EffectiveWorkspace() != "/a" {
		t.Fatalf("got %q", c.EffectiveWorkspace())
	}
	c.Workspace = "  "
	if c.EffectiveWorkspace() != "" {
		t.Fatalf("whitespace-only: got %q", c.EffectiveWorkspace())
	}
}

func TestPersistentConfig_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	pc := &PersistentConfig{
		BaseURL: "http://myserver:8080/v1",
		APIKey:  "sk-test-key",
		Model:   "my-model-id",
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadPersistentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BaseURL != pc.BaseURL {
		t.Fatalf("BaseURL: got %q want %q", loaded.BaseURL, pc.BaseURL)
	}
	if loaded.APIKey != pc.APIKey {
		t.Fatalf("APIKey: got %q want %q", loaded.APIKey, pc.APIKey)
	}
	if loaded.Model != pc.Model {
		t.Fatalf("Model: got %q want %q", loaded.Model, pc.Model)
	}

	path := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("config.json not valid JSON: %v", err)
	}
}

func TestPersistentConfig_MissingFile(t *testing.T) {
	t.Setenv("CODIENT_STATE_DIR", t.TempDir())

	pc, err := LoadPersistentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if pc.BaseURL != "" || pc.APIKey != "" || pc.Model != "" {
		t.Fatalf("expected empty defaults, got %+v", pc)
	}
}

func TestLoad_SearchDefaults(t *testing.T) {
	t.Setenv("CODIENT_STATE_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SearchMaxResults != defaultSearchMaxResults {
		t.Fatalf("SearchMaxResults default: got %d", c.SearchMaxResults)
	}
}

func TestLoad_FetchHosts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{FetchAllowHosts: "file.example.com, env.example.com"}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.FetchAllowHosts) != 2 {
		t.Fatalf("fetch hosts: %#v", c.FetchAllowHosts)
	}
}

func TestLoad_FetchPreapprovedDefault(t *testing.T) {
	t.Setenv("CODIENT_STATE_DIR", t.TempDir())
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.FetchPreapproved {
		t.Fatal("expected FetchPreapproved true by default")
	}
}

func TestLoad_FetchPreapprovedDisabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	f := false
	pc := &PersistentConfig{FetchPreapproved: &f}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.FetchPreapproved {
		t.Fatal("expected FetchPreapproved false")
	}
}

func TestAppendPersistentFetchHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	if err := AppendPersistentFetchHost("Example.COM"); err != nil {
		t.Fatal(err)
	}
	if err := AppendPersistentFetchHost("example.com"); err != nil {
		t.Fatal(err)
	}
	pc, err := LoadPersistentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if pc.FetchAllowHosts != "example.com" {
		t.Fatalf("got %q", pc.FetchAllowHosts)
	}
}

func TestLoad_SearchFromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{
		SearchMaxResults: 8,
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SearchMaxResults != 8 {
		t.Fatalf("SearchMaxResults: got %d", c.SearchMaxResults)
	}
}

func TestLoad_SearchMaxResultsClamp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{SearchMaxResults: 50}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SearchMaxResults != maxSearchMaxResults {
		t.Fatalf("SearchMaxResults clamp: got %d want %d", c.SearchMaxResults, maxSearchMaxResults)
	}
}

func TestPersistentConfig_FeedsIntoLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	if err := SavePersistentConfig(&PersistentConfig{Model: "persisted-model"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Model != "persisted-model" {
		t.Fatalf("model from config file: got %q", c.Model)
	}
	if c.BaseURL != defaultBaseURL {
		t.Fatalf("BaseURL should default: got %q", c.BaseURL)
	}
}

func TestLoad_StreamWithToolsConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	pc := &PersistentConfig{StreamWithTools: true}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.StreamWithTools {
		t.Fatal("expected StreamWithTools true")
	}
}

func TestLoad_StreamReplyExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	f := false
	pc := &PersistentConfig{StreamReply: &f}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.StreamReply {
		t.Fatal("expected StreamReply false when explicitly set")
	}
}

func TestLoad_DesignSaveExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)
	f := false
	pc := &PersistentConfig{DesignSave: &f}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DesignSave {
		t.Fatal("expected DesignSave false when explicitly set")
	}
}

func TestConfigToPersistent_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	cfg := &Config{
		BaseURL:            "http://test/v1",
		APIKey:             "key",
		Model:              "m",
		MaxConcurrent:      5,
		FetchPreapproved:   false,
		StreamReply:        false,
		DesignSave:         false,
		FetchWebRatePerSec: 8,
		FetchWebRateBurst:  3,
	}
	pc := ConfigToPersistent(cfg)
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "http://test/v1" || c.Model != "m" || c.MaxConcurrent != 5 {
		t.Fatalf("round-trip failed: %+v", c)
	}
	if c.FetchPreapproved || c.StreamReply || c.DesignSave {
		t.Fatalf("*bool round-trip failed: fetch=%v stream=%v design=%v", c.FetchPreapproved, c.StreamReply, c.DesignSave)
	}
	if c.FetchWebRatePerSec != 8 || c.FetchWebRateBurst != 3 {
		t.Fatalf("fetch web rate round-trip: got %d/%d", c.FetchWebRatePerSec, c.FetchWebRateBurst)
	}
}

func TestSchemaVersionMigration_Version0(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	// Create a config file without schema_version (version 0).
	configPath := filepath.Join(dir, "config.json")
	oldConfig := `{"base_url":"http://old/v1","model":"old-model"}`
	if err := os.WriteFile(configPath, []byte(oldConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	pc, err := LoadPersistentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if pc.SchemaVersion != 1 {
		t.Fatalf("expected migration to version 1, got %d", pc.SchemaVersion)
	}
	if pc.BaseURL != "http://old/v1" || pc.Model != "old-model" {
		t.Fatalf("data should be preserved: base_url=%q model=%q", pc.BaseURL, pc.Model)
	}
}

func TestSchemaVersionMigration_CurrentVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	pc := &PersistentConfig{
		SchemaVersion: currentSchemaVersion,
		Model:         "test-model",
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadPersistentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != currentSchemaVersion {
		t.Fatalf("version should remain %d, got %d", currentSchemaVersion, loaded.SchemaVersion)
	}
	if loaded.Model != "test-model" {
		t.Fatalf("model: got %q", loaded.Model)
	}
}

func TestSchemaVersionMigration_FutureVersion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	// Manually write a config with a future schema version.
	configPath := filepath.Join(dir, "config.json")
	futureConfig := `{"schema_version":99,"model":"future-model"}`
	if err := os.WriteFile(configPath, []byte(futureConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPersistentConfig()
	if err == nil {
		t.Fatal("expected error when loading future schema version")
	}
	if !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("error should mention newer version: %v", err)
	}
	if !strings.Contains(err.Error(), "99") {
		t.Fatalf("error should mention version 99: %v", err)
	}
}

func TestSchemaVersionAlwaysStamped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	pc := &PersistentConfig{Model: "test"}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	version, ok := raw["schema_version"]
	if !ok {
		t.Fatal("schema_version field missing in saved config")
	}
	versionFloat, ok := version.(float64)
	if !ok {
		t.Fatalf("schema_version should be numeric, got %T", version)
	}
	if int(versionFloat) != currentSchemaVersion {
		t.Fatalf("schema_version should be %d, got %d", currentSchemaVersion, int(versionFloat))
	}
}

func TestLastMode_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	if g := LoadLastMode(); g != "" {
		t.Fatalf("LoadLastMode before save: want empty, got %q", g)
	}
	SaveLastMode("plan")
	if g := LoadLastMode(); g != "plan" {
		t.Fatalf("LoadLastMode after SaveLastMode(plan): got %q", g)
	}
	SaveLastMode("design") // alias → plan
	if g := LoadLastMode(); g != "plan" {
		t.Fatalf("LoadLastMode after SaveLastMode(design): got %q", g)
	}
	SaveLastMode("bogus")
	if g := LoadLastMode(); g != "plan" {
		t.Fatalf("invalid save should not clobber: got %q", g)
	}
}

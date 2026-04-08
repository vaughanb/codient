package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
	if c.SearchBaseURL != "" {
		t.Fatalf("SearchBaseURL should be empty by default: got %q", c.SearchBaseURL)
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
		SearchBaseURL:    "http://localhost:8080",
		SearchMaxResults: 8,
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SearchBaseURL != "http://localhost:8080" {
		t.Fatalf("SearchBaseURL: got %q", c.SearchBaseURL)
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
		BaseURL:          "http://test/v1",
		APIKey:           "key",
		Model:            "m",
		MaxConcurrent:    5,
		SearchBaseURL:    "http://search",
		FetchPreapproved: false,
		StreamReply:      false,
		DesignSave:       false,
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
}

func TestEffectiveModelConfig_NoOverrides(t *testing.T) {
	c := &Config{
		BaseURL: "http://default/v1",
		APIKey:  "default-key",
		Model:   "default-model",
	}
	base, key, model := c.EffectiveModelConfig("plan")
	if base != "http://default/v1" || key != "default-key" || model != "default-model" {
		t.Fatalf("expected defaults, got base=%q key=%q model=%q", base, key, model)
	}
}

func TestEffectiveModelConfig_PartialOverride(t *testing.T) {
	c := &Config{
		BaseURL: "http://default/v1",
		APIKey:  "default-key",
		Model:   "default-model",
		Models: map[string]*ModelProfile{
			"plan": {Model: "plan-model"},
		},
	}
	base, key, model := c.EffectiveModelConfig("plan")
	if base != "http://default/v1" {
		t.Fatalf("base should inherit default: got %q", base)
	}
	if key != "default-key" {
		t.Fatalf("api_key should inherit default: got %q", key)
	}
	if model != "plan-model" {
		t.Fatalf("model should be overridden: got %q", model)
	}
}

func TestEffectiveModelConfig_FullOverride(t *testing.T) {
	c := &Config{
		BaseURL: "http://default/v1",
		APIKey:  "default-key",
		Model:   "default-model",
		Models: map[string]*ModelProfile{
			"build": {
				BaseURL: "http://build-server/v1",
				APIKey:  "build-key",
				Model:   "build-model",
			},
		},
	}
	base, key, model := c.EffectiveModelConfig("build")
	if base != "http://build-server/v1" {
		t.Fatalf("base: got %q", base)
	}
	if key != "build-key" {
		t.Fatalf("api_key: got %q", key)
	}
	if model != "build-model" {
		t.Fatalf("model: got %q", model)
	}

	// Unrelated mode should still get defaults.
	base2, key2, model2 := c.EffectiveModelConfig("ask")
	if base2 != "http://default/v1" || key2 != "default-key" || model2 != "default-model" {
		t.Fatalf("ask should get defaults: base=%q key=%q model=%q", base2, key2, model2)
	}
}

func TestEffectiveModel(t *testing.T) {
	c := &Config{
		Model: "default",
		Models: map[string]*ModelProfile{
			"plan": {Model: "planner"},
		},
	}
	if got := c.EffectiveModel("plan"); got != "planner" {
		t.Fatalf("got %q", got)
	}
	if got := c.EffectiveModel("build"); got != "default" {
		t.Fatalf("got %q", got)
	}
}

func TestHasModeOverrides(t *testing.T) {
	c := &Config{}
	if c.HasModeOverrides() {
		t.Fatal("empty config should not have overrides")
	}
	c.Models = map[string]*ModelProfile{"plan": {Model: "x"}}
	if !c.HasModeOverrides() {
		t.Fatal("should detect override")
	}
	c.Models = map[string]*ModelProfile{"plan": {}}
	if c.HasModeOverrides() {
		t.Fatal("all-empty profile should not count as override")
	}
}

func TestConfigToPersistent_Models_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	cfg := &Config{
		BaseURL:       "http://test/v1",
		APIKey:        "key",
		Model:         "default",
		MaxConcurrent: 3,
		Models: map[string]*ModelProfile{
			"plan": {
				BaseURL: "http://plan-server/v1",
				Model:   "plan-model",
			},
			"build": {
				Model: "build-model",
			},
		},
	}
	pc := ConfigToPersistent(cfg)
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Models == nil {
		t.Fatal("Models should not be nil after round-trip")
	}
	if c.Models["plan"] == nil {
		t.Fatal("plan profile missing")
	}
	if c.Models["plan"].BaseURL != "http://plan-server/v1" {
		t.Fatalf("plan base_url: got %q", c.Models["plan"].BaseURL)
	}
	if c.Models["plan"].Model != "plan-model" {
		t.Fatalf("plan model: got %q", c.Models["plan"].Model)
	}
	if c.Models["build"] == nil || c.Models["build"].Model != "build-model" {
		t.Fatalf("build profile: %+v", c.Models["build"])
	}
	if c.Models["build"].BaseURL != "" {
		t.Fatalf("build base_url should be empty: got %q", c.Models["build"].BaseURL)
	}
}

func TestLoad_ModelsFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODIENT_STATE_DIR", dir)

	pc := &PersistentConfig{
		BaseURL: "http://default/v1",
		APIKey:  "key",
		Model:   "default",
		Models: map[string]*PersistentModelProfile{
			"plan":  {Model: "plan-model", BaseURL: "http://plan/v1"},
			"build": {Model: "build-model"},
		},
	}
	if err := SavePersistentConfig(pc); err != nil {
		t.Fatal(err)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	base, _, model := c.EffectiveModelConfig("plan")
	if base != "http://plan/v1" || model != "plan-model" {
		t.Fatalf("plan effective: base=%q model=%q", base, model)
	}
	base2, _, model2 := c.EffectiveModelConfig("build")
	if base2 != "http://default/v1" || model2 != "build-model" {
		t.Fatalf("build effective: base=%q model=%q", base2, model2)
	}
	base3, _, model3 := c.EffectiveModelConfig("ask")
	if base3 != "http://default/v1" || model3 != "default" {
		t.Fatalf("ask effective: base=%q model=%q", base3, model3)
	}
}

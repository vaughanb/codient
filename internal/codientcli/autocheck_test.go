package codientcli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codient/internal/config"
)

func TestDetectAutoCheckCmd(t *testing.T) {
	if got := detectAutoCheckCmd(""); got != "" {
		t.Fatalf("empty: got %q", got)
	}
	dir := t.TempDir()
	if got := detectAutoCheckCmd(dir); got != "" {
		t.Fatalf("no markers: got %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectAutoCheckCmd(dir); got != "go build ./..." {
		t.Fatalf("go.mod: got %q", got)
	}

	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "Cargo.toml"), []byte("[package]\nname=\"x\"\nversion=\"0.1.0\"\nedition=\"2021\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectAutoCheckCmd(dir2); got != "cargo check" {
		t.Fatalf("Cargo.toml: got %q", got)
	}

	dir3 := t.TempDir()
	pkg := `{"scripts":{"build":"echo x"},"name":"x"}`
	if err := os.WriteFile(filepath.Join(dir3, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectAutoCheckCmd(dir3); got != "npm run build" {
		t.Fatalf("package.json build: got %q", got)
	}

	dir4 := t.TempDir()
	pkg2 := `{"name":"x"}`
	if err := os.WriteFile(filepath.Join(dir4, "package.json"), []byte(pkg2), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir4, "tsconfig.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectAutoCheckCmd(dir4); got != "npx tsc --noEmit" {
		t.Fatalf("tsconfig: got %q", got)
	}

	dir5 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir5, "pyproject.toml"), []byte("[project]\nname=\"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectAutoCheckCmd(dir5); got != "python -m compileall -q ." {
		t.Fatalf("pyproject: got %q", got)
	}
}

func TestEffectiveAutoCheckCmd(t *testing.T) {
	cfg := &config.Config{AutoCheckCmd: "off"}
	if effectiveAutoCheckCmd(cfg) != "" {
		t.Fatal("off should disable")
	}
	cfg = &config.Config{AutoCheckCmd: "  custom  "}
	if effectiveAutoCheckCmd(cfg) != "custom" {
		t.Fatalf("explicit: got %q", effectiveAutoCheckCmd(cfg))
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{Workspace: dir}
	if effectiveAutoCheckCmd(cfg) != "go build ./..." {
		t.Fatalf("auto: got %q", effectiveAutoCheckCmd(cfg))
	}
}

func TestMakeAutoCheck(t *testing.T) {
	dir := t.TempDir()
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "exit /b 0"
	} else {
		cmd = "exit 0"
	}
	ac := makeAutoCheck(dir, cmd, 5*time.Second, 4096, nil)
	out := ac(context.Background())
	if out.Inject != "" {
		t.Fatalf("success should not inject: %q", out.Inject)
	}
	if out.Progress == "" {
		t.Fatal("expected progress line on success")
	}
	if !strings.Contains(out.Progress, "auto-check [build]:") || !strings.Contains(out.Progress, "exit=0") {
		t.Fatalf("unexpected progress: %q", out.Progress)
	}

	if runtime.GOOS == "windows" {
		cmd = "exit /b 1"
	} else {
		cmd = "exit 1"
	}
	ac = makeAutoCheck(dir, cmd, 5*time.Second, 4096, nil)
	out = ac(context.Background())
	if out.Inject == "" || out.Progress == "" {
		t.Fatalf("failure should inject: inject=%q progress=%q", out.Inject, out.Progress)
	}
	if !strings.Contains(out.Inject, "[auto-check]") || !strings.Contains(out.Inject, "build errors") {
		t.Fatalf("expected build label in inject: %q", out.Inject)
	}
}

func TestDetectLintCmd_Cargo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"x\"\nversion=\"0.1.0\"\nedition=\"2021\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLintCmd(dir); got != "cargo clippy -- -D warnings" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectLintCmd_NPM(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"lint":"eslint ."}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := detectLintCmd(dir); got != "npm run lint" {
		t.Fatalf("got %q", got)
	}
}

func TestEffectiveLintCmd_OffAndExplicit(t *testing.T) {
	cfg := &config.Config{LintCmd: "off"}
	if effectiveLintCmd(cfg) != "" {
		t.Fatal("off should disable")
	}
	cfg = &config.Config{LintCmd: "  mylint  "}
	if effectiveLintCmd(cfg) != "mylint" {
		t.Fatalf("got %q", effectiveLintCmd(cfg))
	}
}

func TestEffectiveTestCmd_OffAndExplicit(t *testing.T) {
	cfg := &config.Config{TestCmd: "off"}
	if effectiveTestCmd(cfg) != "" {
		t.Fatal("off should disable")
	}
	cfg = &config.Config{TestCmd: "  mytest  "}
	if effectiveTestCmd(cfg) != "mytest" {
		t.Fatalf("got %q", effectiveTestCmd(cfg))
	}
}

func TestBuildAutoCheckSteps_Order(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Workspace:    dir,
		AutoCheckCmd: "off",
		LintCmd:      "off",
		TestCmd:      "off",
	}
	steps := buildAutoCheckSteps(cfg)
	if len(steps) != 0 {
		t.Fatalf("all off: want no steps, got %#v", steps)
	}
	cfg = &config.Config{Workspace: dir}
	steps = buildAutoCheckSteps(cfg)
	if len(steps) < 1 {
		t.Fatalf("expected at least build step: %#v", steps)
	}
	if steps[0].label != "build" || steps[0].cmdLine != "go build ./..." {
		t.Fatalf("first step: %#v", steps[0])
	}
}

func TestMakeAutoCheckSequence_FailFastOnBuild(t *testing.T) {
	dir := t.TempDir()
	var failCmd, okCmd string
	if runtime.GOOS == "windows" {
		failCmd = "exit /b 1"
		okCmd = "exit /b 0"
	} else {
		failCmd = "exit 1"
		okCmd = "exit 0"
	}
	seq := makeAutoCheckSequence(dir, []autoCheckStep{
		{"build", failCmd},
		{"lint", okCmd},
	}, 5*time.Second, 4096, nil)
	out := seq(context.Background())
	if out.Inject == "" {
		t.Fatal("expected inject on build failure")
	}
	if !strings.Contains(out.Inject, "build errors") {
		t.Fatalf("want build failure: %q", out.Inject)
	}
	if strings.Contains(out.Inject, "lint errors") {
		t.Fatalf("lint should not run after build fail: %q", out.Inject)
	}
}

func TestMakeAutoCheckSequence_TwoStepsPass(t *testing.T) {
	dir := t.TempDir()
	var okCmd string
	if runtime.GOOS == "windows" {
		okCmd = "exit /b 0"
	} else {
		okCmd = "exit 0"
	}
	seq := makeAutoCheckSequence(dir, []autoCheckStep{
		{"build", okCmd},
		{"lint", okCmd},
	}, 5*time.Second, 4096, nil)
	out := seq(context.Background())
	if out.Inject != "" {
		t.Fatalf("unexpected inject: %q", out.Inject)
	}
	p := out.Progress
	if !strings.Contains(p, "[build]:") || !strings.Contains(p, "[lint]:") || !strings.Contains(p, "exit=0") {
		t.Fatalf("want both progress lines: %q", out.Progress)
	}
}

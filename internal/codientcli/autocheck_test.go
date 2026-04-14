package codientcli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
}

package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildContainerRunArgs_NetworkNone(t *testing.T) {
	dir := t.TempDir()
	args, err := BuildContainerRunArgs("docker", "alpine:3.20", dir, Policy{}, []string{"echo", "hi"}, "/tmp/env")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(args, " ")
	if !strings.Contains(s, "--network=none") {
		t.Fatalf("missing --network=none: %s", s)
	}
	if !strings.Contains(s, "--read-only") {
		t.Fatalf("missing --read-only: %s", s)
	}
}

func TestBuildContainerRunArgs_ResourceLimits(t *testing.T) {
	dir := t.TempDir()
	p := Policy{MaxMemoryMB: 512, MaxCPUPercent: 50, MaxProcesses: 100}
	args, err := BuildContainerRunArgs("podman", "img", dir, p, []string{"id"}, "/e")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(args, " ")
	for _, want := range []string{"--memory=512m", "--cpus=0.5", "--pids-limit=100"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
}

func TestBuildContainerRunArgs_WorkspaceVolume(t *testing.T) {
	dir := t.TempDir()
	abs, _ := filepath.Abs(dir)
	args, err := BuildContainerRunArgs("docker", "", abs, Policy{}, []string{"pwd"}, "/tmp/e")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-v" && strings.HasPrefix(args[i+1], abs) && strings.Contains(args[i+1], ":/workspace:rw") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("volume mount not found: %v", args)
	}
}

func TestContainerRunner_Available(t *testing.T) {
	c := NewContainerRunner("")
	_ = c.Available() // may be true or false depending on host
}

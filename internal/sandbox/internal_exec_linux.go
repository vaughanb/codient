//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/landlock-lsm/go-landlock/landlock"
	llsyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"golang.org/x/sys/unix"
)

// linuxSandboxPayload is written by LinuxRunner and read by the re-exec child.
type linuxSandboxPayload struct {
	Argv    []string `json:"argv"`
	WorkDir string   `json:"work_dir"`
	Env     []string `json:"env"`
	RWDirs  []string `json:"rw"`
	RODirs  []string `json:"ro"`
}

// RunInternalSandboxExec applies Landlock, seccomp, and execs argv from a JSON payload file (path argv).
func RunInternalSandboxExec(payloadPath string) int {
	data, err := os.ReadFile(payloadPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "codient sandbox: read payload: %v\n", err)
		return 2
	}
	var p linuxSandboxPayload
	if err := json.Unmarshal(data, &p); err != nil {
		fmt.Fprintf(os.Stderr, "codient sandbox: json: %v\n", err)
		return 2
	}
	if len(p.Argv) == 0 {
		fmt.Fprintf(os.Stderr, "codient sandbox: empty argv\n")
		return 2
	}

	ro := append([]string(nil), p.RODirs...)
	rw := append([]string(nil), p.RWDirs...)
	if len(ro) == 0 {
		ro = defaultLinuxRODirs()
	}

	if err := applyLinuxSeccomp(); err != nil {
		fmt.Fprintf(os.Stderr, "codient sandbox: seccomp: %v\n", err)
	}

	if err := landlock.V8.BestEffort().RestrictPaths(
		landlock.RODirs(ro...),
		landlock.RWDirs(rw...),
	); err != nil {
		fmt.Fprintf(os.Stderr, "codient sandbox: landlock: %v\n", err)
	}

	argv0 := p.Argv[0]
	env := p.Env
	if env == nil {
		env = []string{}
	}
	wd := p.WorkDir
	if wd != "" {
		if err := unix.Chdir(wd); err != nil {
			fmt.Fprintf(os.Stderr, "codient sandbox: chdir: %v\n", err)
			return 2
		}
	}

	err = unix.Exec(argv0, p.Argv, env)
	fmt.Fprintf(os.Stderr, "codient sandbox: exec: %v\n", err)
	return 126
}

func defaultLinuxRODirs() []string {
	return []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/opt"}
}

func applyLinuxSeccomp() error {
	if !seccomp.Supported() {
		return nil
	}
	filter := seccomp.Filter{
		NoNewPrivs: true,
		Flag:       seccomp.FilterFlagTSync,
		Policy: seccomp.Policy{
			DefaultAction: seccomp.ActionAllow,
			Syscalls: []seccomp.SyscallGroup{{
				Action: seccomp.ActionErrno,
				Names: []string{
					"ptrace", "mount", "umount2", "pivot_root", "chroot", "reboot",
					"kexec_load", "init_module", "finit_module", "delete_module",
					"process_vm_readv", "process_vm_writev", "userfaultfd",
				},
			}},
		},
	}
	return seccomp.LoadFilter(filter)
}

// LinuxLandlockSupported returns true if the kernel exposes Landlock ABI v1+.
func LinuxLandlockSupported() bool {
	v, err := llsyscall.LandlockGetABIVersion()
	return err == nil && v >= 1
}

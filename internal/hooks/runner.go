package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"codient/internal/sandbox"
)

const (
	defaultHookTimeoutSec = 600
	exitCodeBlock         = 2
)

// hookOutput is a lenient merge of plan + Codex-style fields.
type hookOutput struct {
	Decision          string `json:"decision"`
	Reason            string `json:"reason"`
	AdditionalContext string `json:"additional_context"`
	SystemMessage     string `json:"system_message"`
	Continue          *bool  `json:"continue"`
	StopReason        string `json:"stopReason"`
}

func parseHookOutput(stdout, stderr []byte, exitCode int) (hookOutput, error) {
	out := hookOutput{}
	s := strings.TrimSpace(string(stdout))
	if s == "" {
		if exitCode == exitCodeBlock {
			reason := strings.TrimSpace(string(stderr))
			if reason == "" {
				reason = "blocked by hook (exit code 2)"
			}
			out.Decision = "block"
			out.Reason = reason
			return out, nil
		}
		return out, nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return hookOutput{}, err
	}
	// Codex nested shape: hookSpecificOutput.additionalContext, permissionDecision
	var wrap struct {
		HookSpecificOutput json.RawMessage `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(s), &wrap); err == nil && len(wrap.HookSpecificOutput) > 0 {
		var nested struct {
			AdditionalContext   string `json:"additionalContext"`
			PermissionDecision  string `json:"permissionDecision"`
			HookEventName       string `json:"hookEventName"`
		}
		if err := json.Unmarshal(wrap.HookSpecificOutput, &nested); err == nil {
			if out.AdditionalContext == "" && strings.TrimSpace(nested.AdditionalContext) != "" {
				out.AdditionalContext = nested.AdditionalContext
			}
			if strings.EqualFold(strings.TrimSpace(nested.PermissionDecision), "deny") {
				out.Decision = "block"
			}
		}
	}
	return out, nil
}

func matcherMatches(matcher, value string) bool {
	m := strings.TrimSpace(matcher)
	if m == "" || m == "*" {
		return true
	}
	re, err := regexp.Compile(m)
	if err != nil {
		return false
	}
	return re.MatchString(value)
}

func shellInvoke(command string) (name string, args []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "sh", []string{"-c", command}
}

type runResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
	Err      error
}

func runHookCommand(ctx context.Context, cwd, command string, timeoutSec int, stdinJSON []byte) runResult {
	if strings.TrimSpace(command) == "" {
		return runResult{Err: errors.New("empty hook command")}
	}
	if timeoutSec < 1 {
		timeoutSec = defaultHookTimeoutSec
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	name, args := shellInvoke(command)
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(stdinJSON)
	cmd.Env = sandbox.ScrubOSEnviron(nil)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	rr := runResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rr.ExitCode = ee.ExitCode()
			return rr
		}
		rr.Err = err
		return rr
	}
	return rr
}

// runMatchingHandlers executes handlers whose group matcher matches matchValue in parallel.
func runMatchingHandlers(ctx context.Context, cwd string, groups []MatcherGroup, matchValue string, stdinPayload map[string]any) (hookOutput, error) {
	type job struct {
		g MatcherGroup
		h Handler
	}
	var jobs []job
	for _, g := range groups {
		if !matcherMatches(g.Matcher, matchValue) {
			continue
		}
		for _, h := range g.Hooks {
			typ := strings.ToLower(strings.TrimSpace(h.Type))
			if typ != "" && typ != "command" {
				continue
			}
			if strings.TrimSpace(h.Command) == "" {
				continue
			}
			jobs = append(jobs, job{g: g, h: h})
		}
	}
	if len(jobs) == 0 {
		return hookOutput{}, nil
	}

	stdinBase, err := json.Marshal(stdinPayload)
	if err != nil {
		return hookOutput{}, err
	}

	var mu sync.Mutex
	var blockReasons []string
	var sysMsgs []string
	var contexts []string
	var continueFalse bool
	var failClosedErr error

	var wg sync.WaitGroup
	for _, j := range jobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := runHookCommand(ctx, cwd, j.h.Command, j.h.EffectiveTimeout(), stdinBase)
			if rr.Err != nil {
				if j.h.FailClosed {
					mu.Lock()
					if failClosedErr == nil {
						failClosedErr = fmt.Errorf("%s: %w", j.h.Command, rr.Err)
					}
					mu.Unlock()
				}
				return
			}
			parsed, perr := parseHookOutput(rr.Stdout, rr.Stderr, rr.ExitCode)
			if perr != nil {
				plain := strings.TrimSpace(string(rr.Stdout))
				if plain != "" && rr.ExitCode == 0 {
					mu.Lock()
					contexts = append(contexts, plain)
					mu.Unlock()
					return
				}
				if j.h.FailClosed {
					mu.Lock()
					if failClosedErr == nil {
						failClosedErr = fmt.Errorf("hook %s: invalid JSON on stdout: %w", j.h.Command, perr)
					}
					mu.Unlock()
				}
				return
			}
			if rr.ExitCode == exitCodeBlock && strings.TrimSpace(parsed.Reason) == "" {
				parsed.Decision = "block"
				parsed.Reason = strings.TrimSpace(string(rr.Stderr))
				if parsed.Reason == "" {
					parsed.Reason = "blocked by hook (exit code 2)"
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if strings.TrimSpace(parsed.SystemMessage) != "" {
				sysMsgs = append(sysMsgs, parsed.SystemMessage)
			}
			if strings.TrimSpace(parsed.AdditionalContext) != "" {
				contexts = append(contexts, parsed.AdditionalContext)
			}
			if strings.EqualFold(parsed.Decision, "block") {
				r := strings.TrimSpace(parsed.Reason)
				if r == "" {
					r = "blocked by hook"
				}
				blockReasons = append(blockReasons, r)
			}
			if parsed.Continue != nil && !*parsed.Continue {
				continueFalse = true
			}
		}()
	}
	wg.Wait()

	if failClosedErr != nil {
		return hookOutput{}, failClosedErr
	}

	var out hookOutput
	if len(blockReasons) > 0 {
		out.Decision = "block"
		out.Reason = strings.Join(blockReasons, "; ")
	}
	if len(contexts) > 0 {
		out.AdditionalContext = strings.Join(contexts, "\n")
	}
	if len(sysMsgs) > 0 {
		out.SystemMessage = strings.Join(sysMsgs, "\n")
	}
	if continueFalse {
		f := false
		out.Continue = &f
	}
	return out, nil
}

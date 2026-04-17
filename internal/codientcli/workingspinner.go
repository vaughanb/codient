package codientcli

import (
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// tuiModeActive is set to true when the Bubble Tea TUI is running.
// It disables raw-terminal features (spinners, cursor movement) whose escape
// sequences would pollute the TUI viewport. Lipgloss styling in assistout is
// handled separately via assistout.SetTUIOverride.
var tuiModeActive atomic.Bool

// stderrIsInteractive reports whether os.Stderr is a character device (TTY).
// In TUI mode it returns false to suppress raw ANSI cursor/spinner sequences
// that would appear as garbage in the viewport.
func stderrIsInteractive() bool {
	if tuiModeActive.Load() {
		return false
	}
	st, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return (st.Mode() & os.ModeCharDevice) != 0
}

// startWorkingSpinner draws an indeterminate spinner on w until stop is called.
// It is a no-op when w is not a TTY (avoids escape sequences in logs/pipes).
func startWorkingSpinner(w io.Writer) (stop func()) {
	if w == nil || !stderrIsInteractive() {
		return func() {}
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	var once sync.Once
	stop = func() {
		once.Do(func() {
			close(done)
			wg.Wait()
			_, _ = io.WriteString(w, "\r\x1b[K") // clear line after goroutine exits
		})
	}

	go func() {
		defer wg.Done()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		i := 0
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_, _ = io.WriteString(w, "\r\x1b[K"+frames[i%len(frames)]+" Agent is working…")
				i++
			}
		}
	}()

	return stop
}

// spinStopWriter forwards writes to w and calls stop on every non-empty write.
// stop must be safe for concurrent and repeated calls.
type spinStopWriter struct {
	w    io.Writer
	stop func()
}

func (s *spinStopWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		s.stop()
	}
	return s.w.Write(p)
}

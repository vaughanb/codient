package codientcli

import (
	"fmt"
	"io"
	"sync"
	"time"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// modelSpinner displays a spinner on stderr while a model is being activated.
// Call stop() to clear the spinner line and print the final message.
type modelSpinner struct {
	w       io.Writer
	prefix  string
	stopCh  chan struct{}
	stopped sync.Once
}

func startModelSpinner(w io.Writer, modelName string) *modelSpinner {
	if w == nil {
		return &modelSpinner{stopCh: make(chan struct{})}
	}
	s := &modelSpinner{
		w:      w,
		prefix: fmt.Sprintf("codient: loading model %s ", modelName),
		stopCh: make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *modelSpinner) run() {
	if s.w == nil {
		return
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	i := 0
	for {
		select {
		case <-s.stopCh:
			return
		case <-tick.C:
			frame := spinnerFrames[i%len(spinnerFrames)]
			fmt.Fprintf(s.w, "\r%s%s", s.prefix, frame)
			i++
		}
	}
}

func (s *modelSpinner) stop(result string) {
	s.stopped.Do(func() {
		close(s.stopCh)
		if s.w == nil {
			return
		}
		// Clear the spinner line and print the result.
		fmt.Fprintf(s.w, "\r\033[K")
		if result != "" {
			fmt.Fprintf(s.w, "%s\n", result)
		}
	})
}

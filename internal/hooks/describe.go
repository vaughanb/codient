package hooks

import (
	"sort"
	"strings"
)

// Descriptor is one configured hook command for listing (e.g. /hooks).
type Descriptor struct {
	Event      string
	Matcher    string
	Command    string
	TimeoutSec int
	SourcePath string
}

// ListDescriptors returns a stable-sorted list of all configured hook commands.
func (m *Manager) ListDescriptors() []Descriptor {
	if m == nil || m.loaded == nil {
		return nil
	}
	var events []string
	for ev := range m.loaded.ByEvent {
		events = append(events, ev)
	}
	sort.Strings(events)
	var out []Descriptor
	for _, ev := range events {
		for _, g := range m.loaded.ByEvent[ev] {
			for _, h := range g.Hooks {
				if strings.TrimSpace(h.Command) == "" {
					continue
				}
				out = append(out, Descriptor{
					Event:      ev,
					Matcher:    g.Matcher,
					Command:    h.Command,
					TimeoutSec: h.EffectiveTimeout(),
					SourcePath: g.SourcePath,
				})
			}
		}
	}
	return out
}

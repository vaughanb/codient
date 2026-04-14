package assistout

import "strings"

// Minimal 5×5 block letters + single space between glyphs (█ = U+2588).
var codientBlockASCII = []string{
	strings.Join([]string{"█████", "█████", "████ ", "  █  ", "█████", "█   █", "█████"}, " "),
	strings.Join([]string{"█    ", "█   █", "█   █", "  █  ", "█    ", "██  █", "  █  "}, " "),
	strings.Join([]string{"█    ", "█   █", "█   █", "  █  ", "█████", "█ █ █", "  █  "}, " "),
	strings.Join([]string{"█    ", "█   █", "█   █", "  █  ", "█    ", "█  ██", "  █  "}, " "),
	strings.Join([]string{"█████", "█████", "████ ", "  █  ", "█████", "█   █", "  █  "}, " "),
}

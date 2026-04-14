// Command codient is a CLI coding agent using an OpenAI-compatible chat API (openai-go client).
package main

import (
	"os"

	"codient/internal/codientcli"
)

func main() {
	os.Exit(codientcli.Run())
}

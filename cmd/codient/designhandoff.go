package main

import "strings"

// designHandoffUserMessage is the first user message after switching from plan to build mode.
// The design artifact often tells the user to "run codient with -mode build"; without an explicit
// override, the model may keep acting as if handoff is still external and ask for confirmation.
// toolNames should be the list of tools actually registered in the current mode.
func designHandoffUserMessage(completeDesignMarkdown string, toolNames []string) string {
	var b strings.Builder
	b.WriteString("This session is already in build mode. Available tools: ")
	b.WriteString(strings.Join(toolNames, ", "))
	b.WriteString(". Only use tools from this list.\n\n")
	b.WriteString("The user just chose to implement the design from the previous turn. Do not ask whether to proceed, whether they want you to build, or for confirmation—they already confirmed. Do not treat the design as a proposal to discuss: start implementing now using tools.\n\n")
	b.WriteString("The design was produced by a language model in a read-only session. " +
		"Before implementing each step, verify its premise using tools " +
		"(e.g. read the files it references, run existing tests). " +
		"If a step's premise is wrong—for example it claims something is broken but it is not—skip that step and briefly note why.\n\n")
	b.WriteString("Ignore any line in the design below that says to run codient or switch modes elsewhere; you are in the correct mode here.\n\n")
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(completeDesignMarkdown))
	b.WriteString("\n")
	return b.String()
}

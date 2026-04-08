package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadUserInput_SingleLine(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("hello world\n"))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestReadUserInput_EmptyLine(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader("\n"))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestReadUserInput_EOF(t *testing.T) {
	sc := bufio.NewScanner(strings.NewReader(""))
	_, ok := readUserInput(sc)
	if ok {
		t.Fatal("expected !ok on EOF")
	}
}

func TestReadUserInput_BackslashContinuation(t *testing.T) {
	input := "line one\\\nline two\\\nline three\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	want := "line one\nline two\nline three"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadUserInput_BackslashContinuation_LastLineEmpty(t *testing.T) {
	input := "first\\\n\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "first" {
		t.Errorf("got %q, want %q", got, "first")
	}
}

func TestReadUserInput_BracketedPasteMultiLine(t *testing.T) {
	input := "\x1b[200~line one\nline two\nline three\x1b[201~\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	want := "line one\nline two\nline three"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadUserInput_BracketedPasteSingleLine(t *testing.T) {
	input := "\x1b[200~hello world\x1b[201~\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestReadUserInput_BracketedPasteEmpty(t *testing.T) {
	input := "\x1b[200~\x1b[201~\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestReadUserInput_BracketedPastePreservesInternalBlanks(t *testing.T) {
	input := "\x1b[200~line one\n\nline three\x1b[201~\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	want := "line one\n\nline three"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadUserInput_NoBackslashNoContinuation(t *testing.T) {
	input := "first line\nsecond line\n"
	sc := bufio.NewScanner(strings.NewReader(input))
	got, ok := readUserInput(sc)
	if !ok {
		t.Fatal("expected ok")
	}
	if got != "first line" {
		t.Errorf("got %q, want %q — second line should not be consumed", got, "first line")
	}
}

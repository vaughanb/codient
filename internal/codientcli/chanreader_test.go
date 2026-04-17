package codientcli

import (
	"bufio"
	"io"
	"testing"
)

func TestChanReader_SingleLine(t *testing.T) {
	ch := make(chan string, 1)
	r := &chanReader{ch: ch}

	ch <- "hello"
	buf := make([]byte, 64)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "hello\n" {
		t.Fatalf("got %q, want %q", got, "hello\n")
	}
}

func TestChanReader_EOF(t *testing.T) {
	ch := make(chan string)
	r := &chanReader{ch: ch}
	close(ch)

	_, err := r.Read(make([]byte, 64))
	if err != io.EOF {
		t.Fatalf("got %v, want io.EOF", err)
	}
}

func TestChanReader_MultipleLines(t *testing.T) {
	ch := make(chan string, 3)
	ch <- "first"
	ch <- "second"
	ch <- "third"
	close(ch)

	sc := bufio.NewScanner(&chanReader{ch: ch})
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 3 || lines[0] != "first" || lines[1] != "second" || lines[2] != "third" {
		t.Fatalf("got %v", lines)
	}
}

func TestChanReader_SmallBuffer(t *testing.T) {
	ch := make(chan string, 1)
	r := &chanReader{ch: ch}

	ch <- "hello world"
	buf := make([]byte, 5)

	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	part1 := string(buf[:n])

	n, err = r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	part2 := string(buf[:n])

	got := part1 + part2
	if !containsAll(got, "hello") {
		t.Fatalf("reads should return buffered data, got %q + %q", part1, part2)
	}
}

func containsAll(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr
}

func TestChanReader_EmptyString(t *testing.T) {
	ch := make(chan string, 1)
	r := &chanReader{ch: ch}

	ch <- ""
	buf := make([]byte, 64)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "\n" {
		t.Fatalf("empty string should produce just newline, got %q", got)
	}
}

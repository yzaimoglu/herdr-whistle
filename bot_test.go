package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTelegramTextBoundsUnicodeChunks(t *testing.T) {
	text := strings.Repeat("界", 11)
	chunks := splitTelegramText(text, 4)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	if strings.Join(chunks, "") != text {
		t.Fatal("chunks did not reconstruct the original text")
	}
	for _, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > 4 {
			t.Errorf("chunk exceeds limit: %q", chunk)
		}
	}
}

func TestTruncateText(t *testing.T) {
	if got := truncateText("abcdef", 4); got != "abcd\n...[truncated]" {
		t.Errorf("truncateText = %q", got)
	}
	if got := truncateText("abc", 4); got != "abc" {
		t.Errorf("short text changed to %q", got)
	}
}

func TestSignalParentReady(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	t.Setenv("HERDR_WHISTLE_READY_FD", strconv.Itoa(int(writer.Fd())))

	signalParentReady()
	var value [1]byte
	if _, err := reader.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	if value[0] != 1 {
		t.Fatalf("readiness byte = %d, want 1", value[0])
	}
}

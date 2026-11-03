package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogSinkRotates(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "smifd.jsonl")
	l, err := openLogSink(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	l.Write([]byte(strings.Repeat("x", 100)))
	if rotated, _ := l.rotateIfLarger(1000, time.Now()); rotated {
		t.Fatal("rotated below the limit")
	}
	l.Write([]byte(strings.Repeat("y", 1000)))
	if rotated, err := l.rotateIfLarger(1000, time.Now()); !rotated || err != nil {
		t.Fatalf("not rotated: %v", err)
	}
	l.Write([]byte("after\n"))
	b, _ := os.ReadFile(p)
	if string(b) != "after\n" {
		t.Fatalf("live file after rotation = %q", b)
	}
	old, _ := filepath.Glob(filepath.Join(dir, "smifd.*.jsonl"))
	if len(old) != 1 {
		t.Fatalf("rotated files: %v", old)
	}
}

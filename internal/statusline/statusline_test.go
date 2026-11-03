package statusline

import (
	"errors"
	"testing"

	"github.com/appnice/mailroom/internal/api"
)

func TestSegment(t *testing.T) {
	cases := []struct {
		c    api.Counts
		want string
	}{
		{api.Counts{Agent: "cafe", ClaudeOK: true}, ""},
		{api.Counts{ClaudeOK: true}, ""},
		{api.Counts{Agent: "cafe", Running: 1, Handled: 2, NeedsHuman: 1, GlobalNeedsHuman: 3, ClaudeOK: true},
			"mail: cafe 1 running · 2 handled · 1 needs you · 2 need you elsewhere"},
		{api.Counts{Agent: "web", RepliesReady: 1, DirectLock: true, ClaudeOK: true}, "mail: web 1 reply ready · worker holds repo"},
		{api.Counts{GlobalDLQ: 2}, "mail: 2 dead · claude unavailable"},
	}
	for _, c := range cases {
		if got := Segment(&c.c, nil); got != c.want {
			t.Errorf("Segment(%+v) = %q, want %q", c.c, got, c.want)
		}
	}
	if got := Segment(&api.Counts{}, errors.New("down")); got != "mail: daemon down" {
		t.Fatal(got)
	}
}

func TestCombine(t *testing.T) {
	if got := Combine("my status\nline 2", ""); got != "my status\nline 2" {
		t.Fatalf("empty segment changed the output: %q", got)
	}
	if got := Combine("my status\nline 2", "mail: x"); got != "my status │ mail: x\nline 2" {
		t.Fatal(got)
	}
	if got := Combine("", "mail: x"); got != "mail: x" {
		t.Fatal(got)
	}
	if got := RunWrapped("echo hi", nil, 0); got != "" && got != "hi" {
		t.Fatal(got)
	}
}

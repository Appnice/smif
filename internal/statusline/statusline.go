// Package statusline renders Smif's status-line segment and wraps an
// existing status-line command (RFC 7.4, R-HUM-1). It carries counts only.
package statusline

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/appnice/smif/internal/api"
)

// Segment renders the counts, e.g.
// "mail: puzzle 1 running · 2 handled · 1 needs you".
func Segment(c *api.Counts, err error) string {
	if err != nil {
		return "mail: daemon down"
	}
	var parts []string
	if c.Running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", c.Running))
	}
	if c.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", c.Queued))
	}
	if c.Handled > 0 {
		parts = append(parts, fmt.Sprintf("%d handled", c.Handled))
	}
	if c.NeedsHuman > 0 {
		parts = append(parts, fmt.Sprintf("%d needs you", c.NeedsHuman))
	}
	if c.RepliesReady > 0 {
		r := "replies"
		if c.RepliesReady == 1 {
			r = "reply"
		}
		parts = append(parts, fmt.Sprintf("%d %s ready", c.RepliesReady, r))
	}
	if c.DirectLock {
		parts = append(parts, "worker holds repo")
	}
	elsewhere := c.GlobalNeedsHuman - c.NeedsHuman
	if elsewhere > 0 {
		parts = append(parts, fmt.Sprintf("%d need you elsewhere", elsewhere))
	}
	if c.GlobalDLQ > 0 {
		parts = append(parts, fmt.Sprintf("%d dead", c.GlobalDLQ))
	}
	if !c.ClaudeOK {
		parts = append(parts, "claude unavailable")
	}
	name := c.Agent
	sessions := sessionParts(c)
	if len(parts) == 0 {
		return sessions // "" when nothing needs you: stay out of the status line
	}
	mail := "mail: " + strings.Join(parts, " · ")
	if name != "" {
		mail = "mail: " + name + " " + strings.Join(parts, " · ")
	}
	if sessions == "" {
		return mail
	}
	return sessions + " · " + mail
}

// maxNames is how many session names the status line spells out.
const maxNames = 3

// sessionParts names the other sessions that need you: "! mifi, digi
// wait · ● rfc answered" (ADR-0014).
func sessionParts(c *api.Counts) string {
	names := func(ns []string) string {
		if len(ns) > maxNames {
			return strings.Join(ns[:maxNames], ", ") + fmt.Sprintf(" +%d", len(ns)-maxNames)
		}
		return strings.Join(ns, ", ")
	}
	var out []string
	switch n := len(c.SessionsWaiting); {
	case n == 1:
		out = append(out, "! "+names(c.SessionsWaiting)+" waits")
	case n > 1:
		out = append(out, "! "+names(c.SessionsWaiting)+" wait")
	}
	if len(c.SessionsAnswered) > 0 {
		out = append(out, "● "+names(c.SessionsAnswered)+" answered")
	}
	return strings.Join(out, " · ")
}

// RunWrapped runs the user's original status-line command with the same
// stdin and returns its output unchanged (trailing newline trimmed).
func RunWrapped(command string, stdin []byte, timeout time.Duration) string {
	if strings.TrimSpace(command) == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	out, _ := cmd.Output()
	return strings.TrimRight(string(out), "\n")
}

// Combine appends the segment to the first line of the wrapped output.
func Combine(wrapped, segment string) string {
	if segment == "" {
		return wrapped
	}
	if wrapped == "" {
		return segment
	}
	lines := strings.SplitN(wrapped, "\n", 2)
	lines[0] = lines[0] + " │ " + segment
	return strings.Join(lines, "\n")
}

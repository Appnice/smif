// Package notify raises OS notifications for needs_human, dead letters and
// budget exhaustion (RFC 7.4). Title = agent, body = summary (max 280
// chars), never the raw inbound body.
package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Event is one notification.
type Event struct {
	Kind      string    `json:"kind"` // needs_human | dead_letter | budget | claude_unavailable | session_waiting | session_answered
	Agent     string    `json:"agent"`
	Summary   string    `json:"summary"`
	MessageID string    `json:"message_id,omitempty"`
	At        time.Time `json:"at"`
}

// Notifier delivers events.
type Notifier interface {
	Notify(Event) error
}

// New builds a notifier from the config value: "os", "none" or "log:<path>".
func New(spec string) Notifier {
	switch {
	case spec == "none":
		return none{}
	case strings.HasPrefix(spec, "log:"):
		return &fileLog{path: strings.TrimPrefix(spec, "log:")}
	default:
		return osNotifier{}
	}
}

// Clip trims s to n runes on a rune boundary.
func Clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

type none struct{}

func (none) Notify(Event) error { return nil }

type fileLog struct {
	path string
	mu   sync.Mutex
}

func (f *fileLog) Notify(e Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e.Summary = Clip(e.Summary, 280)
	fh, err := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer fh.Close()
	b, _ := json.Marshal(e)
	_, err = fh.Write(append(b, '\n'))
	return err
}

type osNotifier struct{}

func title(e Event) string {
	switch e.Kind {
	case "needs_human":
		return "Mailroom: " + e.Agent + " needs you"
	case "dead_letter":
		return "Mailroom: " + e.Agent + " dead letter"
	case "budget":
		return "Mailroom: " + e.Agent + " budget exhausted"
	case "session_waiting":
		return "Mailroom: " + e.Agent + " waits for you"
	case "session_answered":
		return "Mailroom: " + e.Agent + " answered"
	case "session_reminder":
		return "Mailroom: " + e.Agent + " still waits for you"
	case "session_silent":
		return "Mailroom: " + e.Agent + " is silent"
	case "session_long":
		return "Mailroom: " + e.Agent + " works long"
	default:
		return "Mailroom: " + e.Agent
	}
}

func (osNotifier) Notify(e Event) error {
	body := Clip(e.Summary, 280)
	t := title(e)
	if runtime.GOOS == "darwin" {
		if p, err := exec.LookPath("terminal-notifier"); err == nil {
			return exec.Command(p, "-title", t, "-message", body, "-group", "mailroom-"+e.Agent).Run()
		}
		script := fmt.Sprintf("display notification %s with title %s", appleQuote(body), appleQuote(t))
		return exec.Command("osascript", "-e", script).Run()
	}
	if p, err := exec.LookPath("notify-send"); err == nil {
		return exec.Command(p, "--app-name=mailroom", t, body).Run()
	}
	return fmt.Errorf("no notifier found (install notify-send)")
}

func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

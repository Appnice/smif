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
// dataDir holds the macOS notifier app (app.go).
func New(spec, dataDir string) Notifier {
	switch {
	case spec == "none":
		return none{}
	case strings.HasPrefix(spec, "log:"):
		return &fileLog{path: strings.TrimPrefix(spec, "log:")}
	default:
		return osNotifier{app: appFor(dataDir)}
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

type osNotifier struct{ app *macApp }

var (
	appsMu sync.Mutex
	apps   = map[string]*macApp{}
)

// appFor shares one app per data dir, so a config reload does not build it
// again.
func appFor(dataDir string) *macApp {
	if dataDir == "" {
		return nil
	}
	appsMu.Lock()
	defer appsMu.Unlock()
	if apps[dataDir] == nil {
		apps[dataDir] = &macApp{dir: dataDir}
	}
	return apps[dataDir]
}

func title(e Event) string {
	switch e.Kind {
	case "needs_human":
		return "Smif: " + e.Agent + " needs you"
	case "dead_letter":
		return "Smif: " + e.Agent + " dead letter"
	case "budget":
		return "Smif: " + e.Agent + " budget exhausted"
	case "session_waiting":
		return "Smif: " + e.Agent + " waits for you"
	case "session_answered":
		return "Smif: " + e.Agent + " answered"
	case "session_reminder":
		return "Smif: " + e.Agent + " still waits for you"
	case "session_silent":
		return "Smif: " + e.Agent + " is silent"
	case "session_long":
		return "Smif: " + e.Agent + " works long"
	default:
		return "Smif: " + e.Agent
	}
}

func (n osNotifier) Notify(e Event) error {
	body := Clip(e.Summary, 280)
	t := title(e)
	if runtime.GOOS == "darwin" {
		// Smif.app shows smif's icon; the name is on the icon, so the title
		// drops "Smif: ". Any failure falls back to the tools below.
		if n.app != nil && n.app.notify(strings.TrimPrefix(t, "Smif: "), body) == nil {
			return nil
		}
		if p, err := exec.LookPath("terminal-notifier"); err == nil {
			return exec.Command(p, "-title", t, "-message", body, "-group", "smif-"+e.Agent).Run()
		}
		script := fmt.Sprintf("display notification %s with title %s", appleQuote(body), appleQuote(t))
		return exec.Command("osascript", "-e", script).Run()
	}
	if p, err := exec.LookPath("notify-send"); err == nil {
		return exec.Command(p, "--app-name=smif", t, body).Run()
	}
	return fmt.Errorf("no notifier found (install notify-send)")
}

func appleQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

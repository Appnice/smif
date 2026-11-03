// Package presence turns Claude Code hook and status-line payloads into
// presence updates (RFC 6.4, 7.4). Hooks print nothing and always exit 0.
package presence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Payload is the subset of a hook or status-line stdin JSON we use.
type Payload struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Event     string `json:"hook_event_name"`
	// TranscriptPath is the session's JSONL transcript; Prompt is the
	// submitted text (UserPromptSubmit only).
	TranscriptPath string `json:"transcript_path"`
	Prompt         string `json:"prompt"`
	Workspace      struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
}

// Parse reads a payload; missing fields fall back to the process cwd.
func Parse(b []byte) Payload {
	var p Payload
	_ = json.Unmarshal(b, &p)
	if p.Cwd == "" {
		p.Cwd = p.Workspace.CurrentDir
	}
	if p.Cwd == "" {
		p.Cwd = p.Workspace.ProjectDir
	}
	if p.Cwd == "" {
		p.Cwd, _ = os.Getwd()
	}
	return p
}

// EventFor maps a hook name to a presence event.
func EventFor(hook string) string {
	switch strings.ToLower(strings.ReplaceAll(hook, "-", "")) {
	case "sessionstart":
		return "start"
	case "sessionend":
		return "end"
	case "userpromptsubmit":
		return "busy"
	case "stop":
		return "idle"
	default: // the status line
		return "heartbeat"
	}
}

// Locate finds the claude process this hook runs under (walking up from
// the parent) and its terminal device, so the human can later type a
// prompt into that session (ADR-0012). It returns 0, "" when there is
// none (no claude ancestor, or no terminal).
func Locate() (pid int, tty string) {
	cur := os.Getppid()
	for i := 0; i < 6 && cur > 1; i++ {
		out, err := exec.Command("ps", "-o", "ppid=,tty=,comm=", "-p", strconv.Itoa(cur)).Output()
		if err != nil {
			return 0, ""
		}
		f := strings.Fields(string(out))
		if len(f) < 3 {
			return 0, ""
		}
		if strings.Contains(filepath.Base(strings.Join(f[2:], " ")), "claude") {
			if f[1] == "??" || f[1] == "?" {
				return 0, ""
			}
			return cur, "/dev/" + f[1]
		}
		next, err := strconv.Atoi(f[0])
		if err != nil {
			return 0, ""
		}
		cur = next
	}
	return 0, ""
}

// Term names the terminal from the hook's environment: "tmux" inside
// tmux, else $TERM_PROGRAM (Apple_Terminal, iTerm.app, …).
func Term() string {
	if os.Getenv("TMUX") != "" {
		return "tmux"
	}
	return os.Getenv("TERM_PROGRAM")
}

// InWorker reports whether this process runs inside a Smif worker, where
// user hooks also fire but must not count as an interactive session.
func InWorker() bool { return os.Getenv("SMIF_RUN_ID") != "" }

// transcriptTail is how much of the transcript end Transcript reads.
// Claude Code appends the title and last-prompt records again on each
// turn, so the end of the file holds the newest ones.
const transcriptTail = 1 << 20

// Transcript reads the session's name (claude --name, /rename) and the
// human's last prompt from the end of its transcript. Missing or
// unreadable files give "", "".
func Transcript(path string) (title, lastPrompt string) {
	if path == "" || filepath.Ext(path) != ".jsonl" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > transcriptTail {
		if _, err := f.Seek(st.Size()-transcriptTail, io.SeekStart); err != nil {
			return "", ""
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"custom-title"`)) && !bytes.Contains(line, []byte(`"last-prompt"`)) {
			continue
		}
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			LastPrompt  string `json:"lastPrompt"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch {
		case rec.Type == "custom-title" && rec.CustomTitle != "":
			title = rec.CustomTitle
		case rec.Type == "last-prompt" && rec.LastPrompt != "":
			lastPrompt = rec.LastPrompt
		}
	}
	return title, lastPrompt
}

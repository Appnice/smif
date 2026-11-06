package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appnice/smif/internal/envelope"
)

// A rule can hand mail to a read-only `codex exec`. The stand-in records its
// argv and stdin: the message goes in on stdin and never reaches argv
// (R-ISO-3), and the reply comes back through the thread (ADR-0022).
func TestCodexWorkerAnswersAsk(t *testing.T) {
	bin := t.TempDir()
	fake := filepath.Join(bin, "fakecodex")
	script := `#!/bin/sh
printf '%s\n' "$@" > "` + bin + `/argv"
cat > "` + bin + `/stdin"
echo '{"type":"thread.started","thread_id":"t1"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"I will look."}}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"{\"status\":\"done\",\"summary\":\"one bug\",\"reply\":\"line 3 divides by zero\",\"human_question\":\"\"}"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":7}}'
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rules := "[[agents.AGENT.rules]]\nmatch = { intent = \"review.*\" }\nhandler = \"worker\"\nprofile = \"review\"\n" + digestRule()
	profiles := fmt.Sprintf("[profiles.review]\nharness = \"codex\"\ntimeout_s = 30\n\n[harnesses.codex]\ncommand = %q\n", fake)
	e := NewEnv(t, "", profiles, AgentSpec{ID: "asker", Rules: digestRule()}, AgentSpec{ID: "rev", Rules: rules})
	e.Start()

	r := e.Send("asker", "message.ask", envelope.Draft{To: "rev", Kind: "ask", Intent: "review.diff", Subject: "SUBJECT-MARKER", Body: "BODY-MARKER"})
	e.WaitState(r.ID, 15*time.Second, "done")
	e.WaitFor("the reply", 15*time.Second, func() bool {
		for _, m := range e.Thread(r.ThreadID).Messages {
			if m.Body == "line 3 divides by zero" {
				return true
			}
		}
		return false
	})

	argv, _ := os.ReadFile(filepath.Join(bin, "argv"))
	for _, want := range []string{"exec", "read-only", "--ephemeral", "--ignore-user-config", "--output-schema"} {
		if !strings.Contains(string(argv), want) {
			t.Errorf("argv lacks %s:\n%s", want, argv)
		}
	}
	if strings.Contains(string(argv), "MARKER") {
		t.Fatalf("the message reached argv:\n%s", argv)
	}
	stdin, _ := os.ReadFile(filepath.Join(bin, "stdin"))
	if !strings.Contains(string(stdin), "BODY-MARKER") || !strings.Contains(string(stdin), `untrusted="true"`) {
		t.Fatalf("stdin:\n%s", stdin)
	}
}

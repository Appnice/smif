// Package envelope defines the immutable message envelope (RFC 5.1) and the
// limits checked at ingress.
package envelope

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/appnice/mailroom/internal/config"
)

// Limits from RFC 5.1.
const (
	MaxBody            = 32 << 10
	MaxData            = 64 << 10
	MaxAttachment      = 25 << 20
	MaxAttachmentTotal = 100 << 20
	MaxSubject         = 512
	MaxSummary         = 280
)

// Origins of a message (RFC 4.5).
const (
	OriginInteractive = "interactive"
	OriginWorker      = "worker"
	OriginCLI         = "cli"
	OriginHuman       = "human"
	OriginDaemon      = "daemon"
)

// From is stamped by the daemon from the authenticated connection.
type From struct {
	Agent  string `json:"agent"`
	Origin string `json:"origin"`
	RunID  string `json:"run_id,omitempty"`
}

// Attachment is a content-addressed blob reference.
type Attachment struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime"`
}

// Envelope is the message as stored and handed to handlers.
type Envelope struct {
	ID          string          `json:"id"`
	ThreadID    string          `json:"thread_id"`
	ParentID    *string         `json:"parent_id"`
	From        From            `json:"from"`
	To          string          `json:"to"`
	Kind        string          `json:"kind"`
	Intent      string          `json:"intent"`
	Priority    string          `json:"priority"`
	Subject     string          `json:"subject"`
	Body        string          `json:"body"`
	Data        json.RawMessage `json:"data,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	DedupeKey   string          `json:"dedupe_key,omitempty"`
	Hop         int             `json:"hop"`
	Deadline    *time.Time      `json:"deadline,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	// Status is set on replies: "ok", "timeout", "rejected", "failed".
	Status string `json:"status,omitempty"`
}

// Draft is what a client submits. It has no id, thread, sender or hop:
// those are minted or stamped by the daemon.
type Draft struct {
	To          string          `json:"to"`
	Kind        string          `json:"kind"`
	Intent      string          `json:"intent"`
	Priority    string          `json:"priority,omitempty"`
	Subject     string          `json:"subject"`
	Body        string          `json:"body,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	DedupeKey   string          `json:"dedupe_key,omitempty"`
	Deadline    *time.Time      `json:"deadline,omitempty"`
	ThreadID    string          `json:"thread_id,omitempty"`
}

var (
	entropyMu sync.Mutex
	entropy   = ulid.Monotonic(rand.Reader, 0)
)

// NewID mints a ULID.
func NewID() string {
	entropyMu.Lock()
	defer entropyMu.Unlock()
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// Validate checks a draft against RFC 5.1 limits.
func (d *Draft) Validate() error {
	if d.To == "" {
		return fmt.Errorf("to is required")
	}
	switch d.Kind {
	case "tell", "ask", "event":
	case "reply":
		return fmt.Errorf("kind reply is created by the daemon from a handler result or mailroom reply")
	default:
		return fmt.Errorf("kind %q: must be tell, ask or event", d.Kind)
	}
	if !config.ValidIntent(d.Intent) {
		return fmt.Errorf("intent %q: must match ^[a-z0-9]+(\\.[a-z0-9_-]+)*$", d.Intent)
	}
	if d.Priority == "" {
		d.Priority = "normal"
	}
	if config.PriorityLevel(d.Priority) < 0 {
		return fmt.Errorf("priority %q: must be low, normal, high or urgent", d.Priority)
	}
	if len(d.Subject) > MaxSubject {
		return fmt.Errorf("subject longer than %d bytes", MaxSubject)
	}
	if len(d.Body) > MaxBody {
		return fmt.Errorf("body is %d bytes; max %d (put larger content in attachments)", len(d.Body), MaxBody)
	}
	if len(d.Data) > MaxData {
		return fmt.Errorf("data is %d bytes; max %d", len(d.Data), MaxData)
	}
	if len(d.Data) > 0 {
		var obj map[string]any
		if err := json.Unmarshal(d.Data, &obj); err != nil {
			return fmt.Errorf("data must be a JSON object")
		}
	}
	if d.DedupeKey != "" && d.Kind != "event" {
		return fmt.Errorf("dedupe_key is only valid for events")
	}
	var total int64
	for _, a := range d.Attachments {
		if a.Size > MaxAttachment {
			return fmt.Errorf("attachment %q is larger than 25 MiB", a.Name)
		}
		total += a.Size
		if err := ValidName(a.Name); err != nil {
			return err
		}
		if len(a.SHA256) != 64 {
			return fmt.Errorf("attachment %q: sha256 must be 64 hex chars", a.Name)
		}
	}
	if total > MaxAttachmentTotal {
		return fmt.Errorf("attachments total more than 100 MiB")
	}
	return nil
}

// ValidName rejects attachment names that could escape a directory.
func ValidName(n string) error {
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\\x00") || strings.HasPrefix(n, ".") {
		return fmt.Errorf("attachment name %q is not a plain file name", n)
	}
	if len(n) > 255 {
		return fmt.Errorf("attachment name too long")
	}
	return nil
}

// SanitizeName turns any string into a safe file name.
func SanitizeName(n string) string {
	n = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 || r < 32 {
			return '_'
		}
		return r
	}, n)
	n = strings.TrimLeft(n, ".")
	if n == "" {
		n = "file"
	}
	if len(n) > 200 {
		n = n[:200]
	}
	return n
}

// Short returns the last 8 chars of a ULID, used in branch names.
func Short(id string) string {
	if len(id) <= 10 {
		return strings.ToLower(id)
	}
	return strings.ToLower(id[len(id)-10:])
}

// Package audit mirrors committed audit rows into the append-only daily
// JSONL log (RFC 8.1, 8.3). The SQLite audit table is written in the same
// transaction as each state change; this package exports those rows to
// audit/YYYY-MM-DD.jsonl after commit, fsyncing every batch.
package audit

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/appnice/mailroom/internal/store"
)

// Log writes audit rows to daily files.
type Log struct {
	dir    string
	mu     sync.Mutex
	redact func(string) string
}

// New returns a log rooted at dir (created 0700).
func New(dir string, redact func(string) string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Log{dir: dir, redact: redact}, nil
}

// Dir returns the log directory.
func (l *Log) Dir() string { return l.dir }

// Write appends rows (called from store.OnAudit).
func (l *Log) Write(rows []store.AuditRow) {
	l.mu.Lock()
	defer l.mu.Unlock()
	byDay := map[string][]store.AuditRow{}
	for _, r := range rows {
		d := r.TS.Local().Format("2006-01-02")
		byDay[d] = append(byDay[d], r)
	}
	for day, rs := range byDay {
		f, err := os.OpenFile(filepath.Join(l.dir, day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			continue
		}
		for _, r := range rs {
			b, _ := json.Marshal(r)
			s := string(b)
			if l.redact != nil {
				s = l.redact(s)
			}
			f.WriteString(s + "\n")
		}
		f.Sync()
		f.Close()
	}
}

// Rotate gzips daily files older than today and deletes files older than
// retention. It returns how many files were compressed.
func (l *Log) Rotate(now time.Time, retention time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	ents, _ := os.ReadDir(l.dir)
	today := now.Local().Format("2006-01-02")
	n := 0
	for _, e := range ents {
		name := e.Name()
		day := strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".jsonl")
		t, err := time.ParseInLocation("2006-01-02", day, time.Local)
		if err != nil {
			continue
		}
		p := filepath.Join(l.dir, name)
		if now.Sub(t) > retention {
			os.Remove(p)
			continue
		}
		if strings.HasSuffix(name, ".jsonl") && day < today {
			if gzipFile(p) == nil {
				n++
			}
		}
	}
	return n
}

func gzipFile(p string) error {
	in, err := os.Open(p)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(p+".gz", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		out.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	out.Close()
	return os.Remove(p)
}

// Files lists current (uncompressed) day files, oldest first.
func (l *Log) Files() []string {
	ents, _ := os.ReadDir(l.dir)
	var out []string
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(l.dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

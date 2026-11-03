package daemon

import (
	"os"
	"strings"
	"sync"
	"time"
)

// logSink is the daemon log file; GC rotates it so it cannot grow without
// bound (rotated files are deleted after daemon.log_retention).
type logSink struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func openLogSink(path string) (*logSink, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &logSink{path: path, f: f}, nil
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Write(p)
}

func (l *logSink) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// rotateIfLarger renames the live file to smifd.<time>.jsonl and reopens
// it when it is larger than max bytes.
func (l *logSink) rotateIfLarger(max int64, now time.Time) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fi, err := l.f.Stat()
	if err != nil || fi.Size() <= max {
		return false, err
	}
	dst := strings.TrimSuffix(l.path, ".jsonl") + "." + now.Format("20060102-150405") + ".jsonl"
	if err := os.Rename(l.path, dst); err != nil {
		return false, err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	l.f.Close()
	l.f = f
	return true, nil
}

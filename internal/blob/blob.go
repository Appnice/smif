// Package blob is content-addressed attachment storage under
// blobs/sha256/ab/cd<rest> (RFC 8.1).
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/appnice/mailroom/internal/envelope"
)

// Store is the blob directory.
type Store struct{ dir string }

// New returns a store rooted at dir/blobs.
func New(dataDir string) *Store { return &Store{dir: filepath.Join(dataDir, "blobs", "sha256")} }

// Path returns where a blob lives.
func (s *Store) Path(sha string) string {
	if len(sha) < 5 {
		return filepath.Join(s.dir, "invalid")
	}
	return filepath.Join(s.dir, sha[:2], sha[2:4], sha[4:])
}

// Put writes data and returns its sha256 and a sniffed MIME type.
func (s *Store) Put(data []byte, mime string) (sha string, outMime string, err error) {
	sum := sha256.Sum256(data)
	sha = hex.EncodeToString(sum[:])
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	p := s.Path(sha)
	if _, err := os.Stat(p); err == nil {
		return sha, mime, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", "", err
	}
	if f, err := os.Open(tmp); err == nil {
		f.Sync()
		f.Close()
	}
	return sha, mime, os.Rename(tmp, p)
}

// PutFile stores a file from disk.
func (s *Store) PutFile(path string) (sha string, size int64, mime string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, "", err
	}
	if !fi.Mode().IsRegular() {
		return "", 0, "", fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > envelope.MaxAttachment {
		return "", 0, "", fmt.Errorf("%s is larger than 25 MiB", path)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", 0, "", err
	}
	sha, mime, err = s.Put(data, "")
	return sha, int64(len(data)), mime, err
}

// Get reads a blob.
func (s *Store) Get(sha string) ([]byte, error) { return os.ReadFile(s.Path(sha)) }

// Remove deletes a blob file.
func (s *Store) Remove(sha string) error { return os.Remove(s.Path(sha)) }

// CopyReadOnly materialises attachments into dir as read-only files with
// sanitised names and returns their paths keyed by name.
func (s *Store) CopyReadOnly(atts []envelope.Attachment, dir string) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range atts {
		name := envelope.SanitizeName(a.Name)
		dst := filepath.Join(dir, name)
		if _, exists := out[name]; exists {
			dst = filepath.Join(dir, a.SHA256[:8]+"-"+name)
		}
		data, err := s.Get(a.SHA256)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", a.Name, err)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != a.SHA256 {
			return nil, fmt.Errorf("attachment %s: checksum mismatch", a.Name)
		}
		if err := os.WriteFile(dst, data, 0o400); err != nil {
			return nil, err
		}
		out[a.Name] = dst
	}
	return out, nil
}

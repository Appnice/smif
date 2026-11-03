package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appnice/smif/internal/api"
	"github.com/appnice/smif/internal/store"
)

// GC applies the retention rules of RFC 8.1.
func (d *Daemon) GC() (*api.GCResult, error) {
	cfg := d.Config()
	now := time.Now()
	res := &api.GCResult{}
	msgCut := now.Add(-cfg.Daemon.MessageRetention.Duration).UnixMilli()
	err := d.store.Update(func(tx *store.Tx) error {
		r, err := tx.Exec(`DELETE FROM runs WHERE ended_at IS NOT NULL AND ended_at<? AND message_id IN
(SELECT id FROM messages WHERE state IN ('done','rejected','expired') AND updated_at<?)`, msgCut, msgCut)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		res.Runs = int(n)
		if _, err := tx.Exec(`DELETE FROM message_blobs WHERE message_id IN (SELECT id FROM messages WHERE state IN ('done','rejected','expired') AND updated_at<?)`, msgCut); err != nil {
			return err
		}
		r, err = tx.Exec(`DELETE FROM messages WHERE state IN ('done','rejected','expired') AND updated_at<? AND id NOT IN (SELECT message_id FROM runs)`, msgCut)
		if err != nil {
			return err
		}
		n, _ = r.RowsAffected()
		res.Messages = int(n)
		if _, err := tx.Exec(`DELETE FROM audit WHERE ts<?`, now.Add(-cfg.Daemon.AuditRetention.Duration).UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM presence WHERE last_seen<?`, now.Add(-24*time.Hour).UnixMilli()); err != nil {
			return err
		}
		return tx.Audit("admin.gc", "", "", "", "", nil)
	})
	if err != nil {
		return nil, err
	}
	// Transcripts older than retention.
	tcut := now.Add(-cfg.Daemon.TranscriptRetain.Duration)
	_ = filepath.Walk(filepath.Join(d.dataDir, "transcripts"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() && fi.ModTime().Before(tcut) {
			if os.Remove(p) == nil {
				res.Transcripts++
			}
		}
		return nil
	})
	// Unreferenced blobs past their grace period.
	shas, _ := d.store.ExpiredBlobs(now.Add(-cfg.Daemon.BlobGrace.Duration))
	for _, sha := range shas {
		if d.store.Update(func(tx *store.Tx) error { return tx.DeleteBlob(sha) }) == nil {
			if d.blobs.Remove(sha) == nil {
				res.Blobs++
			}
		}
	}
	// Old worker branches.
	for _, id := range cfg.AgentIDs() {
		res.Branches += d.ws.GC(cfg.Agent(id), cfg.Daemon.BranchRetention.Duration, now)
	}
	res.AuditFiles = d.audit.Rotate(now, cfg.Daemon.AuditRetention.Duration)
	// Daemon logs: rotate the live file once it passes 10 MiB, delete old ones.
	if rotated, err := d.logFile.rotateIfLarger(10<<20, now); err != nil {
		d.log.Warn("log rotation failed", "error", err.Error())
	} else if rotated {
		d.log.Info("log rotated")
	}
	logDir := filepath.Join(d.dataDir, "logs")
	ents, _ := os.ReadDir(logDir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "smifd.") && e.Name() != "smifd.jsonl" {
			if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > cfg.Daemon.LogRetention.Duration {
				if os.Remove(filepath.Join(logDir, e.Name())) == nil {
					res.Logs++
				}
			}
		}
	}
	d.log.Info("gc finished", "messages", res.Messages, "runs", res.Runs, "transcripts", res.Transcripts, "blobs", res.Blobs,
		"branches", res.Branches, "audit_files", res.AuditFiles)
	return res, nil
}

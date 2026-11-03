# ADR-0004: Audit rows live in SQLite and are mirrored to the JSONL log

**Status:** accepted, 2026-11-02

**What.** Every state transition writes an `audit` row in the same SQLite transaction (RFC 5.2). After commit
the rows are appended (fsync) to `audit/YYYY-MM-DD.jsonl`, which `mailroom tail` follows and which is
gzipped after rotation.

**Why.** A transaction cannot span SQLite and a plain file. Writing the row in SQLite makes the audit atomic
with the transition; the file is an export.

**Trade-off.** A crash between commit and the file append can leave the newest rows only in SQLite. The
table is the source of truth; the file is for humans and tools.

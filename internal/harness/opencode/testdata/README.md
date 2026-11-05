These database rows are built by hand from the research report (OpenCode source at commit 1e4ad075, session/sql.ts,
session-message.ts). None comes from a real OpenCode session, so a green test proves only smif's reader, not the
real schema. The report marks the keys inside `data` as unverified, and the pairing of `time_busy`, `time_idle` and
`time_suspended` as its own inference. Replace them with one captured database once someone has access.

`schema.sql` holds the tables, reduced to the columns the report names. Each `report-*.sql` fills them for the session
`ses_0a1b2c3d4e5f`. `CWD` stands for the session's folder and `NOW` for a time in ms.

- `report-idle.sql`: a finished run: a tool, then the final text. `time_idle` is the newest.
- `report-working.sql`: a run with its claim and no open tool: the model writes.
- `report-pending.sql`: a run with its claim and a `running` tool: a tool runs, or an approval waits.
- `report-no-claim.sql`: `time_busy` is the newest, with no claim.
- `report-v1.sql`: an OpenCode V1 database: no `session_v2` table.

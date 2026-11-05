These event logs are built by hand from the research report: Copilot's `session-events.schema.json` at 1.0.60 and
records that the Entire project captured at about 0.0.420. None comes from a session on this Mac, so a green test proves
only smif's parser, not the real file format. The report did not read `phase`, the shape of `permissionRequest` or the
lock file's body. Replace them with one captured session once someone has access.

`CWD` stands for the session's folder.

- `report-idle.jsonl`: a tool call in one turn, then a turn with the final text.
- `report-between-turns.jsonl`: a turn that asked for tools ended, and the next one did not start: still working.
- `report-working.jsonl`: `assistant.turn_start` is the end of the file: working.
- `report-tool.jsonl`: `tool.execution_start` with no end: a tool runs, or a question waits.
- `report-permission.jsonl`: `permission.requested` with no `permission.completed`: waiting for the human.
- `report-permission-first.jsonl`: the same, before the tool's start record.
- `report-permission-done.jsonl`: the permission got its answer, the tool ended, the turn ended.
- `report-aborted.jsonl`: `abort` while a tool ran.
- `report-resumed.jsonl`: a crash left a tool open, then `session.resume`.
- `report-shutdown.jsonl`: `session.shutdown` at the end of the file.
- `report-new.jsonl`: only `session.start`: no prompt yet.

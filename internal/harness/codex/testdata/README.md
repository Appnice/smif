These rollouts are built by hand from the research report (Codex source at commit c9035417, record shapes
from protocol.rs, items.rs and models.rs). None comes from a real Codex session, so a green test proves only
smif's parser, not the real file format. Replace them with one captured session once someone has access.

- `report-idle-paginated.jsonl`: a finished turn in the paginated mode (`item_completed`, no `user_message`).
- `report-idle-legacy.jsonl`: a finished turn in the legacy mode (`user_message`, `agent_message`).
- `report-working.jsonl`: an open turn after a tool result: working.
- `report-pending.jsonl`: an open turn with a tool call and no result: a tool runs, or an approval waits.
- `report-pending-never.jsonl`: the same with approval policy `never`: no approval can wait.
- `report-aborted.jsonl`: a turn the human stopped.
- `report-subagent.jsonl`: a subagent's rollout, which no process owns.

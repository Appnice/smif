These transcripts are built by hand from the research report (Qwen Code source at commit cf6b4270,
chatRecordingService.ts). None comes from a real Qwen Code session, so a green test proves only smif's parser,
not the real file format. Field names in `functionCall` and `toolCallResult` follow genai types, which the
report marks as to be verified. Replace them with one captured session once someone has access.

- `report-idle.jsonl`: a finished turn in the TUI: an answer with text and no tool call.
- `report-working.jsonl`: a tool result is the end of the file: working.
- `report-pending.jsonl`: a tool call with no result: a tool runs, or an approval waits.
- `report-acp-ended.jsonl`: `turn_result` after a tool call that got no result: the ACP path's decisive end.
- `report-rewind.jsonl`: a rewind left a dead branch in the file, and a subagent's record sits in it too.

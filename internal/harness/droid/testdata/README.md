These transcripts are built by hand from the research report: Droid's zod schema in the 1.3.0 bundle, and comments in
the Entire project's adapter. The report rebuilt its own example, nobody captured a real line, and `parentId` on a
message line is its inference. So a green test proves only smif's parser, not the real file format. Replace them with
one captured session once someone has access.

`CWD` stands for the session's folder.

- `report-idle.jsonl`: a tool call and its result, then a text answer.
- `report-working.jsonl`: a tool result is the end of the file: working.
- `report-prompt.jsonl`: the human's prompt is the end of the file: working.
- `report-pending.jsonl`: a tool call with no result: a tool runs, or an approval waits.
- `report-hidden.jsonl`: a finished answer, then messages the human does not see, which change nothing.
- `report-subagent.jsonl`: `callingSessionId` in the first line: a subagent's session.

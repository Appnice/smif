# ADR-0020: Prompt parts: long pastes, images and files as @path

Status: accepted, 2026-11-05

## Context

The prompt box took a paste as text and pressed it into one line, so a log lost its lines. It could not attach an
image. A human could not see whether a paste arrived whole. Claude Code's own box shows only a short mark for a paste.

smif types one line into a session, on four kinds of terminal. So a part must reach Claude Code through that line.

## Decision

A part is a file, and the line names it with `@path`.

Live test on 2026-11-05 (Claude Code 2.1.318, haiku, default permission mode, through a pty in herdr):

| What the line holds | What happened |
|---|---|
| A plain path to a PNG | It stays text. The model used Read |
| A plain path to a `.txt` | It stays text. The model chose Bash, which stopped on a permission prompt |
| `@file` with 501 and 3000 lines | Read in whole, no tool call, no permission prompt |
| `@file` with a line of 5000 characters | Read in whole |
| `@"path with space.png"` | The image reached the model |
| `@file` of 452 KB | Left out: `not read: too large` |
| `@file` at the very end of the line | The first enter only picks a row in the file list and drops the quotes |

From this:

- Parts live in `PartsDir` (`~/Library/Caches/smif/parts` on macOS), because a path with no spaces stays whole. Files are
  `0600` and go away after 24 hours: a paste can hold a secret.
- A word always follows the last part (`(attached)` when the text ends there), so one enter sends.
- A paste with several lines, or of 2000 characters or more, becomes a part. A paste that is one path to a file is a
  dropped file, and that file stays where it is. `ctrl+v` saves the clipboard's image, because a terminal cannot paste one.
- `[n]` in the text marks where the part goes. A part with no `[n]` left goes at the end.
- The box shows the first and last three lines, the size, and whether the part matches the clipboard. `ctrl+p` picks a
  part, enter shows it whole, x removes it. An image has a thumbnail in `▀` cells and a card.
- A check opens before sending only when there is something to look at: what looks like a secret, a line of 5000
  characters or more, a file of 200 KB or more, a mismatch with the clipboard. It shows where, and never the whole secret.
- Parts go only to an idle session. A fork does not read them, and no test has shown that `@path` works there.

The daemon does not change: it gets one line of the human's text, as before.

## Limits

- Only the pty route in herdr is checked. Terminal.app and iTerm2 type the same characters, but no live test ran there.
- The size limit is between about 50 KB, which went whole, and 452 KB, which did not. 200 KB is a guess inside that gap.
- The secret rules look for common shapes. They can miss a key, and they can flag a line that is not one.
- `ctrl+v` on macOS uses `osascript` to read the clipboard. It saved a real clipboard image as a valid PNG on 2026-11-05. The Linux route (`wl-paste`, `xclip`) has not run.
- Only Claude Code takes `@path` this way, and only verified agents take typing (ADR-0019).

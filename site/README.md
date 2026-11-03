# smif.dev

The landing page: static files, no build step, no external requests (fonts are self-hosted).

- `index.html`, `styles.css`, `main.js`: the page. `main.js` holds the hero chapters (`scenes`, times in seconds
  of `media/hero.*`).
- `media/`: the recordings and their posters. `fonts/`: Bricolage Grotesque, Geist, JetBrains Mono (OFL).
- `og.png`: the share image, rendered from `demo/og.html`.

Preview: `python3 -m http.server -d site 8765`, then open http://127.0.0.1:8765/.

## Recordings

The videos are real smif runs on staged sessions, recorded with [VHS](https://github.com/charmbracelet/vhs)
(`brew install vhs tmux`, font JetBrains Mono installed).

- `demo/director.py` stages eight fake Claude Code sessions under `/tmp/smifdemo`: a fake `HOME` with session files
  and transcripts, a stand-in `claude` (`demo/fakeclaude`) in a private tmux server, and a private smif daemon. It
  never touches the real `~/.claude` or smif install. When smif types a prompt into a pane, it plays the session's
  work (busy, tool calls, answer).
- `cd site/demo && vhs tapes/hero.tape && vhs tapes/chat.tape && ./media.sh` records both clips and copies them to
  `media/`. The tapes start the director themselves.
- The tapes never press `n`: a new session would start the real `claude`.
- After a new hero recording, check the scene times in `main.js` (`ffmpeg -i demo/out/hero.mp4 -vf fps=1,tile=3x7`).

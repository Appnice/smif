# ADR-0017: macOS notifications come from a small Smif.app

**Status:** accepted, 2026-11-04.

**Context.** macOS shows a notification with the icon of the app that raised it. Through `osascript` that is
Script Editor, so a notice from smif looked like it came from a script. `terminal-notifier` shows its own icon,
and its `-appIcon` option has no effect on current macOS. An AppleScript applet gets no arguments when it starts.

**Decision.**

- On first use the daemon builds `<data dir>/Smif.app` on this Mac: `osacompile` makes an applet, smif's icon
  (`internal/notify/smif.icns`, from `docs/brand/icns.sh`) replaces the default one, the bundle id is
  `dev.smif.notify`, `LSUIElement` keeps it out of the Dock, and `codesign --sign -` signs it ad hoc. A stamp
  (hash of the script and the icon) rebuilds it when a new binary changes either.
- Each notification is one file in `<data dir>/notify-queue/` (title line, body line), written atomically. The
  daemon then runs `open -n -g -j Smif.app`. The applet claims each file with `mv`, shows it and quits.
- The title drops the `Smif: ` prefix there: the icon names smif.
- When the build or the start fails, `terminal-notifier` or `osascript` are used as before. Linux is unchanged.

**Consequences.** The first notification makes macOS ask the human to allow notifications from Smif. Nothing
extra to install: `osacompile`, `plutil`, `codesign` and `open` come with macOS. `smif doctor` checks that
`osacompile` is there.

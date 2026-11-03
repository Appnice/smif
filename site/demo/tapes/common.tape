# Shared settings for every tape (vhs tapes/<name>.tape from site/demo).
Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 17
Set Width 1680
Set Height 980
Set Padding 24
Set Framerate 30
Set TypingSpeed 55ms
Set CursorBlink false
Set Theme {"name":"smif-gruvbox","black":"#282828","red":"#fb4934","green":"#b8bb26","yellow":"#fabd2f","blue":"#83a598","magenta":"#d3869b","cyan":"#8ec07c","white":"#d5c4a1","brightBlack":"#928374","brightRed":"#fb4934","brightGreen":"#b8bb26","brightYellow":"#fabd2f","brightBlue":"#83a598","brightMagenta":"#d3869b","brightCyan":"#8ec07c","brightWhite":"#fbf1c7","background":"#1d2021","foreground":"#ebdbb2","selection":"#504945","cursor":"#ebdbb2"}
Env HOME "/tmp/smifdemo/home"
Env CLAUDE_CONFIG_DIR "/tmp/smifdemo/home/.claude"
Env SMIF_SOCKET "/tmp/smifdemo/s.sock"
Env TMUX_TMPDIR "/tmp/smifdemo/tmux"
Env SMIF_NO_AUTOSTART "1"
Env TERM "xterm-256color"
Env COLORTERM "truecolor"
Env LANG "en_US.UTF-8"
Hide
Type "export PS1='$ '; python3 director.py up > /tmp/smifdemo.log 2>&1 & until grep -q ready /tmp/smifdemo.log 2>/dev/null; do sleep 0.2; done; touch /tmp/smifdemo/go; clear"
Enter
Wait /^\$\s*$/
Sleep 200ms
Show

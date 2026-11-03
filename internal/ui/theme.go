package ui

import "charm.land/lipgloss/v2"

// The styles of every screen. applyTheme sets them; SetupColor picks the
// colour or the no-colour set.
var (
	stTitle   lipgloss.Style
	stFocus   lipgloss.Style
	stDim     lipgloss.Style
	stBad     lipgloss.Style
	stWarn    lipgloss.Style
	stCursor  lipgloss.Style
	stHeading lipgloss.Style

	stAssistant lipgloss.Style
	stTool      lipgloss.Style
	stOK        lipgloss.Style
	stAdd       lipgloss.Style
	stDel       lipgloss.Style
	stHunk      lipgloss.Style
	stLive      lipgloss.Style // a working session (also marked ▶)
	stNew       lipgloss.Style // a new answer (also marked ●)

	// The home screen.
	stBadge   lipgloss.Style // the "mailroom" name in the top line
	stTabOn   lipgloss.Style // the selected agent tab
	stAccent  lipgloss.Style // keys, the unread dot, the thread mark
	stSelRow  lipgloss.Style // the selected list row (also marked ▌)
	stFrame   lipgloss.Style // a box frame
	stFrameOn lipgloss.Style // the frame that has the keys
)

func init() { applyTheme(true) }

// applyTheme sets the styles. Without colour, the styles keep only text
// attributes (bold, faint, underline, reverse). Every meaning a colour
// carries is also in the text: "DAEMON DOWN", "ERROR", the "▶" focus mark,
// the "> " cursor, "[1 RECORD]" for the open tab, "+"/"-" in a diff.
//
// The colours are Gruvbox dark (github.com/morhetz/gruvbox), picked by the
// user from five eye-friendly palettes on 2026-11-03: warm, muted, each
// text colour at least 4.5:1 on Gruvbox's #282828. Terminals without true
// colour get the nearest 256 colours (Lip Gloss downsamples). No ANSI blue
// or magenta: many dark themes draw them navy and purple, too dark to read.
const (
	gbFg     = "#ebdbb2" // light1: assistant text
	gbGray   = "#a89984" // light4: faint text
	gbRed    = "#fb4934" // errors, dead letters
	gbGreen  = "#b8bb26" // ok, done, diff additions
	gbYellow = "#fabd2f" // a new answer, the name badge
	gbBlue   = "#83a598" // keys, the focused frame, tool names
	gbAqua   = "#8ec07c" // a working session, code
	gbOrange = "#fe8019" // waits for you, warnings
	gbBg0    = "#282828" // dark0: text on the badge
	gbBg1    = "#3c3836" // dark1: the selected row
	gbBg2    = "#504945" // dark2: frames
)

func applyTheme(color bool) {
	fg := func(s lipgloss.Style, c string) lipgloss.Style {
		if color {
			return s.Foreground(lipgloss.Color(c))
		}
		return s
	}
	bold := lipgloss.NewStyle().Bold(true)
	stTitle = bold
	stFocus = lipgloss.NewStyle().Bold(true).Reverse(true)
	stDim = lipgloss.NewStyle().Faint(true)
	stBad = fg(bold, gbRed)
	stWarn = fg(bold, gbOrange)
	stCursor = lipgloss.NewStyle().Reverse(true)
	stHeading = lipgloss.NewStyle().Underline(true)

	stAssistant = fg(bold, gbFg)
	stTool = fg(bold, gbBlue)
	stOK = fg(bold, gbGreen)
	stAdd = fg(lipgloss.NewStyle(), gbGreen)
	stDel = fg(lipgloss.NewStyle(), gbRed)
	stHunk = fg(lipgloss.NewStyle(), gbAqua)
	stLive = fg(bold, gbAqua)
	stNew = fg(bold, gbYellow)
	if !color {
		stHunk = lipgloss.NewStyle().Faint(true)
	}

	stBadge = lipgloss.NewStyle().Bold(true).Reverse(true)
	stTabOn = lipgloss.NewStyle().Bold(true).Underline(true)
	stAccent = fg(lipgloss.NewStyle(), gbBlue)
	stSelRow = lipgloss.NewStyle().Bold(true)
	stFrame = lipgloss.NewStyle().Faint(true)
	stFrameOn = fg(lipgloss.NewStyle(), gbBlue)
	if color {
		stDim = lipgloss.NewStyle().Foreground(lipgloss.Color(gbGray))
		stBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(gbBg0)).Background(lipgloss.Color(gbYellow))
		stSelRow = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color(gbBg1))
		stFrame = lipgloss.NewStyle().Foreground(lipgloss.Color(gbBg2))
	}
}

// NoColor reports whether the NO_COLOR variable asks for no colours: it
// is set and not empty (https://no-color.org).
func NoColor(getenv func(string) string) bool { return getenv("NO_COLOR") != "" }

// SetupColor prepares the styles before `mailroom ui` starts. With
// NO_COLOR the styles keep only text attributes (bold, faint, underline,
// reverse). Bubble Tea v2 downsamples what is left to the terminal's own
// profile, so a terminal without styles (TERM=dumb) gets plain text.
// It reports whether colours are off.
func SetupColor(getenv func(string) string) bool {
	off := NoColor(getenv)
	applyTheme(!off)
	return off
}

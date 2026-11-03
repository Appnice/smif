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
	stBad = fg(bold, "9")
	stWarn = fg(bold, "11")
	stCursor = lipgloss.NewStyle().Reverse(true)
	stHeading = lipgloss.NewStyle().Underline(true)

	stAssistant = fg(bold, "12")
	stTool = fg(bold, "13")
	stOK = fg(bold, "10")
	stAdd = fg(lipgloss.NewStyle(), "10")
	stDel = fg(lipgloss.NewStyle(), "9")
	stHunk = fg(lipgloss.NewStyle(), "14")
	if !color {
		stHunk = lipgloss.NewStyle().Faint(true)
	}

	stBadge = lipgloss.NewStyle().Bold(true).Reverse(true)
	stTabOn = lipgloss.NewStyle().Bold(true).Underline(true)
	stAccent = fg(lipgloss.NewStyle(), "12")
	stSelRow = lipgloss.NewStyle().Bold(true)
	stFrame = lipgloss.NewStyle().Faint(true)
	stFrameOn = fg(lipgloss.NewStyle(), "12")
	if color {
		stBadge = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("12"))
		stSelRow = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("236"))
		stFrame = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
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

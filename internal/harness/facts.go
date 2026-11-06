package harness

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Facts is what a transcript shows from the newest human prompt on
// (ADR-0021). The UI states these facts and does not judge the answer.
type Facts struct {
	Files         int    // distinct files that edit tools changed
	Commits       int    // Bash calls with git commit that succeeded
	Tests         string // TestsPassed, TestsFailed, TestsNone, or "" with no edit of a non-doc file
	ContextTokens int64  // input + cache read + cache creation of the newest assistant record
}

// Facts.Tests values. They speak only of the time after the last edit of a
// non-doc file.
const (
	TestsPassed = "passed"
	TestsFailed = "failed"
	TestsNone   = "none" // no test command finished after that edit
)

// Factser is a provider that reads Facts from a session's transcript. It
// reads only what the transcript added since the last call, so a call on
// an unchanged transcript parses nothing. isTest says whether a shell
// command is a test command; nil means the built-in list. ok is false with
// no transcript.
type Factser interface {
	Facts(sessionID string, isTest func(command string) bool) (f Facts, ok bool)
}

var (
	// quotedRe finds quoted shell text, so a commit message that names a
	// test command is not a test run.
	quotedRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|'[^']*'`)
	testRe   = regexp.MustCompile(`(?:^|[\s;&|(/=])(?:go test|make (?:check|test)|pytest|cargo test|npm (?:run )?test|vitest|jest|gradlew? test|flutter test)(?:$|[\s;&|):-])`)
	commitRe = regexp.MustCompile(`(?:^|[\s;&|(])git(?:\s+-[cC]\s+\S+|\s+--?[a-z][a-z-]*(?:=\S+)?)*\s+commit(?:$|[\s;&|)])`)
)

// unquoted is a shell command with quoted text replaced by "".
func unquoted(command string) string {
	return quotedRe.ReplaceAllString(command, `""`)
}

// IsTestCommand reports a shell command that runs tests: a built-in
// pattern outside quotes, or one of extra (daemon.test_commands) anywhere
// in the command.
func IsTestCommand(command string, extra []*regexp.Regexp) bool {
	if testRe.MatchString(unquoted(command)) {
		return true
	}
	for _, re := range extra {
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

// IsCommit reports a shell command that runs git commit.
func IsCommit(command string) bool {
	return commitRe.MatchString(unquoted(command))
}

// IsDocFile reports a file whose edit needs no new test run: .md, .txt, or
// anything under a docs folder.
func IsDocFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".txt":
		return true
	}
	return strings.Contains("/"+path, "/docs/")
}

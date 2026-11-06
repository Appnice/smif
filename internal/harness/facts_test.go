package harness

import (
	"regexp"
	"testing"
)

func TestIsTestCommand(t *testing.T) {
	extra := []*regexp.Regexp{regexp.MustCompile(`^\./run-tests\b`)}
	for cmd, want := range map[string]bool{
		"go test ./...": true,
		"export PATH=$PATH:/x; make check > /tmp/o 2>&1": true,
		"cd api && make test":                            true,
		"make test-unit":                                 true,
		"python -m pytest -q":                            true,
		"cargo test --all":                               true,
		"npm test":                                       true,
		"npm run test:unit":                              true,
		"npx vitest run":                                 true,
		"./node_modules/.bin/jest":                       true,
		"./gradlew test":                                 true,
		"gradle test":                                    true,
		"flutter test":                                   true,
		"./run-tests --fast":                             true,
		"go build ./...":                                 false,
		"cat jest.config.js":                             false,
		"vim vitest.config.ts":                           false,
		"rm -rf .pytest_cache":                           false,
		`git commit -m "make test pass"`:                 false,
		`echo 'go test ./...'`:                           false,
		"npm install":                                    false,
	} {
		if got := IsTestCommand(cmd, extra); got != want {
			t.Errorf("IsTestCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestIsCommit(t *testing.T) {
	for cmd, want := range map[string]bool{
		`git commit -m "x"`:                 true,
		"git add a.go && git commit -m x":   true,
		`git -C "/a b" commit -m x`:         true,
		"git -c user.name=x commit --amend": true,
		"git commit":                        true,
		"git commit-tree abc":               false,
		"git log --oneline":                 false,
		`echo "git commit"`:                 false,
		`git log --grep="git commit"`:       false,
		"gh pr create --title commit":       false,
	} {
		if got := IsCommit(cmd); got != want {
			t.Errorf("IsCommit(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestIsDocFile(t *testing.T) {
	for path, want := range map[string]bool{
		"/r/README.md": true, "/r/NOTES.TXT": true, "/r/docs/a.go": true, "docs/x.png": true,
		"/r/main.go": false, "/r/mydocs/a.go": false, "/r/docs.go": false,
	} {
		if got := IsDocFile(path); got != want {
			t.Errorf("IsDocFile(%q) = %v, want %v", path, got, want)
		}
	}
}

// Package paths resolves Mailroom's config, data and socket locations.
//
// Defaults follow RFC 8.1 and 5.4. Every location can be overridden with an
// environment variable, which the tests use to keep socket paths short
// (macOS limits Unix socket paths to 104 bytes).
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Home returns the user's home directory, honouring $HOME.
func Home() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// ConfigDir is ~/.config/mailroom unless MAILROOM_CONFIG_DIR is set.
func ConfigDir() string {
	if v := os.Getenv("MAILROOM_CONFIG_DIR"); v != "" {
		return v
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && runtime.GOOS != "darwin" {
		return filepath.Join(x, "mailroom")
	}
	return filepath.Join(Home(), ".config", "mailroom")
}

// ConfigFile is ConfigDir()/config.toml unless MAILROOM_CONFIG is set.
func ConfigFile() string {
	if v := os.Getenv("MAILROOM_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(ConfigDir(), "config.toml")
}

// TokenDir holds one token file per agent plus the admin token.
func TokenDir() string { return filepath.Join(ConfigDir(), "tokens") }

// DataDir is the state directory (RFC 8.1).
func DataDir() string {
	if v := os.Getenv("MAILROOM_DATA_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(Home(), "Library", "Application Support", "mailroom")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "mailroom")
	}
	return filepath.Join(Home(), ".local", "share", "mailroom")
}

// Socket is the daemon's Unix socket path (RFC 5.4).
func Socket() string {
	if v := os.Getenv("MAILROOM_SOCKET"); v != "" {
		return v
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(DataDir(), "mailroomd.sock")
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "mailroom", "mailroomd.sock")
	}
	return filepath.Join(DataDir(), "mailroomd.sock")
}

// ClaudeDir is ~/.claude unless CLAUDE_CONFIG_DIR is set.
func ClaudeDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".claude")
}

// Expand replaces a leading ~ with the home directory and cleans the path.
func Expand(p string) string {
	if p == "~" {
		return Home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(Home(), p[2:])
	}
	return filepath.Clean(p)
}

// EnsureDir creates dir with mode 0700.
func EnsureDir(dir string) error { return os.MkdirAll(dir, 0o700) }

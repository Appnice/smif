// Package paths resolves Smif's config, data and socket locations.
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

// ConfigDir is ~/.config/smif unless SMIF_CONFIG_DIR is set.
func ConfigDir() string {
	if v := os.Getenv("SMIF_CONFIG_DIR"); v != "" {
		return v
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && runtime.GOOS != "darwin" {
		return filepath.Join(x, "smif")
	}
	return filepath.Join(Home(), ".config", "smif")
}

// ConfigFile is ConfigDir()/config.toml unless SMIF_CONFIG is set.
func ConfigFile() string {
	if v := os.Getenv("SMIF_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(ConfigDir(), "config.toml")
}

// TokenDir holds one token file per agent plus the admin token.
func TokenDir() string { return filepath.Join(ConfigDir(), "tokens") }

// PartsDir keeps the parts of a prompt (ADR-0020). It is a cache folder and
// not DataDir, because a path with no spaces stays whole in a prompt, and on
// macOS DataDir has one.
func PartsDir() string {
	if v := os.Getenv("SMIF_PARTS_DIR"); v != "" {
		return v
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "smif", "parts")
	}
	return filepath.Join(Home(), ".cache", "smif", "parts")
}

// DataDir is the state directory (RFC 8.1).
func DataDir() string {
	if v := os.Getenv("SMIF_DATA_DIR"); v != "" {
		return v
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(Home(), "Library", "Application Support", "smif")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "smif")
	}
	return filepath.Join(Home(), ".local", "share", "smif")
}

// Socket is the daemon's Unix socket path (RFC 5.4).
func Socket() string {
	if v := os.Getenv("SMIF_SOCKET"); v != "" {
		return v
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(DataDir(), "smifd.sock")
	}
	if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" {
		return filepath.Join(x, "smif", "smifd.sock")
	}
	return filepath.Join(DataDir(), "smifd.sock")
}

// ClaudeDir is ~/.claude unless CLAUDE_CONFIG_DIR is set.
func ClaudeDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".claude")
}

// CodexDir is ~/.codex unless CODEX_HOME is set.
func CodexDir() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".codex")
}

// QwenDir is ~/.qwen unless QWEN_HOME is set. It holds sessions/.
func QwenDir() string {
	if v := os.Getenv("QWEN_HOME"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".qwen")
}

// QwenRuntimeDir holds Qwen Code's projects/: QWEN_RUNTIME_DIR moves it
// apart from QwenDir. Smif does not read the runtimeOutputDir setting.
func QwenRuntimeDir() string {
	if v := os.Getenv("QWEN_RUNTIME_DIR"); v != "" {
		return v
	}
	return QwenDir()
}

// OpenCodeDB is OpenCode's database: OPENCODE_DB, else under XDG_DATA_HOME,
// on macOS too. Smif reads only the default channel.
func OpenCodeDB() string {
	if v := os.Getenv("OPENCODE_DB"); v != "" {
		return v
	}
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		dir = filepath.Join(Home(), ".local", "share")
	}
	return filepath.Join(dir, "opencode", "opencode.db")
}

// CopilotDir is ~/.copilot unless COPILOT_HOME is set.
func CopilotDir() string {
	if v := os.Getenv("COPILOT_HOME"); v != "" {
		return v
	}
	return filepath.Join(Home(), ".copilot")
}

// FactoryDir is Droid's ~/.factory. FACTORY_HOME_OVERRIDE stands in for the
// home folder.
func FactoryDir() string {
	if v := os.Getenv("FACTORY_HOME_OVERRIDE"); v != "" {
		return filepath.Join(v, ".factory")
	}
	return filepath.Join(Home(), ".factory")
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

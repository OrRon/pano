package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the full pano configuration. Zero values are replaced by Default()
// before use; the TOML file only needs to list overrides.
type Config struct {
	Proxy       Proxy       `toml:"proxy"`
	Capture     Capture     `toml:"capture"`
	Retention   Retention   `toml:"retention"`
	Redaction   Redaction   `toml:"redaction"`
	Views       Views       `toml:"views"`
	Breakpoints Breakpoints `toml:"breakpoints"`
	MCP         MCP         `toml:"mcp"`
	SystemProxy SystemProxy `toml:"system_proxy"`
	Limits      Limits      `toml:"limits"`
}

// Proxy configures the listening proxy.
type Proxy struct {
	Port    int      `toml:"port"`
	MCPPort int      `toml:"mcp_port"`
	Bind    string   `toml:"bind"`
	Bypass  []string `toml:"bypass"`
}

// Capture configures what is recorded.
type Capture struct {
	Enabled          bool  `toml:"enabled"`
	MaxBodyBytes     int64 `toml:"max_body_bytes"`
	MaxInflightBytes int64 `toml:"max_inflight_bytes"`
	Persist          bool  `toml:"persist"`
	WebSocketFrames  bool  `toml:"websocket_frames"`
	RingSize         int   `toml:"ring_size"`
}

// Retention bounds the on-disk store.
type Retention struct {
	MaxAge     Duration `toml:"max_age"`
	MaxFlows   int      `toml:"max_flows"`
	MaxDBBytes int64    `toml:"max_db_bytes"`
}

// Redaction controls secret masking in views.
type Redaction struct {
	Enabled       bool     `toml:"enabled"`
	ExtraPatterns []string `toml:"extra_patterns"`
	ExtraHeaders  []string `toml:"extra_headers"`
}

// Views sets default token budgets for body rendering.
type Views struct {
	DefaultMaxBytes int `toml:"default_max_bytes"`
	ListPageSize    int `toml:"list_page_size"`
	StringTruncate  int `toml:"string_truncate"`
}

// Breakpoints configures held-request behaviour.
type Breakpoints struct {
	HoldTimeout Duration `toml:"hold_timeout"`
}

// MCP configures the MCP server.
type MCP struct {
	Autostart  bool `toml:"autostart"`
	ExposeHTTP bool `toml:"expose_http"`
}

// SystemProxy configures macOS system proxy integration.
type SystemProxy struct {
	RestoreOnExit bool `toml:"restore_on_exit"`
}

// Limits bounds resource usage.
type Limits struct {
	MaxConns int `toml:"max_conns"`
}

// Duration is a time.Duration that marshals as a human string ("7d", "90s").
type Duration struct{ time.Duration }

// UnmarshalText parses durations, accepting a trailing "d" for days.
func (d *Duration) UnmarshalText(b []byte) error {
	s := string(b)
	if n := len(s); n > 1 && s[n-1] == 'd' {
		var days float64
		if _, err := fmt.Sscanf(s[:n-1], "%g", &days); err != nil {
			return fmt.Errorf("config: bad duration %q: %w", s, err)
		}
		d.Duration = time.Duration(days * float64(24*time.Hour))
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: bad duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// MarshalText renders the duration.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// DefaultBypass lists hosts that are tunneled without decryption because they
// pin certificates or break under interception (Apple services, pinned SDKs).
var DefaultBypass = []string{
	"*.apple.com", "*.icloud.com", "*.icloud-content.com", "*.mzstatic.com",
	"*.cdn-apple.com", "*.push.apple.com", "*.apple-cloudkit.com", "*.ls.apple.com",
	"*.crashlytics.com",
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Proxy: Proxy{Port: 9091, MCPPort: 9092, Bind: "127.0.0.1", Bypass: append([]string(nil), DefaultBypass...)},
		Capture: Capture{
			Enabled: true, MaxBodyBytes: 4 << 20, MaxInflightBytes: 256 << 20,
			Persist: true, WebSocketFrames: true, RingSize: 10000,
		},
		Retention:   Retention{MaxAge: Duration{7 * 24 * time.Hour}, MaxFlows: 200_000, MaxDBBytes: 2 << 30},
		Redaction:   Redaction{Enabled: true},
		Views:       Views{DefaultMaxBytes: 4096, ListPageSize: 50, StringTruncate: 200},
		Breakpoints: Breakpoints{HoldTimeout: Duration{120 * time.Second}},
		MCP:         MCP{Autostart: true, ExposeHTTP: true},
		SystemProxy: SystemProxy{RestoreOnExit: true},
		Limits:      Limits{MaxConns: 10000},
	}
}

// Paths locates pano's files. All live under Dir (default ~/.pano, override
// with $PANO_HOME).
type Paths struct {
	Dir string
}

// DefaultPaths returns the standard layout.
func DefaultPaths() (Paths, error) {
	if d := os.Getenv("PANO_HOME"); d != "" {
		return Paths{Dir: d}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config: home dir: %w", err)
	}
	return Paths{Dir: filepath.Join(home, ".pano")}, nil
}

// ConfigFile is config.toml.
func (p Paths) ConfigFile() string { return filepath.Join(p.Dir, "config.toml") }

// Socket is the control API Unix socket.
func (p Paths) Socket() string {
	s := filepath.Join(p.Dir, "pano.sock")
	// Unix socket paths are limited to ~104 bytes on macOS/BSD.
	if len(s) > 100 {
		return filepath.Join(os.TempDir(), fmt.Sprintf("pano-%d.sock", os.Getuid()))
	}
	return s
}

// PIDFile holds the daemon pid.
func (p Paths) PIDFile() string { return filepath.Join(p.Dir, "daemon.pid") }

// LogFile is the daemon log.
func (p Paths) LogFile() string { return filepath.Join(p.Dir, "daemon.log") }

// AuditLog records secret reveals and system changes.
func (p Paths) AuditLog() string { return filepath.Join(p.Dir, "audit.log") }

// CACert is the root certificate (PEM).
func (p Paths) CACert() string { return filepath.Join(p.Dir, "ca.pem") }

// CAKey is the root private key (PEM, 0600).
func (p Paths) CAKey() string { return filepath.Join(p.Dir, "ca.key") }

// LeafKey is the shared leaf private key.
func (p Paths) LeafKey() string { return filepath.Join(p.Dir, "leaf.key") }

// CertCache holds minted leaf certificates.
func (p Paths) CertCache() string { return filepath.Join(p.Dir, "certs") }

// DB is the SQLite database.
func (p Paths) DB() string { return filepath.Join(p.Dir, "pano.db") }

// RulesFile persists rules.
func (p Paths) RulesFile() string { return filepath.Join(p.Dir, "rules.json") }

// SysProxyState is the system proxy snapshot.
func (p Paths) SysProxyState() string { return filepath.Join(p.Dir, "sysproxy.json") }

// Token is the bearer token for the TCP control listener.
func (p Paths) Token() string { return filepath.Join(p.Dir, "token") }

// Ensure creates Dir (0700) if missing.
func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return fmt.Errorf("config: create %s: %w", p.Dir, err)
	}
	return os.Chmod(p.Dir, 0o700) //nolint:gosec // directory: owner-only rwx is the intended mode
}

// Load reads config.toml over Default(). A missing file is not an error.
func Load(p Paths) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(p.ConfigFile())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("config: read: %w", err)
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", p.ConfigFile(), err)
	}
	return cfg, cfg.Validate()
}

// Save writes the config atomically.
func Save(p Paths, cfg Config) error {
	b, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	return WriteAtomic(p.ConfigFile(), b, 0o600)
}

// Validate checks ranges.
func (c Config) Validate() error {
	if c.Proxy.Port <= 0 || c.Proxy.Port > 65535 {
		return fmt.Errorf("config: proxy.port out of range: %d", c.Proxy.Port)
	}
	if c.Capture.MaxBodyBytes < 0 {
		return errors.New("config: capture.max_body_bytes must be >= 0")
	}
	if c.Views.DefaultMaxBytes <= 0 || c.Views.ListPageSize <= 0 {
		return errors.New("config: views limits must be > 0")
	}
	return nil
}

// WriteAtomic writes b to path via a temp file + rename.
func WriteAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil { //nolint:gosec // mode is chosen by the caller
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

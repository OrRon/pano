package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the full pano configuration. Zero values are replaced by Default()
// before use; the TOML file only needs to list overrides.
type Config struct {
	Proxy       Proxy       `toml:"proxy"`
	Decrypt     Decrypt     `toml:"decrypt"`
	Capture     Capture     `toml:"capture"`
	Redaction   Redaction   `toml:"redaction"`
	Views       Views       `toml:"views"`
	Breakpoints Breakpoints `toml:"breakpoints"`
	MCP         MCP         `toml:"mcp"`
	SystemProxy SystemProxy `toml:"system_proxy"`
	Limits      Limits      `toml:"limits"`
	Updates     Updates     `toml:"updates"`
}

// Proxy configures the listening proxy.
type Proxy struct {
	Port    int    `toml:"port"`
	MCPPort int    `toml:"mcp_port"`
	Bind    string `toml:"bind"`
	// Bypass is the pre-[decrypt] name of Decrypt.Never. Load migrates it and
	// Save never writes it back.
	Bypass []string `toml:"bypass,omitempty"`
}

// Decrypt says which HTTPS tunnels are TLS-terminated. Never wins in every
// mode; Only is consulted only when Mode is "only". Entries are hosts (which
// also cover their subdomains) or globs.
type Decrypt struct {
	Mode  string   `toml:"mode"`
	Only  []string `toml:"only"`
	Never []string `toml:"never"`
}

// Capture configures what is recorded. Everything captured is held in
// memory only: RingSize bounds how many flows are kept (oldest evicted) and
// nothing survives a daemon restart.
type Capture struct {
	Enabled          bool  `toml:"enabled"`
	MaxBodyBytes     int64 `toml:"max_body_bytes"`
	MaxInflightBytes int64 `toml:"max_inflight_bytes"`
	WebSocketFrames  bool  `toml:"websocket_frames"`
	RingSize         int   `toml:"ring_size"`
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

// Updates controls the once-a-day release check. It only ever prints a hint
// (never downloads or installs); see internal/update for every other way to
// turn it off.
type Updates struct {
	Check bool `toml:"check"`
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

// DefaultNever lists the hosts that are never decrypted out of the box: the
// macOS daemons that pin certificates and visibly break under interception
// (push notifications, iCloud sync, CloudKit, Maps). Deliberately minimal —
// anything else that pins shows up under "rejected" for the user to decide.
var DefaultNever = []string{
	"*.push.apple.com", "*.icloud.com", "*.icloud-content.com", "*.apple-cloudkit.com", "*.ls.apple.com",
}

// Default returns the built-in configuration.
func Default() Config {
	return Config{
		Proxy:   Proxy{Port: 9091, MCPPort: 9092, Bind: "127.0.0.1"},
		Decrypt: Decrypt{Mode: "all", Only: []string{}, Never: append([]string(nil), DefaultNever...)},
		Capture: Capture{
			Enabled: true, MaxBodyBytes: 4 << 20, MaxInflightBytes: 256 << 20,
			WebSocketFrames: true, RingSize: 10000,
		},
		Redaction:   Redaction{Enabled: true},
		Views:       Views{DefaultMaxBytes: 4096, ListPageSize: 50, StringTruncate: 200},
		Breakpoints: Breakpoints{HoldTimeout: Duration{120 * time.Second}},
		MCP:         MCP{ExposeHTTP: true},
		SystemProxy: SystemProxy{RestoreOnExit: true},
		Limits:      Limits{MaxConns: 10000},
		Updates:     Updates{Check: true},
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

// RulesFile persists rules.
func (p Paths) RulesFile() string { return filepath.Join(p.Dir, "rules.json") }

// SysProxyState is the system proxy snapshot.
func (p Paths) SysProxyState() string { return filepath.Join(p.Dir, "sysproxy.json") }

// SimulatorState is the record of which iOS Simulators pano's CA has been
// installed into (and which asked not to be suggested again).
func (p Paths) SimulatorState() string { return filepath.Join(p.Dir, "simulators.json") }

// UpdateState caches the last release check (internal/update).
func (p Paths) UpdateState() string { return filepath.Join(p.Dir, "update-check.json") }

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
// Deprecated keys are migrated in memory (see LoadWithWarnings).
func Load(p Paths) (Config, error) {
	cfg, _, err := LoadWithWarnings(p)
	return cfg, err
}

// LoadWithWarnings is Load plus one human-readable line per migrated or
// deprecated key, for the daemon log and `pano config get`.
func LoadWithWarnings(p Paths) (Config, []string, error) {
	cfg := Default()
	b, err := os.ReadFile(p.ConfigFile())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil, nil
	}
	if err != nil {
		return cfg, nil, fmt.Errorf("config: read: %w", err)
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return cfg, nil, fmt.Errorf("config: parse %s: %w", p.ConfigFile(), err)
	}
	var warnings []string
	{
		// Flows stopped being written to disk in ADR 0011; the old keys are
		// accepted and ignored so existing files keep loading.
		var probe struct {
			Retention *struct{} `toml:"retention"`
			Capture   struct {
				Persist *bool `toml:"persist"`
			} `toml:"capture"`
		}
		_ = toml.Unmarshal(b, &probe)
		if probe.Retention != nil {
			warnings = append(warnings, "config: [retention] is ignored; flows are kept in memory only and gone when pano stops (see capture.ring_size)")
		}
		if probe.Capture.Persist != nil {
			warnings = append(warnings, "config: capture.persist is ignored; flows are never written to disk any more")
		}
	}
	if len(cfg.Proxy.Bypass) > 0 {
		// Presence check: only migrate when the file has no [decrypt] table,
		// otherwise the new key is authoritative.
		var probe struct {
			Decrypt *struct{} `toml:"decrypt"`
		}
		_ = toml.Unmarshal(b, &probe)
		if probe.Decrypt == nil {
			cfg.Decrypt.Never = append([]string(nil), cfg.Proxy.Bypass...)
			warnings = append(warnings, "config: [proxy] bypass is deprecated and was read as [decrypt] never; it will be rewritten on the next save")
		} else {
			warnings = append(warnings, "config: [proxy] bypass is ignored because [decrypt] is present; remove it")
		}
		cfg.Proxy.Bypass = nil
	}
	return cfg, warnings, cfg.Validate()
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
	switch c.Decrypt.Mode {
	case "all", "only", "off":
	default:
		return fmt.Errorf("config: decrypt.mode must be all, only or off (got %q)", c.Decrypt.Mode)
	}
	for _, list := range [][]string{c.Decrypt.Only, c.Decrypt.Never} {
		for _, h := range list {
			if strings.TrimSpace(h) == "" || strings.ContainsAny(h, " \t/") {
				return fmt.Errorf("config: bad decrypt host entry %q", h)
			}
		}
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

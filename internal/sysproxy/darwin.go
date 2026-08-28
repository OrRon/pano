//go:build darwin

package sysproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/config"
)

const (
	networksetupPath = "/usr/sbin/networksetup"
	osascriptPath    = "/usr/bin/osascript"
	snapshotVersion  = 1

	// noBypassMarker is the value networksetup expects when the bypass list
	// must be cleared.
	noBypassMarker = "Empty"
)

// New returns a Manager that drives /usr/sbin/networksetup and keeps its
// snapshot in statePath (normally config.Paths.SysProxyState()). A nil logger
// discards log output.
func New(statePath string, logger *slog.Logger) Manager {
	return newManager(statePath, logger, execRunner{})
}

// runner executes an external command and returns its standard output. It is
// an interface so tests can substitute a fake networksetup.
type runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
}

// cmdError is returned by execRunner when a command exits with a non-zero
// status. It keeps both output streams: networksetup prints some of its
// diagnostics ("** Error: …") on stdout.
type cmdError struct {
	name   string
	args   []string
	code   int
	stdout string
	stderr string
}

func (e *cmdError) Error() string {
	msg := fmt.Sprintf("%s %s: exit status %d", filepath.Base(e.name), strings.Join(e.args, " "), e.code)
	if out := strings.TrimSpace(e.stderr + "\n" + e.stdout); out != "" {
		msg += ": " + out
	}
	return msg
}

// output returns both output streams, lower-cased, for keyword matching.
func (e *cmdError) output() string {
	return strings.ToLower(e.stderr + "\n" + e.stdout)
}

// execRunner runs real commands.
type execRunner struct{}

// Run implements runner.
func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return stdout.String(), &cmdError{
				name: name, args: args, code: exit.ExitCode(),
				stdout: stdout.String(), stderr: stderr.String(),
			}
		}
		return stdout.String(), fmt.Errorf("%s: %w", filepath.Base(name), err)
	}
	return stdout.String(), nil
}

// snapshot is the on-disk state file: what every enabled service looked like
// before pano touched it.
type snapshot struct {
	Version  int            `json:"version"`
	SetAt    time.Time      `json:"set_at"`
	Pano     endpoint       `json:"pano"`
	Services []serviceState `json:"services"`
	// UsedAdmin records that administrator privileges (osascript) were needed
	// to apply the settings, so the restore uses the same path directly.
	UsedAdmin bool `json:"used_admin,omitempty"`
}

// endpoint is the proxy address pano asked the system to use.
type endpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (e endpoint) String() string { return e.Host + ":" + strconv.Itoa(e.Port) }

// serviceState is the proxy configuration of one network service.
type serviceState struct {
	Name   string       `json:"name"`
	Web    proxySetting `json:"web"`
	Secure proxySetting `json:"secure"`
	Bypass []string     `json:"bypass"`
}

// proxySetting is the output of -getwebproxy / -getsecurewebproxy.
type proxySetting struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
	Port    int    `json:"port"`
}

// pointsAt reports whether the setting is on and targets host:port.
func (p proxySetting) pointsAt(host string, port int) bool {
	return p.Enabled && p.Server == host && p.Port == port
}

type manager struct {
	statePath string
	log       *slog.Logger
	run       runner
	binary    string // networksetup; overridable so tests do not depend on the host
	now       func() time.Time

	mu sync.Mutex // serialises Enable / Disable / RestoreStale
}

func newManager(statePath string, logger *slog.Logger, r runner) *manager {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &manager{
		statePath: statePath,
		log:       logger.With("component", "sysproxy"),
		run:       r,
		binary:    networksetupPath,
		now:       time.Now,
	}
}

// Supported reports whether networksetup is present on this host.
func (m *manager) Supported() bool {
	_, err := os.Stat(m.binary)
	return err == nil
}

// Enable implements Manager.
func (m *manager) Enable(ctx context.Context, host string, port int, bypass []string) error {
	if host == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("sysproxy: invalid proxy address %q:%d", host, port)
	}
	if !m.Supported() {
		return fmt.Errorf("sysproxy: %s not found", m.binary)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	snap, err := m.readSnapshot()
	if err != nil {
		return err
	}
	if snap == nil {
		if snap, err = m.capture(ctx, host, port); err != nil {
			return err
		}
	} else {
		// Keep the pre-pano settings; only the target may have changed.
		m.log.Info("keeping existing snapshot", "path", m.statePath, "set_at", snap.SetAt)
		snap.Pano = endpoint{Host: host, Port: port}
	}
	// The snapshot must be durable before the first change is made, so a
	// crash at any later point is recoverable by RestoreStale.
	if err := m.writeSnapshot(snap); err != nil {
		return err
	}

	portStr := strconv.Itoa(port)
	usedAdmin := snap.UsedAdmin
	var errs []error
	for _, svc := range snap.Services {
		cmds := [][]string{
			{"-setwebproxy", svc.Name, host, portStr},
			{"-setsecurewebproxy", svc.Name, host, portStr},
			append([]string{"-setproxybypassdomains", svc.Name}, bypassArgs(mergeBypass(svc.Bypass, bypass))...),
		}
		for _, args := range cmds {
			admin, err := m.set(ctx, usedAdmin, args...)
			usedAdmin = usedAdmin || admin
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", svc.Name, err))
				break
			}
		}
		m.log.Info("system proxy set", "service", svc.Name, "proxy", snap.Pano.String())
	}
	if usedAdmin && !snap.UsedAdmin {
		snap.UsedAdmin = true
		if err := m.writeSnapshot(snap); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("sysproxy: enable incomplete (run `pano off` to restore): %w", errors.Join(errs...))
	}
	return nil
}

// Disable implements Manager.
func (m *manager) Disable(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.restoreLocked(ctx)
	return err
}

// RestoreStale implements Manager.
func (m *manager) RestoreStale(ctx context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restoreLocked(ctx)
}

// restoreLocked restores the snapshot if one exists and deletes the state
// file on full success. It reports whether a snapshot was found.
func (m *manager) restoreLocked(ctx context.Context) (bool, error) {
	snap, err := m.readSnapshot()
	if err != nil {
		return false, err
	}
	if snap == nil {
		return false, nil
	}
	usedAdmin := snap.UsedAdmin
	var errs []error
	for _, svc := range snap.Services {
		var svcErrs []error
		for _, args := range restoreCommands(svc) {
			admin, err := m.set(ctx, usedAdmin, args...)
			usedAdmin = usedAdmin || admin
			if err == nil {
				continue
			}
			if isMissingService(err) {
				m.log.Warn("network service no longer exists; skipping restore", "service", svc.Name)
				svcErrs = nil
				break
			}
			svcErrs = append(svcErrs, fmt.Errorf("%s: %w", svc.Name, err))
		}
		errs = append(errs, svcErrs...)
		m.log.Info("system proxy restored", "service", svc.Name, "errors", len(svcErrs))
	}
	if len(errs) > 0 {
		return true, fmt.Errorf("sysproxy: restore incomplete, state kept at %s: %w", m.statePath, errors.Join(errs...))
	}
	if err := os.Remove(m.statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return true, fmt.Errorf("sysproxy: remove state file: %w", err)
	}
	return true, nil
}

// restoreCommands returns the networksetup invocations that put svc back to
// its snapshotted configuration.
func restoreCommands(svc serviceState) [][]string {
	var cmds [][]string
	if svc.Web.Enabled {
		cmds = append(cmds,
			[]string{"-setwebproxy", svc.Name, svc.Web.Server, strconv.Itoa(svc.Web.Port)},
			[]string{"-setwebproxystate", svc.Name, "on"})
	} else {
		cmds = append(cmds, []string{"-setwebproxystate", svc.Name, "off"})
	}
	if svc.Secure.Enabled {
		cmds = append(cmds,
			[]string{"-setsecurewebproxy", svc.Name, svc.Secure.Server, strconv.Itoa(svc.Secure.Port)},
			[]string{"-setsecurewebproxystate", svc.Name, "on"})
	} else {
		cmds = append(cmds, []string{"-setsecurewebproxystate", svc.Name, "off"})
	}
	cmds = append(cmds, append([]string{"-setproxybypassdomains", svc.Name}, bypassArgs(svc.Bypass)...))
	return cmds
}

// Status implements Manager.
func (m *manager) Status(ctx context.Context, host string, port int) (api.SysProxy, error) {
	st := api.SysProxy{Supported: m.Supported()}
	target := endpoint{Host: host, Port: port}

	snap, err := m.readSnapshot()
	if err != nil {
		st.Detail = err.Error()
		return st, err
	}
	if snap != nil {
		st.SetByPano = true
		for _, svc := range snap.Services {
			st.Services = append(st.Services, svc.Name)
		}
	}
	if !st.Supported {
		st.Detail = m.binary + " not found"
		return st, nil
	}
	if err := ctx.Err(); err != nil {
		// Best effort: the state file alone still answers "did pano set it".
		st.Detail = "current settings not queried: " + err.Error()
		return st, nil //nolint:nilerr // status is valid without the live query
	}

	names, err := m.listServices(ctx)
	if err != nil {
		st.Detail = err.Error()
		return st, err
	}
	var pointing []string
	var errs []error
	for _, name := range names {
		web, err := m.getProxy(ctx, "-getwebproxy", name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		secure, err := m.getProxy(ctx, "-getsecurewebproxy", name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if web.pointsAt(host, port) || secure.pointsAt(host, port) {
			pointing = append(pointing, name)
		}
	}
	st.Enabled = len(pointing) > 0
	switch {
	case st.Enabled:
		st.Services = pointing
		st.Detail = fmt.Sprintf("%s → %s", pluralServices(pointing), target)
	case st.SetByPano:
		st.Detail = fmt.Sprintf("state file present but no service points at %s (stale; run `pano doctor`)", target)
	default:
		st.Detail = fmt.Sprintf("no service points at %s", target)
	}
	return st, errors.Join(errs...)
}

func pluralServices(names []string) string {
	if len(names) == 1 {
		return "1 service (" + names[0] + ")"
	}
	return fmt.Sprintf("%d services (%s)", len(names), strings.Join(names, ", "))
}

// capture queries every enabled service and builds a fresh snapshot.
func (m *manager) capture(ctx context.Context, host string, port int) (*snapshot, error) {
	names, err := m.listServices(ctx)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, errors.New("sysproxy: no enabled network services found")
	}
	snap := &snapshot{
		Version: snapshotVersion,
		SetAt:   m.now().UTC(),
		Pano:    endpoint{Host: host, Port: port},
	}
	for _, name := range names {
		svc := serviceState{Name: name}
		if svc.Web, err = m.getProxy(ctx, "-getwebproxy", name); err != nil {
			return nil, err
		}
		if svc.Secure, err = m.getProxy(ctx, "-getsecurewebproxy", name); err != nil {
			return nil, err
		}
		out, err := m.run.Run(ctx, m.binary, "-getproxybypassdomains", name)
		if err != nil {
			return nil, fmt.Errorf("sysproxy: %w", err)
		}
		svc.Bypass = parseBypass(out)
		snap.Services = append(snap.Services, svc)
	}
	return snap, nil
}

func (m *manager) listServices(ctx context.Context) ([]string, error) {
	out, err := m.run.Run(ctx, m.binary, "-listallnetworkservices")
	if err != nil {
		return nil, fmt.Errorf("sysproxy: list network services: %w", err)
	}
	return parseServices(out), nil
}

func (m *manager) getProxy(ctx context.Context, op, service string) (proxySetting, error) {
	out, err := m.run.Run(ctx, m.binary, op, service)
	if err != nil {
		return proxySetting{}, fmt.Errorf("sysproxy: %w", err)
	}
	p, err := parseProxy(out)
	if err != nil {
		return proxySetting{}, fmt.Errorf("sysproxy: %s %s: %w", op, service, err)
	}
	return p, nil
}

// set runs one networksetup -set* command. When the direct call is refused for
// lack of privileges (or admin is already known to be required) it is re-run
// through osascript's "with administrator privileges", which shows the
// standard macOS password prompt. It reports whether that path was used.
func (m *manager) set(ctx context.Context, admin bool, args ...string) (usedAdmin bool, err error) {
	if !admin {
		_, err := m.run.Run(ctx, m.binary, args...)
		if err == nil {
			return false, nil
		}
		if !isPermissionError(err) {
			return false, err
		}
		m.log.Info("networksetup refused; retrying with administrator privileges", "cmd", args[0])
	}
	if _, err := m.run.Run(ctx, osascriptPath, "-e", adminScript(m.binary, args)); err != nil {
		return true, fmt.Errorf("%s (with administrator privileges): %w", args[0], err)
	}
	return true, nil
}

// permissionHints are the substrings that identify a privilege failure in
// networksetup's output.
var permissionHints = []string{"permission", "not permitted", "authoriz", "admin"}

// isPermissionError reports whether err is a non-zero exit whose output
// suggests the caller lacks the privileges to change network settings.
func isPermissionError(err error) bool {
	var ce *cmdError
	if !errors.As(err, &ce) || ce.code == 0 {
		return false
	}
	out := ce.output()
	for _, hint := range permissionHints {
		if strings.Contains(out, hint) {
			return true
		}
	}
	return false
}

// isMissingService reports whether err is networksetup complaining that the
// named service does not exist (exit status 8, "Unable to find item in
// network database").
func isMissingService(err error) bool {
	var ce *cmdError
	return errors.As(err, &ce) && strings.Contains(ce.output(), "unable to find item in network database")
}

// adminScript builds the AppleScript that runs name args… as root. Every
// argument is single-quoted for /bin/sh, then the whole command is escaped as
// an AppleScript string literal.
func adminScript(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	for _, a := range append([]string{name}, args...) {
		parts = append(parts, shellQuote(a))
	}
	cmd := strings.Join(parts, " ")
	return `do shell script "` + appleScriptEscape(cmd) + `" with administrator privileges`
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func appleScriptEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// bypassArgs returns the arguments for -setproxybypassdomains: the list, or
// the "Empty" marker when the list must be cleared.
func bypassArgs(list []string) []string {
	if len(list) == 0 {
		return []string{noBypassMarker}
	}
	return list
}

// parseServices parses -listallnetworkservices output: a leading legend line,
// then one service per line, disabled services prefixed with "*".
func parseServices(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "An asterisk"), strings.HasPrefix(line, "*"):
			continue
		}
		names = append(names, line)
	}
	return names
}

// parseProxy parses -getwebproxy / -getsecurewebproxy output:
//
//	Enabled: No
//	Server: 127.0.0.1
//	Port: 9090
//	Authenticated Proxy Enabled: 0
func parseProxy(out string) (proxySetting, error) {
	var p proxySetting
	seen := 0
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.TrimSpace(key) {
		case "Enabled":
			p.Enabled = strings.EqualFold(val, "yes")
			seen++
		case "Server":
			p.Server = val
			seen++
		case "Port":
			if val != "" {
				n, err := strconv.Atoi(val)
				if err != nil {
					return proxySetting{}, fmt.Errorf("bad port %q", val)
				}
				p.Port = n
			}
			seen++
		}
	}
	if seen < 3 {
		return proxySetting{}, fmt.Errorf("unexpected output %q", strings.TrimSpace(out))
	}
	return p, nil
}

// parseBypass parses -getproxybypassdomains output: one domain per line, or a
// sentence saying there are none.
func parseBypass(out string) []string {
	var list []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "There aren't any bypass domains") {
			continue
		}
		list = append(list, line)
	}
	return list
}

// readSnapshot loads the state file. It returns (nil, nil) when none exists.
func (m *manager) readSnapshot() (*snapshot, error) {
	b, err := os.ReadFile(m.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sysproxy: read state: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("sysproxy: parse state %s: %w", m.statePath, err)
	}
	if snap.Version != snapshotVersion {
		return nil, fmt.Errorf("sysproxy: state %s has unsupported version %d", m.statePath, snap.Version)
	}
	return &snap, nil
}

// writeSnapshot atomically writes the state file with mode 0600.
func (m *manager) writeSnapshot(snap *snapshot) error {
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return fmt.Errorf("sysproxy: %w", err)
	}
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("sysproxy: encode state: %w", err)
	}
	if err := config.WriteAtomic(m.statePath, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("sysproxy: %w", err)
	}
	return nil
}

// Package simulator finds booted iOS Simulators and installs pano's root CA
// into their trust stores.
//
// The Simulator shares the Mac's network stack, so `pano on` already routes
// its traffic — decryption is the only missing piece, because each simulator
// keeps its own trust store (the Mac's keychain does not apply). Since Xcode
// 12.5 the supported way in is Xcode's own CLI:
//
//	xcrun simctl keychain <udid> add-root-cert <ca.pem>
//
// which lands the certificate directly in the trusted root store — no
// Settings toggle, unlike a physical iPhone. A reboot of the simulator makes
// running apps pick it up.
//
// pano never installs by itself: it detects and suggests, the user confirms
// (`pano ca install --simulator`, or one key in the TUI). A state file
// remembers which simulators already have the current CA — and which asked
// not to be suggested again — so the suggestion appears only when useful.
package simulator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/orron/pano/internal/config"
)

const xcrunPath = "/usr/bin/xcrun"

// Device is one booted simulator.
type Device struct {
	UDID    string `json:"udid"`
	Name    string `json:"name"`    // "iPhone 17"
	Runtime string `json:"runtime"` // "iOS 26.4"
}

// Label renders the device for humans: "iPhone 17 (iOS 26.4)".
func (d Device) Label() string {
	if d.Runtime == "" {
		return d.Name
	}
	return d.Name + " (" + d.Runtime + ")"
}

// runner executes an external command; a seam for tests.
type runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg != "" {
			return stdout.String(), fmt.Errorf("%s %s: %s", filepath.Base(name), args[0], msg)
		}
		return stdout.String(), fmt.Errorf("%s %s: %w", filepath.Base(name), args[0], err)
	}
	return stdout.String(), nil
}

// Manager detects simulators and installs the CA. Safe for concurrent use in
// the read paths; Install/Dismiss serialise on the state file via WriteAtomic.
type Manager struct {
	certPath  string
	statePath string
	binary    string
	run       runner
}

// New returns a Manager for pano's CA at certPath, remembering installs in
// statePath (normally config.Paths.SimulatorState()).
func New(certPath, statePath string) *Manager {
	return &Manager{certPath: certPath, statePath: statePath, binary: xcrunPath, run: execRunner{}}
}

// Supported reports whether xcrun exists (Xcode or its Command Line Tools).
func (m *Manager) Supported() bool {
	_, err := os.Stat(m.binary)
	return err == nil
}

// simctlList mirrors `simctl list devices booted -j`.
type simctlList struct {
	Devices map[string][]struct {
		UDID        string `json:"udid"`
		Name        string `json:"name"`
		State       string `json:"state"`
		IsAvailable bool   `json:"isAvailable"`
	} `json:"devices"`
}

// Booted lists the simulators that are running right now.
func (m *Manager) Booted(ctx context.Context) ([]Device, error) {
	if !m.Supported() {
		return nil, errors.New("simulator: xcrun not found — install Xcode or its Command Line Tools")
	}
	out, err := m.run.Run(ctx, m.binary, "simctl", "list", "devices", "booted", "-j")
	if err != nil {
		return nil, fmt.Errorf("simulator: %w", err)
	}
	var list simctlList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, fmt.Errorf("simulator: parse simctl list: %w", err)
	}
	var devs []Device
	for runtime, entries := range list.Devices {
		for _, e := range entries {
			if e.State != "Booted" || !e.IsAvailable {
				continue
			}
			devs = append(devs, Device{UDID: e.UDID, Name: e.Name, Runtime: parseRuntime(runtime)})
		}
	}
	sort.Slice(devs, func(i, j int) bool { return devs[i].Name < devs[j].Name })
	return devs, nil
}

// Suggest returns the booted simulators worth offering an install for: the
// current CA is not recorded as installed and the simulator was not
// dismissed. It is best-effort — any failure returns nil devices.
func (m *Manager) Suggest(ctx context.Context) []Device {
	if !m.Supported() {
		return nil
	}
	fp, err := m.fingerprint()
	if err != nil {
		return nil
	}
	booted, err := m.Booted(ctx)
	if err != nil {
		return nil
	}
	st := m.readState()
	var out []Device
	for _, d := range booted {
		rec, ok := st.Devices[d.UDID]
		if ok && (rec.Dismissed || rec.Fingerprint == fp) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// Install adds pano's CA to the simulator's trusted root store and records
// it. Running apps see it after a reboot of the simulator.
func (m *Manager) Install(ctx context.Context, d Device) error {
	fp, err := m.fingerprint()
	if err != nil {
		return err
	}
	if _, err := m.run.Run(ctx, m.binary, "simctl", "keychain", d.UDID, "add-root-cert", m.certPath); err != nil {
		return fmt.Errorf("simulator: install into %s: %w", d.Label(), err)
	}
	st := m.readState()
	st.Devices[d.UDID] = deviceState{Name: d.Name, Fingerprint: fp}
	return m.writeState(st)
}

// Reboot shuts the simulator down and boots it again so apps pick up the
// freshly trusted certificate.
func (m *Manager) Reboot(ctx context.Context, d Device) error {
	if _, err := m.run.Run(ctx, m.binary, "simctl", "shutdown", d.UDID); err != nil {
		return fmt.Errorf("simulator: shutdown %s: %w", d.Label(), err)
	}
	if _, err := m.run.Run(ctx, m.binary, "simctl", "boot", d.UDID); err != nil {
		return fmt.Errorf("simulator: boot %s: %w", d.Label(), err)
	}
	return nil
}

// Dismiss records that these simulators must not be suggested again.
func (m *Manager) Dismiss(devs []Device) error {
	st := m.readState()
	for _, d := range devs {
		rec := st.Devices[d.UDID]
		rec.Name, rec.Dismissed = d.Name, true
		st.Devices[d.UDID] = rec
	}
	return m.writeState(st)
}

// fingerprint identifies the current root CA, so a rotated CA suggests again.
func (m *Manager) fingerprint() (string, error) {
	b, err := os.ReadFile(m.certPath)
	if err != nil {
		return "", fmt.Errorf("simulator: read CA: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// state is the on-disk record.
type state struct {
	Version int                    `json:"version"`
	Devices map[string]deviceState `json:"devices"`
}

type deviceState struct {
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint,omitempty"` // CA installed into it
	Dismissed   bool   `json:"dismissed,omitempty"`
}

func (m *Manager) readState() state {
	st := state{Version: 1, Devices: map[string]deviceState{}}
	b, err := os.ReadFile(m.statePath)
	if err != nil {
		return st
	}
	var read state
	if json.Unmarshal(b, &read) == nil && read.Devices != nil {
		st = read
		st.Version = 1
	}
	return st
}

func (m *Manager) writeState(st state) error {
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return fmt.Errorf("simulator: %w", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("simulator: encode state: %w", err)
	}
	if err := config.WriteAtomic(m.statePath, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("simulator: %w", err)
	}
	return nil
}

// parseRuntime turns "com.apple.CoreSimulator.SimRuntime.iOS-26-4" into
// "iOS 26.4".
func parseRuntime(id string) string {
	const prefix = "com.apple.CoreSimulator.SimRuntime."
	s := strings.TrimPrefix(id, prefix)
	if s == id {
		return ""
	}
	parts := strings.SplitN(s, "-", 2)
	if len(parts) == 2 {
		return parts[0] + " " + strings.ReplaceAll(parts[1], "-", ".")
	}
	return parts[0]
}

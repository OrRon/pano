//go:build darwin

package ca

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Keychain uses /usr/bin/security to manage trust in the login keychain (or
// the System keychain when system=true, which requires sudo).
type Keychain struct{}

// NewTrustStore returns the macOS keychain trust store.
func NewTrustStore() TrustStore { return Keychain{} }

func loginKeychain() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db")
}

// trustPolicies limits what the root may vouch for: TLS servers and plain
// X.509 chains. Without "-p" macOS would also trust it for code signing,
// S/MIME, software updates and package signing — none of which pano needs and
// all of which a leaked key could then abuse. (mkcert restricts the same way.)
var trustPolicies = []string{"-p", "ssl", "-p", "basic"}

// Install adds the certificate as a trusted root for TLS only. macOS shows a
// GUI password prompt; this cannot be suppressed from the command line.
func (Keychain) Install(ctx context.Context, certPath, _ string, system bool) error {
	var cmd *exec.Cmd
	if system {
		args := append([]string{"security", "add-trusted-cert", "-d", "-r", "trustRoot"}, trustPolicies...)
		cmd = exec.CommandContext(ctx, "sudo", append(args, "-k", "/Library/Keychains/System.keychain", certPath)...)
	} else {
		args := append([]string{"add-trusted-cert", "-r", "trustRoot"}, trustPolicies...)
		cmd = exec.CommandContext(ctx, "security", append(args, "-k", loginKeychain(), certPath)...)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security add-trusted-cert: %w", err)
	}
	return nil
}

// Uninstall removes every certificate with the given subject from the login
// keychain, then sweeps roots left behind by earlier rotations (their CN
// shares SubjectPrefix; "-c" matches by substring).
func (Keychain) Uninstall(ctx context.Context, _ string, subject string) error {
	if n, err := deleteBySubject(ctx, subject); err != nil && n == 0 {
		return err
	}
	_, _ = deleteBySubject(ctx, SubjectPrefix) // best effort: stale rotated roots
	return nil
}

// deleteBySubject deletes matching certificates until none are left (there
// may be duplicates from repeated installs) and returns how many it removed.
func deleteBySubject(ctx context.Context, subject string) (int, error) {
	for i := 0; i < 10; i++ {
		cmd := exec.CommandContext(ctx, "security", "delete-certificate", "-c", subject, "-t", loginKeychain())
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if i == 0 {
				return 0, fmt.Errorf("security delete-certificate: %w", err)
			}
			return i, nil
		}
	}
	return 10, nil
}

// Status reports whether the certificate is present and trusted.
func (Keychain) Status(ctx context.Context, certPath, subject string) TrustStatus {
	st := TrustStatus{Supported: true}
	find := exec.CommandContext(ctx, "security", "find-certificate", "-c", subject, loginKeychain())
	if err := find.Run(); err != nil {
		st.Detail = "not present in login keychain"
		return st
	}
	verify := exec.CommandContext(ctx, "security", "verify-cert", "-c", certPath, "-L", "-l")
	out, err := verify.CombinedOutput()
	if err != nil {
		st.Detail = "present but not trusted: " + strings.TrimSpace(string(out))
		return st
	}
	st.Installed = true
	st.Detail = "trusted (login keychain)"
	return st
}

// ManualInstructions describes the GUI path.
func (Keychain) ManualInstructions(certPath string) string {
	return fmt.Sprintf(`Open Keychain Access, drag %s into the "login" keychain, double-click it,
expand Trust and set "When using this certificate" to "Always Trust".
Firefox: about:config → security.enterprise_roots.enabled = true.`, certPath)
}

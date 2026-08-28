// Package ca is pano's local certificate authority: it generates a root CA on
// first run, mints per-host leaf certificates on demand for TLS interception,
// and (on macOS) installs/uninstalls trust in the login keychain.
package ca

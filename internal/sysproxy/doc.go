// Package sysproxy toggles the operating system's HTTP/HTTPS proxy so that
// every application on the machine sends its traffic through pano.
//
// On macOS the package drives /usr/sbin/networksetup. Before it changes
// anything it snapshots the proxy configuration of every enabled network
// service to a small JSON state file; Disable (and RestoreStale after a crash)
// put the exact previous settings back and delete the file. The presence of
// the state file is therefore the single source of truth for "pano owns the
// system proxy right now".
//
// On other operating systems Manager.Supported reports false and Enable
// returns an error pointing at the per-process alternatives (pano run --, or
// the HTTP_PROXY/HTTPS_PROXY environment variables).
package sysproxy

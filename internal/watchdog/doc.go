// Package watchdog restores the system proxy settings if the pano daemon dies
// without cleaning up after itself.
//
// When the daemon enables the system proxy it spawns a detached watchdog
// process (Spawn) that runs the hidden `pano _watchdog` subcommand. That
// process blocks until the daemon exits (Run / WaitExit) and then calls
// sysproxy.Manager.RestoreStale, which puts the previous settings back if the
// state file still exists — a clean shutdown deletes the file first, so in
// that case the watchdog simply exits.
//
// On macOS the exit is observed with a kqueue EVFILT_PROC/NOTE_EXIT filter;
// other platforms poll the process every 500ms.
package watchdog

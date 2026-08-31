package cli

import (
	"context"
	"errors"
	"time"

	"github.com/orron/pano/internal/simulator"
)

// sim returns the iOS Simulator manager for this pano home.
func (a *App) sim() *simulator.Manager {
	return simulator.New(a.paths.CACert(), a.paths.SimulatorState())
}

// caSimulatorInstall is `pano ca install --simulator`: trust pano's CA inside
// every booted iOS Simulator. The Simulator shares the Mac's network stack,
// so `pano on` already routes its traffic — but it keeps its own trust store
// (the Mac keychain does not apply), so HTTPS stays opaque until the CA is
// added there via `xcrun simctl keychain <udid> add-root-cert`. No password,
// no Settings toggle; a restart of the simulator makes running apps see it.
func (a *App) caSimulatorInstall(ctx context.Context) error {
	if _, err := a.loadCA(); err != nil { // make sure a CA exists to install
		return err
	}
	sim := a.sim()
	if !sim.Supported() {
		return errors.New("xcrun not found — iOS Simulator support needs Xcode or its Command Line Tools")
	}
	devs, err := sim.Booted(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		a.printf("%s no simulator is running\n  boot one (Xcode ▸ Open Developer Tool ▸ Simulator), then re-run %s\n",
			a.c(yellow, "○"), a.c(bold, "pano ca install --simulator"))
		return nil
	}
	for _, d := range devs {
		if err := sim.Install(ctx, d); err != nil {
			return err
		}
		a.printf("%s certificate trusted in %s\n", a.c(green, "✓"), d.Label())
	}
	if a.confirm("Restart the simulator now so running apps pick up the trust?") {
		for _, d := range devs {
			if err := sim.Reboot(ctx, d); err != nil {
				return err
			}
			a.printf("%s %s restarting\n", a.c(green, "✓"), d.Label())
		}
	} else {
		a.printf("  restart it later from the Simulator app (Device ▸ Restart) to apply\n")
	}
	return nil
}

// suggestSimulators prints a hint when a booted iOS Simulator lacks pano's
// certificate (used by `pano on -b`; the TUI shows its own modal). It is
// best-effort and bounded: detection failures stay silent, pano never
// installs on its own.
func (a *App) suggestSimulators(ctx context.Context) {
	if a.jsonOut || a.quiet {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	devs := a.sim().Suggest(ctx)
	if len(devs) == 0 {
		return
	}
	for _, d := range devs {
		a.printf("             %s %s simulator is running — its HTTPS is not decrypted yet\n", a.c(yellow, "▸"), d.Label())
	}
	a.printf("               install pano's certificate with %s\n", a.c(bold, "pano ca install --simulator"))
}

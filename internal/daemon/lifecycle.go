package daemon

import (
	"context"
	"sync"

	"github.com/orron/pano/internal/api"
)

// lifecycle tracks attached terminal UIs and which of them, if any, owns
// the daemon (ADR 0009). `pano on` without --background opens the UI as the
// owner: when that UI's control connection drops — q, ctrl-c, a closed
// terminal window, a kill — the daemon runs `pano off` on itself. A UI
// that only attaches (`pano ui` on a running daemon) never stops anything;
// `pano off` does.
type lifecycle struct {
	mu    sync.Mutex
	uis   int
	owner int // token of the owning attachment; 0 = background mode
	next  int
}

// Attach implements control.Backend.
func (d *Daemon) Attach(own bool) func() {
	d.life.mu.Lock()
	d.life.next++
	tok := d.life.next
	d.life.uis++
	if own {
		d.life.owner = tok
	}
	d.life.mu.Unlock()
	d.log.Info("ui attached", "owner", own, "uis", d.life.count())

	return func() {
		d.life.mu.Lock()
		d.life.uis--
		owned := d.life.owner == tok
		if owned {
			d.life.owner = 0
		}
		d.life.mu.Unlock()
		if !owned {
			d.log.Info("ui detached", "uis", d.life.count())
			return
		}
		if d.ctx != nil && d.ctx.Err() != nil {
			return // already stopping (the ui asked for off before leaving)
		}
		d.log.Info("owning ui closed — turning pano off")
		_ = d.Off(context.Background())
	}
}

// Disown implements control.Backend.
func (d *Daemon) Disown(context.Context) {
	d.life.mu.Lock()
	d.life.owner = 0
	d.life.mu.Unlock()
}

// Off implements control.Backend: restore the system proxy, then stop.
// Closing the proxy listeners on shutdown takes the mobile listener down
// with them.
func (d *Daemon) Off(ctx context.Context) error {
	if d.sysp.Supported() {
		if err := d.sysp.Disable(ctx); err != nil {
			d.log.Warn("restore system proxy", "err", err)
		}
	}
	return d.Shutdown(ctx)
}

// Lifecycle reports the current mode for status surfaces.
func (d *Daemon) Lifecycle() api.Lifecycle {
	d.life.mu.Lock()
	defer d.life.mu.Unlock()
	l := api.Lifecycle{Mode: "background", UIs: d.life.uis}
	if d.life.owner != 0 {
		l.Mode = "app"
	}
	return l
}

func (l *lifecycle) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.uis
}

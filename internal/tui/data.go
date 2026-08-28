package tui

import (
	"context"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/store"
)

// Messages exchanged between commands and the model.
type (
	statusMsg  struct{ st api.Status }
	flowsMsg   struct{ list api.FlowList }
	eventMsg   struct{ ev flow.Event }
	eventsDone struct{}
	detailMsg  struct {
		id  flow.ID
		q   api.FlowQuery
		det api.FlowDetail
		err error
	}
	explainMsg struct {
		id  flow.ID
		res api.ExplainResult
		err error
	}
	diffMsg struct {
		a, b flow.ID
		res  api.DiffResult
		err  error
	}
	rulesMsg struct {
		rules []api.Rule
		held  []api.Held
	}
	actionMsg struct {
		text string
		err  error
	}
	tickMsg time.Time
	errMsg  struct{ err error }
)

// fetchStatus polls the daemon.
func fetchStatus(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := c.Status(ctx)
		if err != nil {
			return errMsg{err}
		}
		return statusMsg{st}
	}
}

// fetchFlows loads the initial page.
func fetchFlows(c *client.Client, f api.FlowFilter) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		f.Limit = 200
		list, err := c.Flows(ctx, f)
		if err != nil {
			return errMsg{err}
		}
		return flowsMsg{list}
	}
}

// subscribeEvents opens the SSE stream and forwards events one at a time via
// the returned channel-pumping command.
type eventSub struct {
	ch     <-chan flow.Event
	cancel context.CancelFunc
}

func openEvents(c *client.Client) (*eventSub, error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Events(ctx, nil, "")
	if err != nil {
		cancel()
		return nil, err
	}
	return &eventSub{ch: ch, cancel: cancel}, nil
}

func (s *eventSub) next() tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-s.ch
		if !ok {
			return eventsDone{}
		}
		return eventMsg{ev}
	}
}

func fetchDetail(c *client.Client, id flow.ID, q api.FlowQuery) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		det, err := c.Flow(ctx, id, q)
		return detailMsg{id: id, q: q, det: det, err: err}
	}
}

func fetchExplain(c *client.Client, id flow.ID) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res, err := c.Explain(ctx, id, api.ExplainRequest{})
		return explainMsg{id: id, res: res, err: err}
	}
}

func fetchDiff(c *client.Client, a, b flow.ID) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		res, err := c.Diff(ctx, api.DiffRequest{A: a, B: b, Part: "both"})
		return diffMsg{a: a, b: b, res: res, err: err}
	}
}

func fetchRules(c *client.Client) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		rules, _ := c.Rules(ctx)
		held, _ := c.Held(ctx)
		return rulesMsg{rules: rules, held: held}
	}
}

func doReplay(c *client.Client, id flow.ID) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		r, err := c.Replay(ctx, id, api.ReplayRequest{})
		if err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{text: "replayed " + id.Short() + " → " + r.Short + " (" + itoa(r.Status) + ", " + r.Duration + ")"}
	}
}

func doResume(c *client.Client, id flow.ID, action string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Resume(ctx, id, api.ResumeRequest{Action: action}); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{text: action + " " + id.Short()}
	}
}

func doToggleRule(c *client.Client, r api.Rule) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		en := r.Enabled != nil && !*r.Enabled
		if _, err := c.UpdateRule(ctx, r.ID, api.RulePatch{Enabled: &en}); err != nil {
			return actionMsg{err: err}
		}
		if en {
			return actionMsg{text: "enabled " + r.ID}
		}
		return actionMsg{text: "disabled " + r.ID}
	}
}

func doRemoveRule(c *client.Client, id string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.RemoveRule(ctx, id); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{text: "removed " + id}
	}
}

func doCapture(c *client.Client, on bool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		action := "stop"
		if on {
			action = "start"
		}
		st, err := c.Capture(ctx, api.CaptureRequest{Action: action})
		if err != nil {
			return actionMsg{err: err}
		}
		return statusMsg{st}
	}
}

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// flowTable is the in-memory list the UI renders: newest first, bounded.
type flowTable struct {
	rows  []api.FlowRow
	byID  map[flow.ID]int
	max   int
	fresh map[flow.ID]time.Time // arrival time, for the entrance highlight
}

func newFlowTable(max int) *flowTable {
	return &flowTable{byID: map[flow.ID]int{}, max: max, fresh: map[flow.ID]time.Time{}}
}

func (t *flowTable) reset(rows []api.FlowRow) {
	t.rows = append([]api.FlowRow(nil), rows...)
	t.reindex()
}

func (t *flowTable) reindex() {
	t.byID = make(map[flow.ID]int, len(t.rows))
	for i, r := range t.rows {
		t.byID[r.ID] = i
	}
}

// upsert inserts or replaces a row, keeping newest-first order.
func (t *flowTable) upsert(f *flow.Flow, now time.Time) {
	row := store.Row(f)
	if i, ok := t.byID[row.ID]; ok {
		t.rows[i] = row
		return
	}
	t.fresh[row.ID] = now
	// Insert in ID order (descending). Most arrivals are the newest.
	pos := sort.Search(len(t.rows), func(i int) bool { return t.rows[i].ID < row.ID })
	t.rows = append(t.rows, api.FlowRow{})
	copy(t.rows[pos+1:], t.rows[pos:])
	t.rows[pos] = row
	if len(t.rows) > t.max {
		t.rows = t.rows[:t.max]
	}
	t.reindex()
}

func (t *flowTable) get(id flow.ID) (api.FlowRow, bool) {
	i, ok := t.byID[id]
	if !ok {
		return api.FlowRow{}, false
	}
	return t.rows[i], true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

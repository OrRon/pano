package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/orron/pano/internal/api"
	"github.com/orron/pano/internal/client"
	"github.com/orron/pano/internal/flow"
	"github.com/orron/pano/internal/store"
	"github.com/orron/pano/internal/update"
)

// mode is the UI's focus/interaction state.
type mode int

const (
	modeList mode = iota
	modeDetail
	modeFilter
	modeRules
	modeHeld
	modeDecrypt
	modeMobile
	modeActions
	modeHelp
	modeQuit
)

// pane is what the detail viewport is showing.
type pane int

const (
	paneBody pane = iota
	paneExplain
	paneDiff
)

// Model is the Bubble Tea model for pano ui.
type Model struct {
	c       *client.Client
	version string

	updateFn func() *update.Info // release check, polled from the tick
	update   *update.Info        // newer release, once known

	width, height int
	dark          bool
	ready         bool

	mode     mode
	prevMode mode

	status  api.Status
	table   *flowTable
	visible []int // indexes into table.rows after filtering
	cursor  int   // index into visible
	offset  int   // first visible row index shown
	follow  bool  // stick to newest
	paused  bool  // local pause of list updates (capture continues)

	filter    api.FlowFilter
	filterRaw string
	matcher   rowMatcher           // filter compiled for local rows
	hits      map[flow.ID]struct{} // ids the daemon returned for filter; nil until it answers
	watermark flow.ID              // newest id when the filter was set; newer rows are live arrivals
	flowsGen  int                  // bumped per reloadFlows; stale answers are dropped
	input     textinput.Model

	// Detail
	detailID  flow.ID
	detailQ   api.FlowQuery
	detail    api.FlowDetail
	detailErr error
	loading   bool
	pane      pane
	explain   api.ExplainResult
	diff      api.DiffResult
	marked    flow.ID // first flow marked for diff
	vp        viewport.Model

	// Lifecycle (ADR 0009)
	own    bool       // this UI owns the daemon: closing it turns pano off
	exit   Exit       // why Run returned
	quitIx int        // highlighted item of the quit overlay
	attach *attachSub // held open for the daemon to notice us leaving

	// Drawers
	rules         []api.Rule
	held          []api.Held
	drawerIx      int
	decryptTarget string // list the + input adds to (only|never)
	actionIx      int    // highlighted item of the actions menu

	sub     *eventSub
	th      *Theme
	unseen  int
	tab     tab
	toast   string
	toastAt time.Time
	err     error
	now     time.Time
	frame   int
}

// New creates the model.
func New(c *client.Client, version string) *Model {
	in := textinput.New()
	in.Prompt = ""
	in.Placeholder = "host=api.openai.com status=!2xx since=15m  ·  free text searches url/headers"
	m := &Model{
		c: c, version: version, table: newFlowTable(5000), follow: true, input: in, dark: true,
		vp: viewport.New(),
	}
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{fetchStatus(m.c), m.reloadFlows(), fetchRules(m.c), tick(), tea.RequestBackgroundColor}
	if sub, err := openEvents(m.c); err == nil {
		m.sub = sub
		cmds = append(cmds, sub.next())
	} else {
		m.err = err
	}
	if at, err := openAttach(m.c, m.own); err == nil {
		m.attach = at
		cmds = append(cmds, at.wait())
	} else {
		m.err = err
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layoutViewport()
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		return m, nil
	case tickMsg:
		m.now = time.Time(msg)
		m.frame++
		cmds := []tea.Cmd{tick()}
		if m.update == nil && m.updateFn != nil && m.frame%4 == 0 {
			if info := m.updateFn(); info != nil {
				m.updateFn = nil
				if info.Available {
					m.update = info
				}
			}
		}
		if m.frame%20 == 0 {
			cmds = append(cmds, fetchStatus(m.c))
		}
		if m.frame%12 == 0 && (m.mode == modeRules || m.mode == modeHeld || len(m.held) > 0) {
			cmds = append(cmds, fetchRules(m.c))
		}
		if m.frame%12 == 0 && (m.mode == modeDecrypt || m.mode == modeMobile) {
			cmds = append(cmds, fetchStatus(m.c))
		}
		return m, tea.Batch(cmds...)
	case statusMsg:
		m.status = msg.st
		m.err = nil
		return m, nil
	case flowsMsg:
		if msg.gen != m.flowsGen {
			// Answer to a filter that is no longer active (e.g. the slower
			// full-text query landing after esc cleared it). Dropping it is
			// what keeps the list from shrinking to a stale subset.
			return m, nil
		}
		m.table.merge(msg.list.Flows)
		m.hits = nil
		if msg.filtered {
			m.hits = make(map[flow.ID]struct{}, len(msg.list.Flows))
			for _, r := range msg.list.Flows {
				m.hits[r.ID] = struct{}{}
			}
		}
		m.applyFilter()
		if m.follow {
			m.cursor, m.offset = 0, 0
		}
		return m, nil
	case eventMsg:
		cmd := tea.Cmd(nil)
		if m.sub != nil {
			cmd = m.sub.next()
		}
		if msg.ev.Flow == nil || m.paused {
			return m, cmd
		}
		switch msg.ev.Type {
		case flow.EvStarted, flow.EvHeaders, flow.EvDone, flow.EvHeld:
			_, existed := m.table.get(msg.ev.Flow.ID)
			m.table.upsert(msg.ev.Flow, time.Now())
			m.applyFilter()
			if m.follow && m.mode == modeList {
				m.cursor, m.offset = 0, 0
			} else if !m.follow {
				if !existed {
					m.unseen++
				}
				m.keepCursorOn(msg.ev.Flow.ID)
			}
			if msg.ev.Flow.ID == m.detailID && msg.ev.Type == flow.EvDone {
				return m, tea.Batch(cmd, fetchDetail(m.c, m.detailID, m.detailQ))
			}
			if msg.ev.Type == flow.EvHeld {
				return m, tea.Batch(cmd, fetchRules(m.c))
			}
		case flow.EvWS, flow.EvDropped:
		}
		return m, cmd
	case eventsDone:
		m.sub = nil
		m.err = client.ErrNotRunning
		return m, nil
	case attachLostMsg:
		// The daemon closed our attachment: it is stopping (pano off in
		// another terminal, or the off we asked for) — leave with it.
		if m.exit == ExitInterrupt {
			m.exit = ExitGone
		}
		return m, tea.Quit
	case offDoneMsg:
		if msg.err != nil {
			m.mode = m.prevMode
			m.showToast("✗ could not turn pano off: " + msg.err.Error())
			return m, nil
		}
		m.exit = ExitOff
		return m, tea.Quit
	case disownDoneMsg:
		if msg.err != nil {
			m.mode = m.prevMode
			m.showToast("✗ " + msg.err.Error())
			return m, nil
		}
		m.exit = ExitDetach
		return m, tea.Quit
	case detailMsg:
		if msg.id != m.detailID {
			return m, nil
		}
		m.loading = false
		m.detail, m.detailErr = msg.det, msg.err
		m.setPaneContent()
		return m, nil
	case explainMsg:
		if msg.id != m.detailID {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			// Explain is an entry point into the same detail view; when it
			// isn't available for this flow, land on Summary instead of an
			// empty pane.
			m.showToast("✗ explain: " + msg.err.Error())
			if m.pane == paneExplain {
				return m.setTab(tabSummary)
			}
			return m, nil
		}
		m.explain = msg.res
		m.pane, m.tab = paneExplain, tabExplain
		m.setPaneContent()
		return m, nil
	case diffMsg:
		m.loading = false
		if msg.err != nil {
			m.showToast("diff failed: " + msg.err.Error())
		} else {
			m.diff = msg.res
			m.pane, m.tab = paneDiff, tabDiff
			m.mode = modeDetail
			if r, ok := m.selected(); ok {
				m.detailID = r.ID
			}
		}
		m.setPaneContent()
		return m, nil
	case rulesMsg:
		m.rules, m.held = msg.rules, msg.held
		if m.drawerIx >= m.drawerLen() {
			m.drawerIx = max(0, m.drawerLen()-1)
		}
		return m, nil
	case actionMsg:
		if msg.err != nil {
			m.showToast("✗ " + msg.err.Error())
		} else {
			m.showToast("✓ " + msg.text)
		}
		return m, tea.Batch(fetchRules(m.c), fetchStatus(m.c))
	case errMsg:
		m.err = msg.err
		return m, nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseWheelMsg:
		if m.mode == modeDetail {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		switch msg.Button {
		case tea.MouseWheelUp:
			m.moveCursor(-3)
		case tea.MouseWheelDown:
			m.moveCursor(3)
		default:
		}
		return m, nil
	}
	if m.mode == modeFilter {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if key == "ctrl+c" {
		m.exit = ExitInterrupt
		return m, tea.Quit
	}
	switch m.mode {
	case modeQuit:
		return m.handleQuitKey(key)
	case modeFilter:
		return m.handleFilterKey(k)
	case modeHelp:
		if key == "esc" || key == "?" || key == "q" {
			m.mode = m.prevMode
		}
		return m, nil
	case modeActions:
		return m.handleActionsKey(key)
	case modeRules, modeHeld, modeDecrypt, modeMobile:
		return m.handleDrawerKey(key)
	case modeDetail:
		return m.handleDetailKey(k)
	default:
		return m.handleListKey(key)
	}
}

func (m *Model) handleListKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		return m.openQuit()
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "g", "home":
		m.cursor, m.offset = 0, 0
		m.follow = true
		m.unseen = 0
	case "G", "end":
		m.cursor = max(0, len(m.visible)-1)
		m.ensureVisible()
	case "pgdown", "ctrl+d":
		m.moveCursor(m.listHeight())
	case "pgup", "ctrl+u":
		m.moveCursor(-m.listHeight())
	case "enter", "l", "right":
		return m.openDetail(paneBody)
	case "x", "e":
		return m.openDetail(paneExplain)
	case "m":
		if r, ok := m.selected(); ok {
			if m.marked == r.ID {
				m.marked = 0
				m.showToast("unmarked")
			} else {
				m.marked = r.ID
				m.showToast("marked " + r.Short + " — select another and press d to diff")
			}
		}
	case "d":
		if r, ok := m.selected(); ok && m.marked != 0 && m.marked != r.ID {
			m.loading = true
			return m, fetchDiff(m.c, m.marked, r.ID)
		}
		m.showToast("mark a flow with m first")
	case "R":
		if r, ok := m.selected(); ok {
			m.showToast("replaying " + r.Short + "…")
			return m, doReplay(m.c, r.ID)
		}
	case "/":
		m.prevMode = modeList
		m.mode = modeFilter
		m.input.SetValue(m.filterRaw)
		return m, m.input.Focus()
	case "esc":
		if m.filterRaw != "" {
			m.setFilter("")
			return m, m.reloadFlows()
		}
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.cursor, m.offset = 0, 0
			m.unseen = 0
		}
	case "space":
		m.paused = !m.paused
	case "c":
		on := !m.status.Capturing
		return m, doCapture(m.c, on)
	case "r":
		m.prevMode = modeList
		m.mode = modeRules
		m.drawerIx = 0
		return m, fetchRules(m.c)
	case "h":
		m.prevMode = modeList
		m.mode = modeHeld
		m.drawerIx = 0
		return m, fetchRules(m.c)
	case "D":
		m.prevMode = modeList
		m.mode = modeDecrypt
		m.drawerIx = 0
		return m, fetchStatus(m.c)
	case "M":
		m.prevMode = modeList
		m.mode = modeMobile
		m.drawerIx = 0
		return m, fetchStatus(m.c)
	case "o":
		return m.openActions(modeList)
	case "n":
		if r, ok := m.selected(); ok && r.Kind == flow.KindTunnel {
			return m, doDecrypt(m.c, api.DecryptChange{AddNever: []string{r.Host}}, "never + "+r.Host)
		}
	case "?":
		m.prevMode = m.mode
		m.mode = modeHelp
	}
	return m, nil
}

func (m *Model) handleDetailKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	switch key {
	case "esc", "q", "h", "left":
		m.closeDetail()
		return m, nil
	case "j", "down":
		m.vp.ScrollDown(1)
	case "k", "up":
		m.vp.ScrollUp(1)
	case "pgdown", "ctrl+d", "space":
		m.vp.ScrollDown(m.vp.Height() / 2)
	case "pgup", "ctrl+u":
		m.vp.ScrollUp(m.vp.Height() / 2)
	case "g":
		m.vp.GotoTop()
	case "G":
		m.vp.GotoBottom()
	case "J", "ctrl+n":
		m.moveCursor(1)
		return m.openDetail(m.pane)
	case "K", "ctrl+p":
		m.moveCursor(-1)
		return m.openDetail(m.pane)
	case "1":
		return m.setTab(tabSummary)
	case "2":
		return m.setTab(tabRequest)
	case "3":
		return m.setTab(tabResponse)
	case "4":
		return m.setTab(tabExplain)
	case "5":
		return m.setTab(tabDiff)
	case "tab":
		return m.setTab(m.nextTab(1))
	case "shift+tab":
		return m.setTab(m.nextTab(-1))
	case "v":
		if m.tab == tabRequest || m.tab == tabResponse {
			return m.setView(nextView(m.detailQ.View))
		}
	case "x", "e":
		// same view as Enter, different entry point: jump to the Explain tab
		return m.setTab(tabExplain)
	case "S":
		m.detailQ.RevealSecrets = !m.detailQ.RevealSecrets
		return m.refetchDetail()
	case "H":
		hdr := m.detailQ.Headers != nil && !*m.detailQ.Headers
		m.detailQ.Headers = &hdr
		return m.refetchDetail()
	case "R":
		m.showToast("replaying " + m.detailID.Short() + "…")
		return m, doReplay(m.c, m.detailID)
	case "m":
		m.marked = m.detailID
		m.showToast("marked " + m.detailID.Short())
	case "d":
		if r, ok := m.selected(); ok && m.marked != 0 && m.marked != r.ID {
			m.loading = true
			return m, fetchDiff(m.c, m.marked, r.ID)
		}
	case "n":
		if r, ok := m.selected(); ok && r.Kind == flow.KindTunnel {
			return m, doDecrypt(m.c, api.DecryptChange{AddNever: []string{r.Host}}, "never + "+r.Host)
		}
	case "o":
		return m.openActions(modeDetail)
	case "/":
		m.prevMode = modeDetail
		m.mode = modeFilter
		m.input.SetValue(m.detailQ.Path)
		m.input.Placeholder = "JSON path, e.g. choices.0.message.content  (empty to clear)"
		return m, m.input.Focus()
	case "?":
		m.prevMode = modeDetail
		m.mode = modeHelp
	}
	return m, nil
}

func (m *Model) handleFilterKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = m.prevMode
		m.input.Blur()
		m.input.Placeholder = "host=api.openai.com status=!2xx since=15m  ·  free text searches url/headers"
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.input.Value())
		m.input.Blur()
		if m.prevMode == modeDetail {
			m.mode = modeDetail
			m.detailQ.Path = val
			m.input.Placeholder = "host=api.openai.com status=!2xx since=15m  ·  free text searches url/headers"
			return m.refetchDetail()
		}
		if m.prevMode == modeDecrypt {
			m.mode = modeDecrypt
			m.input.Placeholder = "host=api.openai.com status=!2xx since=15m  ·  free text searches url/headers"
			if val == "" {
				return m, nil
			}
			ch := api.DecryptChange{AddOnly: []string{val}}
			if m.decryptTarget == secNever {
				ch = api.DecryptChange{AddNever: []string{val}}
			}
			return m, doDecrypt(m.c, ch, m.decryptTarget+" + "+val)
		}
		m.mode = modeList
		m.setFilter(val)
		m.cursor, m.offset = 0, 0
		return m, m.reloadFlows()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *Model) handleDrawerKey(key string) (tea.Model, tea.Cmd) {
	if m.mode == modeDecrypt {
		if mm, cmd, handled := m.handleDecryptKey(key); handled {
			return mm, cmd
		}
	}
	if m.mode == modeMobile {
		if mm, cmd, handled := m.handleMobileKey(key); handled {
			return mm, cmd
		}
	}
	switch key {
	case "esc", "q", "r", "h", "D", "M":
		m.mode = modeList
	case "j", "down":
		if m.drawerIx < m.drawerLen()-1 {
			m.drawerIx++
		}
	case "k", "up":
		if m.drawerIx > 0 {
			m.drawerIx--
		}
	case "tab":
		switch m.mode {
		case modeRules:
			m.mode = modeHeld
		case modeHeld:
			m.mode = modeDecrypt
		case modeDecrypt:
			m.mode = modeMobile
		default:
			m.mode = modeRules
		}
		m.drawerIx = 0
	case "enter", "space":
		if m.mode == modeRules && m.drawerIx < len(m.rules) {
			return m, doToggleRule(m.c, m.rules[m.drawerIx])
		}
		if m.mode == modeHeld && m.drawerIx < len(m.held) {
			return m, doResume(m.c, m.held[m.drawerIx].ID, "resume")
		}
	case "x", "delete", "backspace":
		if m.mode == modeRules && m.drawerIx < len(m.rules) {
			return m, doRemoveRule(m.c, m.rules[m.drawerIx].ID)
		}
		if m.mode == modeHeld && m.drawerIx < len(m.held) {
			return m, doResume(m.c, m.held[m.drawerIx].ID, "drop")
		}
	case "?":
		m.prevMode = m.mode
		m.mode = modeHelp
	}
	return m, nil
}

func (m *Model) drawerLen() int {
	switch m.mode {
	case modeHeld:
		return len(m.held)
	case modeDecrypt:
		return len(m.decryptItems())
	case modeMobile:
		return len(m.status.Mobile.Devices)
	default:
		return len(m.rules)
	}
}

// --- selection and navigation ---

func (m *Model) selected() (api.FlowRow, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return api.FlowRow{}, false
	}
	return m.table.rows[m.visible[m.cursor]], true
}

func (m *Model) moveCursor(delta int) {
	if len(m.visible) == 0 {
		m.cursor = 0
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	m.follow = m.cursor == 0 && m.follow
	if delta != 0 && m.cursor != 0 {
		m.follow = false
	}
	m.ensureVisible()
}

func (m *Model) ensureVisible() {
	h := m.listHeight()
	if h <= 0 {
		return
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

func (m *Model) keepCursorOn(id flow.ID) {
	if r, ok := m.selected(); ok && r.ID != id {
		for i, ix := range m.visible {
			if m.table.rows[ix].ID == r.ID {
				m.cursor = i
				break
			}
		}
		m.ensureVisible()
	}
}

// setFilter installs raw as the active filter over the full local table and
// records the watermark: rows newer than it arrive live and are judged by the
// local matcher alone; older rows must also be in the daemon's answer once it
// lands (see applyFilter), which covers criteria the row cannot decide
// (tag, rule, session, body text). An empty raw clears the filter.
func (m *Model) setFilter(raw string) {
	m.filterRaw = strings.TrimSpace(raw)
	m.filter = parseFilter(m.filterRaw)
	m.matcher = compileRowFilter(m.filter, time.Now())
	m.hits = nil
	m.watermark = 0
	if len(m.table.rows) > 0 {
		m.watermark = m.table.rows[0].ID
	}
	m.applyFilter()
}

// reloadFlows asks the daemon for the current filter's page. Every request
// carries a generation so an answer to an earlier filter is dropped rather
// than applied over a newer one.
func (m *Model) reloadFlows() tea.Cmd {
	m.flowsGen++
	return fetchFlows(m.c, m.filter, m.flowsGen, m.filterRaw != "")
}

func (m *Model) applyFilter() {
	m.visible = m.visible[:0]
	if m.filterRaw == "" {
		for i := range m.table.rows {
			m.visible = append(m.visible, i)
		}
		return
	}
	for i, r := range m.table.rows {
		if !m.matcher.match(r) {
			continue
		}
		if m.hits != nil && r.ID <= m.watermark {
			if _, ok := m.hits[r.ID]; !ok {
				continue
			}
		}
		m.visible = append(m.visible, i)
	}
}

func (m *Model) openDetail(p pane) (tea.Model, tea.Cmd) {
	r, ok := m.selected()
	if !ok {
		return m, nil
	}
	m.mode = modeDetail
	if m.detailID != r.ID {
		m.detailID = r.ID
		m.detail = api.FlowDetail{}
		m.explain = api.ExplainResult{}
		m.detailErr = nil
		m.detailQ.Path = ""
	}
	if m.detailQ.View == "" {
		m.detailQ.View = api.ViewSummary
	}
	if m.detailQ.Part == "" {
		m.detailQ.Part = "both"
	}
	m.loading = true
	if p == paneExplain {
		m.pane, m.tab = paneExplain, tabExplain
		return m, tea.Batch(fetchDetail(m.c, r.ID, m.detailQ), fetchExplain(m.c, r.ID))
	}
	m.pane = paneBody
	if m.tab == tabExplain || m.tab == tabDiff {
		m.tab = tabSummary
		m.detailQ.Part, m.detailQ.View = "both", api.ViewSummary
	}
	return m, fetchDetail(m.c, r.ID, m.detailQ)
}

// closeDetail leaves the detail view and drops the pane entirely, so the
// list gets the full width back; the next Enter/x starts from a clean fetch.
func (m *Model) closeDetail() {
	m.mode = modeList
	m.detailID = 0
	m.detail, m.explain, m.diff = api.FlowDetail{}, api.ExplainResult{}, api.DiffResult{}
	m.detailErr = nil
	m.loading = false
	m.pane, m.tab = paneBody, tabSummary
	m.detailQ.Path = ""
	m.vp.SetContent("")
}

// nextView cycles summary → schema → pretty → raw.
func nextView(v string) string {
	switch v {
	case api.ViewSummary:
		return api.ViewSchema
	case api.ViewSchema:
		return api.ViewPretty
	case api.ViewPretty:
		return api.ViewRaw
	default:
		return api.ViewSummary
	}
}

func (m *Model) nextTab(delta int) tab {
	avail := []tab{tabSummary, tabRequest, tabResponse}
	if r, ok := m.table.get(m.detailID); ok && (store.IsLLMHost(hostOnly(r.Host)) || m.explain.Text != "") {
		avail = append(avail, tabExplain)
	}
	if m.diff.Text != "" {
		avail = append(avail, tabDiff)
	}
	cur := 0
	for i, tb := range avail {
		if tb == m.tab {
			cur = i
		}
	}
	n := (cur + delta + len(avail)) % len(avail)
	return avail[n]
}

// setTab switches the detail tab and fetches what it needs.
func (m *Model) setTab(tb tab) (tea.Model, tea.Cmd) {
	m.tab = tb
	switch tb {
	case tabSummary:
		m.pane = paneBody
		m.detailQ.Part, m.detailQ.View = "both", api.ViewSummary
		return m.refetchDetail()
	case tabRequest:
		m.pane = paneBody
		m.detailQ.Part = "request"
		if m.detailQ.View == api.ViewSummary {
			m.detailQ.View = api.ViewPretty
		}
		return m.refetchDetail()
	case tabResponse:
		m.pane = paneBody
		m.detailQ.Part = "response"
		if m.detailQ.View == api.ViewSummary {
			m.detailQ.View = api.ViewPretty
		}
		return m.refetchDetail()
	case tabExplain:
		m.pane = paneExplain
		if m.explain.Text == "" {
			m.loading = true
			return m, fetchExplain(m.c, m.detailID)
		}
		m.setPaneContent()
		return m, nil
	case tabDiff:
		m.pane = paneDiff
		m.setPaneContent()
		return m, nil
	}
	return m, nil
}

func (m *Model) setView(v string) (tea.Model, tea.Cmd) {
	m.detailQ.View = v
	if v == api.ViewRaw && m.detailQ.MaxBytes == 0 {
		m.detailQ.MaxBytes = 64 << 10
	}
	m.pane = paneBody
	return m.refetchDetail()
}

func (m *Model) refetchDetail() (tea.Model, tea.Cmd) {
	if m.detailID == 0 {
		return m, nil
	}
	m.loading = true
	return m, fetchDetail(m.c, m.detailID, m.detailQ)
}

func (m *Model) showToast(s string) {
	m.toast = s
	m.toastAt = time.Now()
}

func (m *Model) listHeight() int {
	// header(2) + column header(1) + footer(2)
	h := m.height - 5
	if m.filterRaw != "" || m.mode == modeFilter {
		h--
	}
	if h < 1 {
		h = 1
	}
	return h
}

// parseFilter turns "host=api.x status=!2xx free text" into a FlowFilter.
func parseFilter(s string) api.FlowFilter {
	var f api.FlowFilter
	var free []string
	for _, tok := range strings.Fields(s) {
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			k, v, ok = strings.Cut(tok, ":")
		}
		if !ok {
			free = append(free, tok)
			continue
		}
		switch strings.ToLower(k) {
		case "host", "h":
			f.Host = v
		case "path", "p":
			f.Path = v
		case "method", "m":
			f.Method = strings.Split(strings.ToUpper(v), ",")
		case "status", "st":
			f.Status = v
		case "since":
			f.Since = v
		case "until":
			f.Until = v
		case "type", "ct":
			f.ContentType = v
		case "tag":
			f.Tag = v
		case "rule":
			f.Rule = v
		case "state":
			f.State = v
		case "kind":
			f.Kind = v
		case "session":
			f.Session = v
		case "client", "device":
			f.Client = v
		case "err", "errors", "error":
			f.HasError = v == "1" || v == "true" || v == "yes"
		default:
			free = append(free, tok)
		}
	}
	f.Q = strings.Join(free, " ")
	return f
}

// rowMatcher is the subset of a FlowFilter a list row can decide on its own;
// relative times are anchored when the filter is set, like the daemon does.
type rowMatcher struct {
	f       api.FlowFilter
	since   time.Time
	sinceID flow.ID
	until   time.Time
	status  func(int) bool
}

func compileRowFilter(f api.FlowFilter, now time.Time) rowMatcher {
	rm := rowMatcher{f: f, status: store.StatusMatcher(f.Status)}
	if f.Since != "" {
		if t, ok := store.ParseTime(f.Since, now); ok {
			rm.since = t
		} else if id, ok := flow.ParseShort(f.Since); ok {
			rm.sinceID = id
		}
	}
	if f.Until != "" {
		if t, ok := store.ParseTime(f.Until, now); ok {
			rm.until = t
		}
	}
	return rm
}

// match applies the row-level subset of the filter (used for live arrivals
// and while the daemon's answer is pending).
func (rm rowMatcher) match(r api.FlowRow) bool {
	f := rm.f
	if rm.sinceID != 0 && r.ID <= rm.sinceID {
		return false
	}
	if !rm.since.IsZero() && r.Time.Before(rm.since) {
		return false
	}
	if !rm.until.IsZero() && r.Time.After(rm.until) {
		return false
	}
	if f.Host != "" && !globMatch(f.Host, hostOnly(r.Host)) {
		return false
	}
	if f.Path != "" && !strings.HasPrefix(r.Path, f.Path) && !globMatch(f.Path, r.Path) {
		return false
	}
	if len(f.Method) > 0 {
		hit := false
		for _, mm := range f.Method {
			if strings.EqualFold(mm, r.Method) {
				hit = true
			}
		}
		if !hit {
			return false
		}
	}
	if rm.status != nil && !rm.status(r.Status) {
		return false
	}
	if f.Client != "" && !store.MatchClient(f.Client, r.Client) {
		return false
	}
	if f.HasError && r.Error == "" && r.Status < 400 {
		return false
	}
	if f.ContentType != "" && r.Type != f.ContentType {
		return false
	}
	if f.Kind != "" && string(r.Kind) != f.Kind {
		return false
	}
	if f.State != "" && f.State != "all" && string(r.State) != f.State {
		if f.State != "replayed" || !hasFlag(r.Flags, "replay") {
			return false
		}
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		if !strings.Contains(strings.ToLower(r.Host+r.Path+r.Error+strings.Join(r.Flags, " ")), q) {
			return false
		}
	}
	return true
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

func hostOnly(h string) string {
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h, "]") {
		return h[:i]
	}
	return h
}

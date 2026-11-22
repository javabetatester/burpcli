// Package tui is the Bubble Tea terminal adapter over the proxy core.
package tui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/javabetatester/burpcli/internal/flow"
	"github.com/javabetatester/burpcli/internal/history"
	"github.com/javabetatester/burpcli/internal/pipeline"
	"github.com/javabetatester/burpcli/internal/repeater"
)

const refreshEvery = 200 * time.Millisecond

// Deps are the core services the TUI drives.
type Deps struct {
	History  *history.Store
	Queue    *pipeline.Queue
	Repeater *repeater.Repeater
	Scope    history.Scope
	Export   func(flows []*flow.Flow) (string, error)
}

// Run starts the TUI and blocks until the user quits or ctx ends.
func Run(ctx context.Context, d Deps) error {
	_, err := tea.NewProgram(newModel(ctx, d), tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	return err
}

type mode int

const (
	modeBrowse mode = iota
	modeSearch
	modeEditHeld
	modeRepeater
)

type tab int

const (
	tabRequest tab = iota
	tabResponse
	tabRepeater
)

type tickMsg time.Time

type repeatMsg struct {
	resp flow.Response
	took time.Duration
	err  error
}

type model struct {
	ctx    context.Context
	deps   Deps
	w, h   int
	mode   mode
	tab    tab
	flows  []*flow.Flow
	cursor int
	query  textinput.Model
	filter history.Filter
	scoped bool
	editor textarea.Model
	view   viewport.Model
	status string
	help   bool

	seenVersion uint64
	seenHeld    int
	editID      uint64
	repOrig     *flow.Response
	repDiff     string
	repInfo     string
}

func newModel(ctx context.Context, d Deps) *model {
	q := textinput.New()
	q.Prompt = "/"
	ed := textarea.New()
	ed.ShowLineNumbers = false
	m := &model{ctx: ctx, deps: d, query: q, editor: ed, view: viewport.New(0, 0), scoped: len(d.Scope.Include)+len(d.Scope.Exclude) > 0}
	m.refresh()
	return m
}

func (m *model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd { return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m *model) refresh() {
	f := m.filter
	f.Query = m.query.Value()
	if m.scoped {
		f.Scope = m.deps.Scope
	}
	m.flows = m.deps.History.List(f)
	m.cursor = min(m.cursor, max(len(m.flows)-1, 0))
	m.seenVersion = m.deps.History.Version()
	m.seenHeld = len(m.deps.Queue.Pending())
	m.renderView()
}

func (m *model) selected() *flow.Flow {
	if m.cursor < len(m.flows) {
		return m.flows[m.cursor]
	}
	return nil
}

func (m *model) firstHeld() (pipeline.Held, bool) {
	p := m.deps.Queue.Pending()
	if len(p) == 0 {
		return pipeline.Held{}, false
	}
	return p[0], true
}

func (m *model) renderView() {
	var b strings.Builder
	if h, ok := m.firstHeld(); ok {
		b.WriteString(delStyle.Render(fmt.Sprintf("HELD #%d (%d waiting)", h.ID, m.seenHeld)) + "\n")
		b.WriteString(h.Request.Method + " " + h.Request.URL + "\n" + highlightHeaders(h.Request.Header) + "\n" + highlightBody(h.Request.Header, h.Request.Body))
		m.view.SetContent(b.String())
		return
	}
	f := m.selected()
	switch {
	case m.tab == tabRepeater:
		b.WriteString(dim.Render(m.repInfo) + "\n" + colorDiff(m.repDiff))
	case f == nil:
		b.WriteString(dim.Render("no traffic yet; point your client at the proxy"))
	case m.tab == tabRequest:
		b.WriteString(f.Request.Method + " " + f.Request.URL + " " + f.Request.Proto + "\n" + highlightHeaders(f.Request.Header) + "\n" + highlightBody(f.Request.Header, f.Request.Body))
	case f.Response == nil:
		b.WriteString(dim.Render("no response: " + f.Error))
	default:
		r := f.Response
		b.WriteString(fmt.Sprintf("%s %d %s\n", r.Proto, r.Status, http.StatusText(r.Status)) + highlightHeaders(r.Header) + "\n" + highlightBody(r.Header, r.Body))
	}
	m.view.SetContent(b.String())
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
	case tickMsg:
		if m.deps.History.Version() != m.seenVersion || len(m.deps.Queue.Pending()) != m.seenHeld {
			m.refresh()
		}
		return m, tick()
	case repeatMsg:
		m.onRepeat(msg)
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m *model) layout() {
	m.view.Width, m.view.Height = m.w, max(m.h-m.listHeight()-4, 3)
	m.editor.SetWidth(m.w)
	m.editor.SetHeight(max(m.h-4, 3))
}

func (m *model) listHeight() int { return max((m.h-4)/3, 3) }

func (m *model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.mode {
	case modeSearch:
		return m.onSearchKey(k)
	case modeEditHeld, modeRepeater:
		return m.onEditKey(k)
	}
	return m.onBrowseKey(k)
}

func (m *model) onSearchKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "enter":
		m.mode = modeBrowse
		m.query.Blur()
	case "esc":
		m.mode = modeBrowse
		m.query.SetValue("")
		m.query.Blur()
	default:
		var cmd tea.Cmd
		m.query, cmd = m.query.Update(k)
		m.refresh()
		return m, cmd
	}
	m.refresh()
	return m, nil
}

func (m *model) onEditKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.mode = modeBrowse
		m.status = "edit cancelled"
		return m, nil
	case "ctrl+s":
		req, err := flow.ParseRaw(m.editor.Value())
		if err != nil {
			m.status = "invalid request: " + err.Error()
			return m, nil
		}
		if m.mode == modeEditHeld {
			m.status = resolveStatus(m.deps.Queue.Resolve(m.editID, pipeline.Decision{Action: pipeline.Forward, Edited: &req}), "forwarded edited request")
			m.mode = modeBrowse
			m.refresh()
			return m, nil
		}
		m.mode = modeBrowse
		m.status = "sending..."
		return m, m.send(req)
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(k)
	return m, cmd
}

func resolveStatus(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func (m *model) send(req flow.Request) tea.Cmd {
	rep, ctx := m.deps.Repeater, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resp, took, err := rep.Send(cctx, req)
		return repeatMsg{resp: resp, took: took, err: err}
	}
}

func (m *model) onRepeat(r repeatMsg) {
	if r.err != nil {
		m.status = "repeater: " + r.err.Error()
		return
	}
	m.status = "repeater done"
	m.repInfo = fmt.Sprintf("repeater: %d in %s", r.resp.Status, r.took.Round(time.Millisecond))
	if m.repOrig != nil {
		m.repDiff = repeater.Diff(*m.repOrig, r.resp)
	} else {
		m.repDiff = repeater.Render(r.resp)
	}
	m.tab = tabRepeater
	m.renderView()
}

func (m *model) onBrowseKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "g":
		m.move(-len(m.flows))
	case "G":
		m.move(len(m.flows))
	case "tab":
		m.tab = (m.tab + 1) % 3
		m.renderView()
		m.view.GotoTop()
	case "pgdown":
		m.view.HalfPageDown()
	case "pgup":
		m.view.HalfPageUp()
	case "i":
		m.deps.Queue.SetEnabled(!m.deps.Queue.Enabled())
		m.status = fmt.Sprintf("intercept %s", onOff(m.deps.Queue.Enabled()))
		m.refresh()
	case "f", "d":
		m.decideHeld(k.String() == "f")
	case "e":
		if h, ok := m.firstHeld(); ok {
			m.openEditor(modeEditHeld, h.Request, h.ID)
		}
	case "r":
		if f := m.selected(); f != nil {
			m.repOrig = f.Response
			m.openEditor(modeRepeater, f.Request, 0)
		}
	case "/":
		m.mode = modeSearch
		m.query.Focus()
	case "s":
		m.scoped = !m.scoped
		m.status = "scope filter " + onOff(m.scoped)
		m.refresh()
	case "x":
		m.export()
	case "?":
		m.help = !m.help
	}
	return m, nil
}

func (m *model) decideHeld(forward bool) {
	h, ok := m.firstHeld()
	if !ok {
		return
	}
	act, msg := pipeline.Drop, "dropped"
	if forward {
		act, msg = pipeline.Forward, "forwarded"
	}
	m.status = resolveStatus(m.deps.Queue.Resolve(h.ID, pipeline.Decision{Action: act}), msg+" #"+fmt.Sprint(h.ID))
	m.refresh()
}

func (m *model) openEditor(md mode, req flow.Request, id uint64) {
	m.mode, m.editID = md, id
	m.editor.SetValue(req.Raw())
	m.editor.Focus()
	m.layout()
}

func (m *model) export() {
	if m.deps.Export == nil {
		return
	}
	path, err := m.deps.Export(m.flows)
	m.status = resolveStatus(err, "exported "+path)
}

func (m *model) move(d int) {
	m.cursor = max(0, min(m.cursor+d, len(m.flows)-1))
	m.renderView()
	m.view.GotoTop()
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

var (
	selStyle   = lipgloss.NewStyle().Reverse(true)
	titleStyle = lipgloss.NewStyle().Bold(true)
	helpStyle  = dim
)

func (m *model) View() string {
	if m.w == 0 {
		return "starting..."
	}
	head := titleStyle.Render(fmt.Sprintf("burpui  flows:%d  intercept:%s  scope:%s  held:%d", len(m.flows), onOff(m.deps.Queue.Enabled()), onOff(m.scoped), m.seenHeld))
	if m.mode == modeEditHeld || m.mode == modeRepeater {
		title := "edit held request"
		if m.mode == modeRepeater {
			title = "repeater: edit and send"
		}
		return head + "\n" + title + "\n" + m.editor.View() + "\n" + helpStyle.Render("ctrl+s send  esc cancel")
	}
	tabs := [...]string{"request", "response", "repeater"}
	return strings.Join([]string{head, m.listView(), titleStyle.Render("["+tabs[m.tab]+"]") + " " + m.status, m.view.View(), m.helpBar()}, "\n")
}

func (m *model) listView() string {
	h := m.listHeight()
	start := max(0, min(m.cursor-h/2, len(m.flows)-h))
	var lines []string
	for i := start; i < min(start+h, len(m.flows)); i++ {
		f := m.flows[i]
		status := "---"
		if f.Response != nil {
			status = fmt.Sprint(f.Response.Status)
		}
		line := fmt.Sprintf("%5d %-7s %s %-40s %s", f.ID, f.Request.Method, status, f.Request.Host(), trunc(f.Request.URL, max(m.w-70, 10)))
		if i == m.cursor {
			line = selStyle.Render(line)
		}
		lines = append(lines, line)
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	if m.mode == modeSearch {
		lines[h-1] = m.query.View()
	}
	return strings.Join(lines, "\n")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func (m *model) helpBar() string {
	if m.help {
		return helpStyle.Render("up/down move  tab request/response/repeater  pgup/pgdn scroll  i intercept  f forward  d drop  e edit held  r repeater  / search  s scope  x export HAR  ? short help  q quit")
	}
	return helpStyle.Render("j/k move  tab pane  i intercept  f/d fwd/drop  e edit  r repeat  / search  s scope  x HAR  ? help  q quit")
}

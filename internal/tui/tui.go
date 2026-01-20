package tui

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"burpui/internal/proxy"
	"burpui/internal/repeater"
)

type Config struct {
	ListenAddr   string
	FlowCh       <-chan *proxy.FlowSnapshot
	SetIntercept func(bool)

	ListBreakpoints  func() []proxy.BreakpointRule
	AddBreakpoint    func(string)
	ToggleBreakpoint func(int64)
	RemoveBreakpoint func(int64)
}

type screen int

const (
	screenMain screen = iota
	screenRepeater
	screenCompose
	screenEdit
	screenBreakpoints
)

type Model struct {
	cfg    Config
	styles styles
	keys   keyMap
	help   help.Model

	width  int
	height int

	intercept bool
	flows     map[int64]*proxy.Flow
	hostOpen  map[string]bool
	list      list.Model
	detail    viewport.Model

	scr         screen
	editorTitle string
	editor      textarea.Model
	resp        viewport.Model
	status      string

	bpList   list.Model
	bpInput  textarea.Model
	bpAdding bool

	toast      string
	toastUntil time.Time
}

type flowItem struct {
	id    int64
	host  string
	title string
	desc  string
}

func (i flowItem) Title() string       { return i.title }
func (i flowItem) Description() string { return i.desc }
func (i flowItem) FilterValue() string { return i.title }

type groupItem struct {
	host     string
	count    int
	lastID   int64
	expanded bool
}

func (i groupItem) Title() string {
	icon := "▸"
	if i.expanded {
		icon = "▾"
	}
	return fmt.Sprintf("%s %s (%d)", icon, i.host, i.count)
}

func (i groupItem) Description() string { return "" }
func (i groupItem) FilterValue() string { return i.host }

type flowMsg struct{ snap *proxy.FlowSnapshot }
type toastMsg struct{ text string }
type rpRespMsg struct {
	status string
	body   string
	err    error
}

func New(cfg Config) Model {
	s := newStyles()
	km := newKeyMap()

	l := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Histórico"
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()
	l.Styles.Title = l.Styles.Title.Foreground(lipgloss.Color("81")).Bold(true)
	l.Styles.PaginationStyle = l.Styles.PaginationStyle.Foreground(lipgloss.Color("244"))
	l.Styles.HelpStyle = l.Styles.HelpStyle.Foreground(lipgloss.Color("244"))

	d := viewport.New(0, 0)
	d.Style = lipgloss.NewStyle().Padding(0, 1)

	ed := textarea.New()
	ed.Placeholder = "Cole uma requisição HTTP aqui"
	ed.Prompt = ""
	ed.ShowLineNumbers = true
	ed.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("236"))

	resp := viewport.New(0, 0)
	resp.Style = lipgloss.NewStyle().Padding(0, 1)

	bpl := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	bpl.Title = "Breakpoints"
	bpl.SetShowHelp(false)
	bpl.DisableQuitKeybindings()
	bpl.Styles.Title = bpl.Styles.Title.Foreground(lipgloss.Color("81")).Bold(true)
	bpl.Styles.PaginationStyle = bpl.Styles.PaginationStyle.Foreground(lipgloss.Color("244"))
	bpl.Styles.HelpStyle = bpl.Styles.HelpStyle.Foreground(lipgloss.Color("244"))

	bpi := textarea.New()
	bpi.Placeholder = "match (substring)"
	bpi.Prompt = ""
	bpi.ShowLineNumbers = false
	bpi.SetHeight(1)
	bpi.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("236"))

	return Model{
		cfg:       cfg,
		styles:    s,
		keys:      km,
		help:      help.New(),
		intercept: false,
		flows:     map[int64]*proxy.Flow{},
		hostOpen:  map[string]bool{},
		list:      l,
		detail:    d,
		scr:       screenMain,
		editor:    ed,
		resp:      resp,
		bpList:    bpl,
		bpInput:   bpi,
	}
}

func (m Model) Init() tea.Cmd {
	return listenForFlows(m.cfg.FlowCh)
}

func listenForFlows(ch <-chan *proxy.FlowSnapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-ch
		if !ok {
			return nil
		}
		return flowMsg{snap: snap}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if t, ok := msg.(toastMsg); ok {
		m.toast = t.text
		m.toastUntil = time.Now().Add(2 * time.Second)
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case flowMsg:
		if msg.snap != nil && msg.snap.Flow != nil {
			m.flows[msg.snap.Flow.ID] = msg.snap.Flow
			m.rebuildList()
			m.updateDetail()
		}
		return m, listenForFlows(m.cfg.FlowCh)
	case rpRespMsg:
		if msg.err != nil {
			m.status = "erro: " + msg.err.Error()
		} else {
			m.status = msg.status
			m.resp.SetContent(msg.body)
		}
		return m, nil
	case tea.KeyMsg:
		if m.scr == screenRepeater || m.scr == screenCompose {
			return m.updateRepeater(msg)
		}
		if m.scr == screenEdit {
			return m.updateEdit(msg)
		}
		if m.scr == screenBreakpoints {
			return m.updateBreakpoints(msg)
		}
		return m.updateMain(msg)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.updateDetail()
	return m, cmd
}

func (m Model) updateMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Toggle):
		it := m.list.SelectedItem()
		if gi, ok := it.(groupItem); ok {
			m.hostOpen[gi.host] = !m.hostOpen[gi.host]
			m.rebuildList()
			m.updateDetail()
			return m, nil
		}
		return m, nil
	case key.Matches(msg, m.keys.ToggleIntercept):
		m.intercept = !m.intercept
		if m.cfg.SetIntercept != nil {
			m.cfg.SetIntercept(m.intercept)
		}
		return m, toastCmd(fmt.Sprintf("Intercept %v", onOff(m.intercept)))
	case key.Matches(msg, m.keys.Forward):
		f := m.selectedFlow()
		if f != nil && f.Intercepted && f.Pending {
			f.Forward()
			return m, toastCmd("Forward")
		}
		return m, nil
	case key.Matches(msg, m.keys.Drop):
		f := m.selectedFlow()
		if f != nil && f.Intercepted && f.Pending {
			f.Drop()
			return m, toastCmd("Drop")
		}
		return m, nil
	case key.Matches(msg, m.keys.Edit):
		f := m.selectedFlow()
		if f == nil || !f.Intercepted || !f.Pending {
			return m, nil
		}
		if strings.TrimSpace(f.RawRequest) == "" {
			return m, toastCmd("edição indisponível")
		}
		m.scr = screenEdit
		m.editorTitle = fmt.Sprintf("Edit #%d", f.ID)
		m.status = "Ctrl+S aplica/forward | Esc volta"
		m.resp.SetContent("")
		m.editor.SetValue(f.RawRequest)
		m.editor.Focus()
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Export):
		f := m.selectedFlow()
		if f == nil {
			return m, nil
		}
		path, err := exportFlow(f)
		if err != nil {
			return m, toastCmd("erro ao exportar")
		}
		return m, toastCmd("exportado: " + path)
	case key.Matches(msg, m.keys.Repeater):
		f := m.selectedFlow()
		m.scr = screenRepeater
		m.editorTitle = "Repeater"
		m.status = "Ctrl+S envia | Esc volta"
		m.resp.SetContent("")
		m.editor.SetValue("")
		if f != nil {
			m.editor.SetValue(renderRawRequest(f))
		}
		m.editor.Focus()
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Compose):
		m.scr = screenCompose
		m.editorTitle = "Compose"
		m.status = "Ctrl+S envia | Esc volta"
		m.resp.SetContent("")
		m.editor.SetValue("")
		m.editor.Focus()
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Breakpoints):
		m.scr = screenBreakpoints
		m.bpAdding = false
		m.bpInput.SetValue("")
		m.bpInput.Blur()
		m.refreshBreakpoints()
		m.layout()
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.updateDetail()
	return m, cmd
}

func (m Model) updateRepeater(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.scr = screenMain
		m.editor.Blur()
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Send):
		raw := m.editor.Value()
		m.status = "enviando..."
		return m, sendRepeaterCmd(raw)
	}

	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

func (m Model) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.scr = screenMain
		m.editor.Blur()
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Send):
		f := m.selectedFlow()
		if f != nil && f.Intercepted && f.Pending {
			raw := m.editor.Value()
			f.ForwardRaw(raw)
			m.scr = screenMain
			m.editor.Blur()
			m.layout()
			return m, toastCmd("aplicado")
		}
		m.scr = screenMain
		m.editor.Blur()
		m.layout()
		return m, nil
	}

	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	return m, cmd
}

type bpItem struct {
	id    int64
	title string
	desc  string
}

func (i bpItem) Title() string       { return i.title }
func (i bpItem) Description() string { return i.desc }
func (i bpItem) FilterValue() string { return i.title }

func (m *Model) refreshBreakpoints() {
	if m.cfg.ListBreakpoints == nil {
		m.bpList.SetItems(nil)
		return
	}
	rules := m.cfg.ListBreakpoints()
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID > rules[j].ID })
	items := make([]list.Item, 0, len(rules))
	for _, r := range rules {
		state := "OFF"
		if r.Enabled {
			state = "ON"
		}
		items = append(items, bpItem{id: r.ID, title: fmt.Sprintf("[%s] %s", state, r.Match), desc: fmt.Sprintf("id=%d", r.ID)})
	}
	m.bpList.SetItems(items)
}

func (m Model) updateBreakpoints(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.bpAdding {
		switch {
		case key.Matches(msg, m.keys.Back):
			m.bpAdding = false
			m.bpInput.SetValue("")
			m.bpInput.Blur()
			return m, nil
		case key.Matches(msg, m.keys.Toggle):
			match := strings.TrimSpace(m.bpInput.Value())
			if match != "" && m.cfg.AddBreakpoint != nil {
				m.cfg.AddBreakpoint(match)
			}
			m.bpAdding = false
			m.bpInput.SetValue("")
			m.bpInput.Blur()
			m.refreshBreakpoints()
			return m, nil
		}

		var cmd tea.Cmd
		m.bpInput, cmd = m.bpInput.Update(msg)
		return m, cmd
	}

	switch {
	case key.Matches(msg, m.keys.Back):
		m.scr = screenMain
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Add):
		m.bpAdding = true
		m.bpInput.SetValue("")
		m.bpInput.Focus()
		return m, nil
	case key.Matches(msg, m.keys.Toggle):
		it := m.bpList.SelectedItem()
		if it != nil {
			id := it.(bpItem).id
			if m.cfg.ToggleBreakpoint != nil {
				m.cfg.ToggleBreakpoint(id)
			}
			m.refreshBreakpoints()
		}
		return m, nil
	case key.Matches(msg, m.keys.Remove):
		it := m.bpList.SelectedItem()
		if it != nil {
			id := it.(bpItem).id
			if m.cfg.RemoveBreakpoint != nil {
				m.cfg.RemoveBreakpoint(id)
			}
			m.refreshBreakpoints()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.bpList, cmd = m.bpList.Update(msg)
	return m, cmd
}

func sendRepeaterCmd(raw string) tea.Cmd {
	return func() tea.Msg {
		status, body, err := repeater.SendRaw(raw, 15*time.Second)
		return rpRespMsg{status: status, body: body, err: err}
	}
}

func toastCmd(text string) tea.Cmd {
	return func() tea.Msg { return toastMsg{text: text} }
}

func (m Model) View() string {
	switch m.scr {
	case screenRepeater, screenCompose:
		return m.viewRepeater()
	case screenEdit:
		return m.viewEdit()
	case screenBreakpoints:
		return m.viewBreakpoints()
	default:
		return m.viewMain()
	}
}

func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	contentW := m.width - m.styles.app.GetHorizontalPadding()
	contentH := m.height - m.styles.app.GetVerticalPadding()
	if contentW < 20 {
		contentW = 20
	}
	if contentH < 10 {
		contentH = 10
	}

	if m.scr == screenRepeater {
		headerH := 3
		editorW := contentW
		editorH := (contentH - headerH) / 2
		respH := contentH - headerH - editorH
		if editorH < 6 {
			editorH = 6
		}
		if respH < 4 {
			respH = 4
		}
		m.editor.SetWidth(editorW)
		m.editor.SetHeight(editorH)
		m.resp.Width = editorW
		m.resp.Height = respH
		return
	}

	if m.scr == screenCompose {
		headerH := 3
		editorW := contentW
		editorH := (contentH - headerH) / 2
		respH := contentH - headerH - editorH
		if editorH < 6 {
			editorH = 6
		}
		if respH < 4 {
			respH = 4
		}
		m.editor.SetWidth(editorW)
		m.editor.SetHeight(editorH)
		m.resp.Width = editorW
		m.resp.Height = respH
		return
	}

	if m.scr == screenEdit {
		headerH := 3
		editorW := contentW
		editorH := contentH - headerH
		if editorH < 8 {
			editorH = 8
		}
		m.editor.SetWidth(editorW)
		m.editor.SetHeight(editorH)
		return
	}

	if m.scr == screenBreakpoints {
		m.bpList.SetSize(contentW, contentH-5)
		m.bpInput.SetWidth(contentW)
		return
	}

	leftW := contentW / 3
	rightW := contentW - leftW
	if leftW < 28 {
		leftW = 28
		rightW = contentW - leftW
	}
	m.list.SetSize(leftW, contentH-3)
	m.detail.Width = rightW
	m.detail.Height = contentH - 3
}

func (m *Model) rebuildList() {
	type group struct {
		host   string
		count  int
		lastID int64
		flows  []*proxy.Flow
	}

	selectedKind, selectedHost, selectedID := m.selectedKey()

	byHost := map[string]*group{}
	for _, f := range m.flows {
		h := normalizeHost(f)
		g := byHost[h]
		if g == nil {
			g = &group{host: h}
			byHost[h] = g
		}
		g.count++
		if f.ID > g.lastID {
			g.lastID = f.ID
		}
		g.flows = append(g.flows, f)
	}

	groups := make([]*group, 0, len(byHost))
	for _, g := range byHost {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].lastID > groups[j].lastID })

	items := make([]list.Item, 0, len(m.flows)+len(groups))
	for _, g := range groups {
		open := m.hostOpen[g.host]
		items = append(items, groupItem{host: g.host, count: g.count, lastID: g.lastID, expanded: open})
		if !open {
			continue
		}
		sort.Slice(g.flows, func(i, j int) bool { return g.flows[i].ID > g.flows[j].ID })
		for _, f := range g.flows {
			title := fmt.Sprintf("  %d  %s %s", f.ID, padRight(f.Method, 6), shortURL(pathFromURL(f.URL)))
			desc := fmt.Sprintf("%s | %s", statusLabel(f), durationLabel(f))
			items = append(items, flowItem{id: f.ID, host: g.host, title: title, desc: desc})
		}
	}

	m.list.SetItems(items)
	for i, it := range items {
		switch v := it.(type) {
		case flowItem:
			if selectedKind == "flow" && v.id == selectedID {
				m.list.Select(i)
				return
			}
		case groupItem:
			if selectedKind == "group" && v.host == selectedHost {
				m.list.Select(i)
				return
			}
		}
	}
}

func (m *Model) selectedKey() (kind string, host string, id int64) {
	it := m.list.SelectedItem()
	if it == nil {
		return "", "", 0
	}
	switch v := it.(type) {
	case flowItem:
		return "flow", v.host, v.id
	case groupItem:
		return "group", v.host, 0
	default:
		return "", "", 0
	}
}

func (m *Model) updateDetail() {
	f := m.selectedFlow()
	if f == nil {
		if gi, ok := m.list.SelectedItem().(groupItem); ok {
			m.detail.SetContent(m.styles.dim.Render(fmt.Sprintf("%s (%d)", gi.host, gi.count)))
			return
		}
		m.detail.SetContent(m.styles.dim.Render("Selecione uma requisição"))
		return
	}

	var b strings.Builder
	b.WriteString(m.styles.title.Render(fmt.Sprintf("#%d", f.ID)))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%s %s\n", f.Method, f.URL))
	if f.Intercepted && f.Pending {
		b.WriteString(m.styles.badgeWarn.Render("PENDENTE"))
		b.WriteString(" ")
		b.WriteString(m.styles.dim.Render("(e = edit, f = forward, d = drop)"))
		b.WriteString("\n")
	}
	if f.Error != "" {
		b.WriteString(m.styles.err.Render("erro: " + f.Error))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(m.styles.dim.Render("Request"))
	b.WriteString("\n")
	b.WriteString(renderHeaders(f.RequestHeader))
	if len(f.RequestBody) > 0 {
		b.WriteString("\n")
		b.WriteString(renderBodyPreview(f.RequestBody, f.ReqTruncated))
	}
	b.WriteString("\n\n")
	b.WriteString(m.styles.dim.Render("Response"))
	b.WriteString("\n")
	if f.StatusCode != 0 {
		b.WriteString(fmt.Sprintf("Status: %d\n", f.StatusCode))
	}
	b.WriteString(renderHeaders(f.ResponseHeader))
	if len(f.ResponseBody) > 0 {
		b.WriteString("\n")
		b.WriteString(renderBodyPreview(f.ResponseBody, f.RespTruncated))
	}
	m.detail.SetContent(b.String())
}

func (m Model) viewMain() string {
	header := m.viewHeader()
	footer := m.viewFooter()

	left := m.styles.border.Width(m.list.Width()).Height(m.list.Height()).Render(m.list.View())
	right := m.styles.border.Width(m.detail.Width).Height(m.detail.Height).Render(m.detail.View())
	row := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	return m.styles.app.Render(lipgloss.JoinVertical(lipgloss.Left, header, row, footer))
}

func (m Model) viewRepeater() string {
	header := lipgloss.JoinHorizontal(lipgloss.Left,
		m.styles.title.Render(m.editorTitle),
		" ",
		m.styles.dim.Render(m.status),
	)

	editor := m.styles.border.Render(m.editor.View())
	resp := m.styles.border.Render(m.resp.View())
	footer := m.viewFooter()

	return m.styles.app.Render(lipgloss.JoinVertical(lipgloss.Left, header, editor, resp, footer))
}

func (m Model) viewEdit() string {
	header := lipgloss.JoinHorizontal(lipgloss.Left,
		m.styles.title.Render(m.editorTitle),
		" ",
		m.styles.dim.Render(m.status),
	)

	editor := m.styles.border.Render(m.editor.View())
	footer := m.viewFooter()

	return m.styles.app.Render(lipgloss.JoinVertical(lipgloss.Left, header, editor, footer))
}

func (m Model) viewBreakpoints() string {
	header := lipgloss.JoinHorizontal(lipgloss.Left,
		m.styles.title.Render("Breakpoints"),
		" ",
		m.styles.dim.Render("a adicionar | enter alterna | del remove | esc volta"),
	)

	listBox := m.styles.border.Render(m.bpList.View())
	input := ""
	if m.bpAdding {
		input = m.styles.border.Render(m.bpInput.View())
	} else {
		input = m.styles.border.Render(m.styles.dim.Render("pressione 'a' para adicionar"))
	}
	footer := m.viewFooter()

	return m.styles.app.Render(lipgloss.JoinVertical(lipgloss.Left, header, listBox, input, footer))
}

func (m Model) viewHeader() string {
	badge := m.styles.badgeOff.Render("INTERCEPT OFF")
	if m.intercept {
		badge = m.styles.badgeOn.Render("INTERCEPT ON")
	}
	title := m.styles.title.Render("burpui")
	addr := m.styles.dim.Render("proxy: " + m.cfg.ListenAddr)
	return lipgloss.JoinHorizontal(lipgloss.Left, title, " ", badge, "  ", addr)
}

func (m Model) viewFooter() string {
	now := time.Now()
	toast := ""
	if m.toast != "" && now.Before(m.toastUntil) {
		toast = m.renderBar(m.styles.status, m.toast)
	} else {
		switch m.scr {
		case screenMain:
			toast = m.renderBar(m.styles.statusDim, "i intercept | enter expande | e edit | f forward | d drop | r repeater | c compose | b breakpoints | x export | q sair")
		case screenRepeater, screenCompose:
			toast = m.renderBar(m.styles.statusDim, "Ctrl+S envia | Esc volta")
		case screenEdit:
			toast = m.renderBar(m.styles.statusDim, "Ctrl+S aplica/forward | Esc volta")
		case screenBreakpoints:
			toast = m.renderBar(m.styles.statusDim, "a add | enter toggle | del remove | esc volta")
		default:
			toast = m.renderBar(m.styles.statusDim, "q sair")
		}
	}
	return toast
}

func (m Model) renderBar(s lipgloss.Style, text string) string {
	w := m.width - m.styles.app.GetHorizontalPadding()
	if w < 0 {
		w = 0
	}
	return s.Width(w).Render(text)
}

func normalizeHost(f *proxy.Flow) string {
	h := strings.TrimSpace(f.Host)
	if h != "" {
		if strings.Contains(h, ":") {
			if host, _, err := net.SplitHostPort(h); err == nil {
				h = host
			}
		}
		return h
	}
	u, err := url.Parse(f.URL)
	if err == nil {
		h = u.Hostname()
		if h != "" {
			return h
		}
	}
	return "(sem host)"
}

func pathFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Path == "" {
		return "/"
	}
	if u.RawQuery != "" {
		return u.Path + "?" + u.RawQuery
	}
	return u.Path
}

func (m *Model) selectedFlowID() int64 {
	it := m.list.SelectedItem()
	if it == nil {
		return 0
	}
	if fi, ok := it.(flowItem); ok {
		return fi.id
	}
	return 0
}

func (m *Model) selectedFlow() *proxy.Flow {
	id := m.selectedFlowID()
	if id == 0 {
		return nil
	}
	return m.flows[id]
}

func exportFlow(f *proxy.Flow) (string, error) {
	dir := filepath.Join("exports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%d-%s.txt", f.ID, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	raw := renderRawRequest(f) + "\n\n" + renderRawResponse(f)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func renderRawRequest(f *proxy.Flow) string {
	var b bytes.Buffer
	urlStr := f.URL
	if urlStr == "" {
		urlStr = "/"
	}
	b.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", f.Method, urlStr))
	if f.Host != "" {
		b.WriteString("Host: ")
		b.WriteString(f.Host)
		b.WriteString("\r\n")
	}
	for k, vv := range f.RequestHeader {
		if strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vv {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\r\n")
	b.Write(f.RequestBody)
	return b.String()
}

func renderRawResponse(f *proxy.Flow) string {
	var b bytes.Buffer
	code := f.StatusCode
	if code == 0 {
		code = 0
	}
	b.WriteString(fmt.Sprintf("HTTP/1.1 %d\r\n", code))
	for k, vv := range f.ResponseHeader {
		for _, v := range vv {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
	}
	b.WriteString("\r\n")
	b.Write(f.ResponseBody)
	return b.String()
}

func renderHeaders(h http.Header) string {
	if h == nil {
		return ""
	}
	var b strings.Builder
	for k, vv := range h {
		for _, v := range vv {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func renderBodyPreview(body []byte, truncated bool) string {
	s := string(body)
	if truncated {
		s += "\n…(truncado)"
	}
	return s
}

func shortURL(u string) string {
	if u == "" {
		return ""
	}
	if len(u) > 60 {
		return u[:57] + "…"
	}
	return u
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func statusLabel(f *proxy.Flow) string {
	if f.Pending {
		return "pending"
	}
	if f.Error != "" {
		return "err"
	}
	if f.StatusCode != 0 {
		return fmt.Sprintf("%d", f.StatusCode)
	}
	return "-"
}

func durationLabel(f *proxy.Flow) string {
	if f.Pending {
		return "…"
	}
	if f.Duration == 0 {
		return "0ms"
	}
	if f.Duration < time.Second {
		return fmt.Sprintf("%dms", f.Duration.Milliseconds())
	}
	return fmt.Sprintf("%.2fs", f.Duration.Seconds())
}

func hostLabel(f *proxy.Flow) string {
	if f.Host != "" {
		return f.Host
	}
	return "-"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

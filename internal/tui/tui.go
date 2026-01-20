package tui

import (
	"bytes"
	"fmt"
	"net/http"
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
}

type screen int

const (
	screenMain screen = iota
	screenRepeater
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
	list      list.Model
	detail    viewport.Model

	scr      screen
	rpEditor textarea.Model
	rpResp   viewport.Model
	rpStatus string

	toast      string
	toastUntil time.Time
}

type flowItem struct {
	id    int64
	title string
	desc  string
}

func (i flowItem) Title() string       { return i.title }
func (i flowItem) Description() string { return i.desc }
func (i flowItem) FilterValue() string { return i.title }

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

	rp := textarea.New()
	rp.Placeholder = "Cole uma requisição HTTP aqui"
	rp.Prompt = ""
	rp.ShowLineNumbers = true
	rp.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("236"))

	rpResp := viewport.New(0, 0)
	rpResp.Style = lipgloss.NewStyle().Padding(0, 1)

	return Model{
		cfg:       cfg,
		styles:    s,
		keys:      km,
		help:      help.New(),
		intercept: false,
		flows:     map[int64]*proxy.Flow{},
		list:      l,
		detail:    d,
		scr:       screenMain,
		rpEditor:  rp,
		rpResp:    rpResp,
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
			m.rpStatus = "erro: " + msg.err.Error()
		} else {
			m.rpStatus = msg.status
			m.rpResp.SetContent(msg.body)
		}
		return m, nil
	case tea.KeyMsg:
		if m.scr == screenRepeater {
			return m.updateRepeater(msg)
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
	case key.Matches(msg, m.keys.ToggleIntercept):
		m.intercept = !m.intercept
		if m.cfg.SetIntercept != nil {
			m.cfg.SetIntercept(m.intercept)
		}
		return m, toastCmd(fmt.Sprintf("Intercept %v", onOff(m.intercept)))
	case key.Matches(msg, m.keys.Forward):
		f := m.selectedFlow()
		if f != nil && f.Intercepted && f.Pending {
			f.Decide(proxy.DecisionForward)
			return m, toastCmd("Forward")
		}
		return m, nil
	case key.Matches(msg, m.keys.Drop):
		f := m.selectedFlow()
		if f != nil && f.Intercepted && f.Pending {
			f.Decide(proxy.DecisionDrop)
			return m, toastCmd("Drop")
		}
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
		m.rpStatus = "Ctrl+S envia | Esc volta"
		m.rpResp.SetContent("")
		m.rpEditor.SetValue("")
		if f != nil {
			m.rpEditor.SetValue(renderRawRequest(f))
		}
		m.rpEditor.Focus()
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
		m.rpEditor.Blur()
		m.layout()
		return m, nil
	case key.Matches(msg, m.keys.Send):
		raw := m.rpEditor.Value()
		m.rpStatus = "enviando..."
		return m, sendRepeaterCmd(raw)
	}

	var cmd tea.Cmd
	m.rpEditor, cmd = m.rpEditor.Update(msg)
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
	if m.scr == screenRepeater {
		return m.viewRepeater()
	}
	return m.viewMain()
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
		m.rpEditor.SetWidth(editorW)
		m.rpEditor.SetHeight(editorH)
		m.rpResp.Width = editorW
		m.rpResp.Height = respH
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
	items := make([]list.Item, 0, len(m.flows))
	for _, f := range m.flows {
		title := fmt.Sprintf("%d  %s %s", f.ID, padRight(f.Method, 6), shortURL(f.URL))
		desc := fmt.Sprintf("%s | %s | %s", statusLabel(f), durationLabel(f), hostLabel(f))
		items = append(items, flowItem{id: f.ID, title: title, desc: desc})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].(flowItem).id > items[j].(flowItem).id
	})
	sel := m.selectedFlowID()
	m.list.SetItems(items)
	if sel != 0 {
		for i, it := range items {
			if it.(flowItem).id == sel {
				m.list.Select(i)
				break
			}
		}
	}
}

func (m *Model) updateDetail() {
	f := m.selectedFlow()
	if f == nil {
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
		b.WriteString(m.styles.dim.Render("(f = forward, d = drop)"))
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
		m.styles.title.Render("Repeater"),
		" ",
		m.styles.dim.Render(m.rpStatus),
	)

	editor := m.styles.border.Render(m.rpEditor.View())
	resp := m.styles.border.Render(m.rpResp.View())
	footer := m.viewFooter()

	return m.styles.app.Render(lipgloss.JoinVertical(lipgloss.Left, header, editor, resp, footer))
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
		toast = m.styles.status.Render(m.toast)
	} else {
		toast = m.styles.statusDim.Render("i intercept | f forward | d drop | r repeater | x export | q sair")
	}
	return toast
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

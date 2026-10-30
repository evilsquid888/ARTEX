package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var neon = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FFD1"))
var pink = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF55CC"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#8993AB"))
var selected = lipgloss.NewStyle().Foreground(lipgloss.Color("#071018")).Background(lipgloss.Color("#00FFD1"))
var tabs = []string{"ACTIVITY", "WORKERS", "CHAT", "FINDINGS", "LOGS"}

type historyMsg struct {
	task, worker string
	items        []Activity
	err          error
}

type rowsMsg struct {
	task, kind string
	rows       []map[string]any
	err        error
}
type tasksMsg struct {
	tasks []Task
	err   error
}
type tickMsg struct{}
type eventMsg struct {
	task       string
	activity   *Activity
	status     string
	generation int
}
type detailMsg struct {
	tab        int
	worker     string
	task, text string
	err        error
}
type sentMsg struct {
	task, text string
	err        error
}
type streamStart struct {
	generation int
	task       string
	items      []Activity
	cursor     int64
	err        error
}
type Model struct {
	ctx           context.Context
	streamCtx     context.Context
	composeWorker string
	generation    int

	client                             *Client
	width, height                      int
	tasks                              []Task
	taskIndex, tab, index              int
	task                               string
	activity                           []Activity
	rows                               []map[string]any
	viewport                           viewport.Model
	input                              textinput.Model
	composing, filtering               bool
	filter                             string
	detail, worker, notice, connection string
	events                             chan eventMsg
	cancel                             context.CancelFunc
	sending                            bool
}

func NewModel(ctx context.Context, c *Client) Model {
	in := textinput.New()
	in.Placeholder = "Message the task's main agent…"
	in.CharLimit = 4000
	return Model{ctx: ctx, client: c, width: 100, height: 30, viewport: viewport.New(), input: in, connection: "select a task"}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.loadTasks(), tick()) }
func tick() tea.Cmd           { return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return tickMsg{} }) }
func (m Model) loadTasks() tea.Cmd {
	return func() tea.Msg {
		var out struct{ Tasks []Task }
		err := m.client.JSON(m.ctx, "GET", "/api/tasks", nil, &out)
		return tasksMsg{out.Tasks, err}
	}
}
func (m Model) start() tea.Cmd {
	task, generation := m.task, m.generation
	return func() tea.Msg {
		var out struct {
			Items  []Activity
			Cursor int64
		}
		err := m.client.JSON(m.ctx, "GET", "/api/exploration/activity"+taskQuery(task)+"&limit=300", nil, &out)
		return streamStart{generation, task, out.Items, out.Cursor, err}
	}
}
func waitEvent(ctx context.Context, ch chan eventMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case e := <-ch:
			return e
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *Model) startStream(cursor int64) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	m.cancel = cancel
	m.streamCtx = ctx
	m.events = make(chan eventMsg, 128)
	ch, task, generation := m.events, m.task, m.generation
	send := func(e eventMsg) error {
		select {
		case ch <- e:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	go m.client.Stream(ctx, task, cursor, func(a Activity) error { return send(eventMsg{task: task, activity: &a, generation: generation}) }, func(s string) { _ = send(eventMsg{task: task, status: s, generation: generation}) })
	return waitEvent(ctx, ch)
}
func (m Model) loadRows() tea.Cmd {
	task, tab := m.task, m.tab
	if task == "" {
		return nil
	}
	return func() tea.Msg {
		path := ""
		switch tab {
		case 1:
			path = "/api/exploration/intents" + taskQuery(task)
		case 3:
			path = "/api/exploration/findings" + taskQuery(task)
		case 4:
			path = "/api/logs?limit=200"
		default:
			return nil
		}
		var raw json.RawMessage
		err := m.client.JSON(m.ctx, "GET", path, nil, &raw)
		var rows []map[string]any
		if err == nil {
			if len(raw) > 0 && raw[0] == '[' {
				err = json.Unmarshal(raw, &rows)
			} else {
				var out map[string]json.RawMessage
				err = json.Unmarshal(raw, &out)
				if err == nil {
					for _, key := range []string{"items", "findings"} {
						if val, ok := out[key]; ok {
							err = json.Unmarshal(val, &rows)
							break
						}
					}
				}
			}
		}
		return rowsMsg{task, tabs[tab], rows, err}
	}
}
func (m *Model) chooseTask(i int) tea.Cmd {
	if len(m.tasks) == 0 {
		return nil
	}
	if i < 0 {
		i = len(m.tasks) - 1
	}
	i %= len(m.tasks)
	m.taskIndex = i
	if m.tasks[i].ID == m.task {
		return nil
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.generation++
	m.task = m.tasks[i].ID
	m.activity = nil
	m.rows = nil
	m.detail = ""
	m.worker = ""
	m.index = 0
	m.connection = "loading"
	m.viewport.GotoTop()
	return tea.Batch(m.start(), m.loadRows())
}
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || r == '\u202e' || r == '\u202d' {
			return -1
		}
		return r
	}, s)
}
func field(row map[string]any, key string) string {
	v := row[key]
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func (m Model) visibleActivities() []Activity {
	out := []Activity{}
	for _, a := range m.activity {
		if m.tab == 2 && a.Worker != "mainagent" {
			continue
		}
		if m.worker != "" && a.IntentID != m.worker {
			continue
		}
		if m.filter != "" && !strings.Contains(strings.ToLower(a.Summary+" "+a.Worker+" "+a.Tool), strings.ToLower(m.filter)) {
			continue
		}
		out = append(out, a)
	}
	return out
}
func (m Model) visibleRows() []map[string]any {
	if m.filter == "" {
		return m.rows
	}
	out := []map[string]any{}
	for _, r := range m.rows {
		b, _ := json.Marshal(r)
		if strings.Contains(strings.ToLower(string(b)), strings.ToLower(m.filter)) {
			out = append(out, r)
		}
	}
	return out
}
func (m *Model) content() {
	var lines []string
	if m.detail != "" {
		lines = []string{clean(m.detail)}
	} else if m.tab == 0 || m.tab == 2 {
		acts := m.visibleActivities()
		for i, a := range acts {
			prefix := "  "
			if i == m.index {
				prefix = "> "
			}
			label := fmt.Sprintf("%s#%d %s · %s", prefix, a.Seq, a.Worker, a.Kind)
			if a.Tool != "" {
				label += " · " + a.Tool
			}
			if a.IsError {
				label += " [ERROR]"
			}
			if i == m.index {
				label = neon.Render(clean(label))
			} else {
				label = muted.Render(clean(label))
			}
			lines = append(lines, label+"\n"+clean(a.Summary)+"\n")
		}
	} else {
		for i, r := range m.visibleRows() {
			b, _ := json.Marshal(r)
			label := string(b)
			switch m.tab {
			case 1:
				label = fmt.Sprintf("#%s [%s] %s", field(r, "id"), field(r, "state"), field(r, "payload"))
			case 3:
				name := field(r, "name")
				if name == "" {
					name = field(r, "vulnclass")
				}
				label = fmt.Sprintf("#%s [%s / %s] %s\n%s", field(r, "id"), field(r, "severity"), field(r, "status"), name, field(r, "summary"))
			case 4:
				label = fmt.Sprintf("%s [%s] %s", field(r, "ts"), field(r, "level"), field(r, "text"))
			}
			prefix := "  "
			if i == m.index {
				prefix = "> "
			}
			label = prefix + clean(label)
			if i == m.index {
				label = neon.Render(label)
			}
			lines = append(lines, label+"\n")
		}
	}
	if len(lines) == 0 {
		lines = []string{muted.Render("No entries yet. Live activity appears here as ARTEX works.")}
	}
	m.viewport.SetContent(strings.Join(lines, "\n"))
}
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.SetWidth(max(10, msg.Width-4))
		m.viewport.SetHeight(max(3, msg.Height-12))
		m.input.SetWidth(max(10, msg.Width-8))
		m.content()
	case tasksMsg:
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		m.tasks = msg.tasks
		for i, t := range m.tasks {
			if t.ID == m.task {
				m.taskIndex = i
				return m, nil
			}
		}
		if len(m.tasks) == 0 {
			if m.cancel != nil {
				m.cancel()
			}
			m.generation++
			m.cancel = nil
			m.task = ""
			m.activity = nil
			m.rows = nil
			m.connection = "no tasks"
			m.content()
			return m, nil
		}
		return m, m.chooseTask(0)
	case streamStart:
		if msg.task != m.task || msg.generation != m.generation {
			return m, nil
		}
		if msg.err != nil {
			m.notice = msg.err.Error()
			m.connection = "snapshot failed; press r to retry"
			return m, nil
		}
		m.activity = msg.items
		m.content()
		return m, m.startStream(msg.cursor)
	case eventMsg:
		if msg.task != m.task || msg.generation != m.generation {
			return m, nil
		}
		if msg.status != "" {
			m.connection = msg.status
		}
		if msg.activity != nil {
			a := *msg.activity
			if len(m.activity) == 0 || a.Seq > m.activity[len(m.activity)-1].Seq {
				bottom := m.viewport.AtBottom()
				m.activity = append(m.activity, a)
				if len(m.activity) > 2000 {
					m.activity = m.activity[len(m.activity)-2000:]
					m.index = max(0, m.index-1)
				}
				m.content()
				if bottom {
					m.viewport.GotoBottom()
				}
			}
		}
		return m, waitEvent(m.streamCtx, m.events)
	case historyMsg:
		if msg.task != m.task || msg.worker != m.worker {
			return m, nil
		}
		if msg.err != nil {
			m.notice = msg.err.Error()
			return m, nil
		}
		bySeq := map[int64]Activity{}
		for _, a := range m.activity {
			bySeq[a.Seq] = a
		}
		for _, a := range msg.items {
			bySeq[a.Seq] = a
		}
		m.activity = nil
		for _, a := range bySeq {
			m.activity = append(m.activity, a)
		}
		sort.Slice(m.activity, func(i, j int) bool { return m.activity[i].Seq < m.activity[j].Seq })
		m.index = 0
		m.content()
		m.viewport.GotoTop()
	case tickMsg:
		return m, tea.Batch(tick(), m.loadTasks(), m.loadRows())
	case rowsMsg:
		if msg.task == m.task && msg.kind == tabs[m.tab] {
			if msg.err != nil {
				m.notice = msg.err.Error()
			} else {
				m.rows = msg.rows
				m.content()
			}
		}
	case detailMsg:
		if msg.task == m.task && msg.tab == m.tab && msg.worker == m.worker {
			if msg.err != nil {
				m.notice = msg.err.Error()
			} else {
				m.detail = msg.text
				m.viewport.GotoTop()
				m.content()
			}
		}
	case sentMsg:
		m.sending = false
		if msg.err != nil {
			m.notice = "Send failed (not retried): " + msg.err.Error()
		} else {
			m.notice = "Message accepted by task #" + msg.task
			if msg.task == m.task && m.input.Value() == msg.text {
				m.input.SetValue("")
				m.composing = false
				m.input.Blur()
			}
		}
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
		if m.composing || m.filtering {
			if key == "esc" {
				m.composing = false
				m.filtering = false
				m.input.Blur()
				return m, nil
			}
			if key == "enter" {
				if m.filtering {
					m.filter = m.input.Value()
					m.filtering = false
					m.input.SetValue("")
					m.index = 0
					m.content()
					return m, nil
				}
				text := strings.TrimSpace(m.input.Value())
				if text == "" || m.sending {
					return m, nil
				}
				m.sending = true
				task, worker := m.task, m.composeWorker
				return m, func() tea.Msg {
					path := "/api/chat" + taskQuery(task)
					body := map[string]string{"message": text}
					if worker != "" {
						path = "/api/tasks/" + task + "/intents/" + worker + "/messages"
						body["request_id"] = uuid.NewString()
					}
					err := m.client.JSON(m.ctx, "POST", path, body, nil)
					return sentMsg{task, text, err}
				}
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		switch key {
		case "q":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		case "[":
			return m, m.chooseTask(m.taskIndex - 1)
		case "]":
			return m, m.chooseTask(m.taskIndex + 1)
		case "tab", "shift+tab":
			d := 1
			if key == "shift+tab" {
				d = -1
			}
			m.tab = (m.tab + d + len(tabs)) % len(tabs)
			m.detail = ""
			m.rows = nil
			m.index = 0
			m.worker = ""
			m.viewport.GotoTop()
			m.content()
			return m, m.loadRows()
		case "1", "2", "3", "4", "5":
			m.tab = int(key[0] - '1')
			m.detail = ""
			m.rows = nil
			m.worker = ""
			m.index = 0
			m.content()
			return m, m.loadRows()
		case "w":
			if m.tab == 1 {
				rows := m.visibleRows()
				if m.index < len(rows) {
					r := rows[m.index]
					if field(r, "state") != "paused" || field(r, "inherited") == "true" {
						m.notice = "Worker replies require a paused, local worker. Open its transcript with Enter."
						return m, nil
					}
					m.composeWorker = field(r, "id")
					m.composing = true
					m.input.SetValue("")
					return m, m.input.Focus()
				}
			}
		case "b":
			return m, m.loadHistory()
		case "c":
			m.composeWorker = ""
			if m.task != "" {
				m.composing = true
				m.input.SetValue("")
				return m, m.input.Focus()
			}
		case "/":
			m.filtering = true
			m.input.SetValue(m.filter)
			return m, m.input.Focus()
		case "esc":
			m.detail = ""
			m.worker = ""
			m.filter = ""
			m.index = 0
			m.content()
		case "r":
			if m.task != "" && m.cancel == nil {
				return m, m.start()
			}
			return m, tea.Batch(m.loadTasks(), m.loadRows())
		case "j", "down", "k", "up":
			if m.detail != "" {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
			d := 1
			if key == "k" || key == "up" {
				d = -1
			}
			count := len(m.visibleRows())
			if m.tab == 0 || m.tab == 2 {
				count = len(m.visibleActivities())
			}
			m.index = max(0, min(max(0, count-1), m.index+d))
			m.content()
			if d > 0 {
				m.viewport.ScrollDown(3)
			} else {
				m.viewport.ScrollUp(3)
			}
		case "g":
			m.index = 0
			m.content()
			m.viewport.GotoTop()
		case "G":
			count := len(m.visibleRows())
			if m.tab == 0 || m.tab == 2 {
				count = len(m.visibleActivities())
			}
			m.index = max(0, count-1)
			m.content()
			m.viewport.GotoBottom()
		case "enter":
			if m.tab == 1 {
				rows := m.visibleRows()
				if m.index < len(rows) {
					m.worker = field(rows[m.index], "id")
					m.tab = 0
					m.index = 0
					m.detail = ""
					m.content()
					m.viewport.GotoBottom()
					return m, m.loadHistory()
				}
				return m, nil
			}
			if m.tab == 0 || m.tab == 2 {
				acts := m.visibleActivities()
				if m.index < len(acts) {
					a := acts[m.index]
					task, tab, worker := m.task, m.tab, m.worker
					return m, func() tea.Msg {
						var out struct{ Detail string }
						err := m.client.JSON(m.ctx, "GET", fmt.Sprintf("/api/exploration/activity/%d", a.Seq)+taskQuery(task), nil, &out)
						return detailMsg{tab, worker, task, fmt.Sprintf("#%d %s / %s\n\n%s\n\n%s", a.Seq, a.Worker, a.Kind, a.Summary, out.Detail), err}
					}
				}
			} else {
				rows := m.visibleRows()
				if m.index < len(rows) {
					b, _ := json.MarshalIndent(rows[m.index], "", "  ")
					m.detail = string(b)
					m.viewport.GotoTop()
					m.content()
				}
			}
		}
	}
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}
func (m Model) View() tea.View {
	if m.width < 50 || m.height < 18 {
		v := tea.NewView("ARTEX // enlarge terminal to at least 50 × 18\nq quit")
		v.AltScreen = true
		return v
	}
	title := pink.Bold(true).Render(" ▟ ARTEX ") + neon.Render("// TERMINAL OPERATIONS")
	status := muted.Render(clean(m.connection))
	header := title + "\n " + status
	taskLine := " No tasks — waiting for ARTEX"
	if len(m.tasks) > m.taskIndex {
		t := m.tasks[m.taskIndex]
		name := t.Name
		if name == "" {
			name = t.Description
		}
		taskLine = fmt.Sprintf(" [%d/%d] #%s %s | %s | workers %d | goals %d/%d", m.taskIndex+1, len(m.tasks), t.ID, clean(name), clean(t.Status), t.InFlight, t.GoalsMet, t.GoalsTotal)
	}
	var nav []string
	for i, t := range tabs {
		s := fmt.Sprintf(" %d %s ", i+1, t)
		if i == m.tab {
			s = selected.Render(s)
		} else {
			s = muted.Render(s)
		}
		nav = append(nav, s)
	}
	extra := ""
	if m.worker != "" {
		extra = " · worker #" + m.worker
	}
	if m.filter != "" {
		extra += " · filter: " + clean(m.filter)
	}
	body := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#38445B")).Render(m.viewport.View())
	footer := muted.Render(" [/] task  1–5 tabs  j/k select  Enter drill  PgUp/PgDn scroll\n c chat  w worker reply  b older  / filter  Esc back  g/G top/end  r refresh  q detach")
	prompt := muted.Render(" Chat sends instructions to the selected task's main agent.")
	if m.composing || m.filtering {
		label := " SEND TO MAIN AGENT > "
		if m.composeWorker != "" {
			label = " RESUME WORKER #" + m.composeWorker + " > "
		}
		if m.filtering {
			label = " FILTER > "
		}
		prompt = neon.Render(label) + m.input.View()
		if m.sending {
			prompt += " [sending…]"
		}
	}
	content := header + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(taskLine) + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(strings.Join(nav, " ")+clean(extra)) + "\n" + body + "\n" + prompt + "\n" + lipgloss.NewStyle().MaxWidth(m.width).Render(pink.Render(clean(m.notice))) + "\n" + footer
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m Model) loadHistory() tea.Cmd {
	if m.task == "" || !(m.tab == 0 || m.tab == 2) {
		return nil
	}
	task, worker := m.task, m.worker
	session := "main"
	if worker != "" {
		session = "intent:" + worker
	}
	before := int64(0)
	for _, a := range m.visibleActivities() {
		if (worker != "" && a.IntentID == worker) || (worker == "" && a.Worker == "mainagent") {
			if before == 0 || a.Seq < before {
				before = a.Seq
			}
		}
	}
	return func() tea.Msg {
		var out struct{ Items []Activity }
		err := m.client.JSON(m.ctx, "GET", "/api/exploration/activity/history"+taskQuery(task)+fmt.Sprintf("&session=%s&before=%d&limit=200", session, before), nil, &out)
		return historyMsg{task, worker, out.Items, err}
	}
}

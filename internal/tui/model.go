package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Aculnaj/aethercli/internal/api"
	"github.com/Aculnaj/aethercli/internal/config"
	"github.com/Aculnaj/aethercli/internal/session"
)

type Client interface {
	StreamChat(ctx context.Context, req api.ChatRequest, onDelta func(string) error) error
	Models(ctx context.Context) ([]api.Model, error)
}

type ClientFactory func(baseURL, apiKey string) Client

type Options struct {
	ConfigPath    string
	Config        config.Config
	APIKey        string
	Model         string
	Resume        bool
	SessionID     string
	In            io.Reader
	Out           io.Writer
	Err           io.Writer
	Store         *session.Store
	ClientFactory ClientFactory
	Now           func() time.Time
}

type mode int

const (
	modeChat mode = iota
	modeModels
	modeSessions
	modeHelp
)

type slashCommand struct {
	name string
	arg  string
}

type Model struct {
	activeModel   string
	client        Client
	store         session.Store
	current       session.Session
	sessionID     string
	messages      []session.Message
	models        []api.Model
	modelCursor   int
	sessions      []session.Summary
	sessionCursor int
	mode          mode
	status        string
	input         textinput.Model
	viewport      viewport.Model
	width         int
	height        int
	streamCh      chan tea.Msg
	streaming     bool
	quitting      bool
}

type streamDeltaMsg struct {
	delta string
}

type streamDoneMsg struct {
	prompt string
	answer string
	err    error
}

func Run(ctx context.Context, opts Options) error {
	if opts.Store == nil {
		store, err := session.NewStore(opts.ConfigPath, opts.Now)
		if err != nil {
			return err
		}
		opts.Store = &store
	}
	model := NewModel(opts)
	if strings.TrimSpace(opts.SessionID) != "" {
		if err := model.resumeSession(opts.SessionID); err != nil {
			return err
		}
	} else if opts.Resume {
		item, err := model.store.Latest()
		if err != nil {
			return err
		}
		model.loadSession(item)
	}

	programOptions := []tea.ProgramOption{tea.WithContext(ctx)}
	if opts.In != nil {
		programOptions = append(programOptions, tea.WithInput(opts.In))
	}
	if opts.Out != nil {
		programOptions = append(programOptions, tea.WithOutput(opts.Out))
	}
	_, err := tea.NewProgram(model, programOptions...).Run()
	return err
}

func NewModel(opts Options) *Model {
	activeModel := strings.TrimSpace(opts.Model)
	if activeModel == "" {
		activeModel = strings.TrimSpace(opts.Config.DefaultModel)
	}
	input := textinput.New()
	input.Placeholder = "Message Aether or type /help"
	input.Prompt = "> "
	input.Focus()
	input.CharLimit = 0

	vp := viewport.New(80, 20)
	store := session.Store{}
	if opts.Store != nil {
		store = *opts.Store
	}
	clientFactory := opts.ClientFactory
	if clientFactory == nil {
		clientFactory = func(baseURL, apiKey string) Client {
			return api.NewClient(api.ClientOptions{BaseURL: baseURL, APIKey: apiKey})
		}
	}
	cfg := configWithDefaults(opts.Config)

	model := &Model{
		activeModel: activeModel,
		client:      clientFactory(cfg.BaseURL, opts.APIKey),
		store:       store,
		mode:        modeChat,
		status:      "Ready. Type /help for commands.",
		input:       input,
		viewport:    vp,
		width:       80,
		height:      24,
		streamCh:    make(chan tea.Msg, 64),
	}
	model.refreshViewport()
	return model
}

func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = max(20, msg.Width)
		m.viewport.Height = max(5, msg.Height-5)
		m.refreshViewport()
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	case streamDeltaMsg:
		m.appendAssistantDelta(msg.delta)
		m.refreshViewport()
		return m, waitForStream(m.streamCh)
	case streamDoneMsg:
		m.streaming = false
		if msg.err != nil {
			m.removeStreamingAssistant()
			m.status = msg.err.Error()
			m.refreshViewport()
			return m, nil
		}
		if err := m.saveCompletedTurn(msg.prompt, msg.answer); err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.status = "Saved."
		m.refreshViewport()
		return m, nil
	}

	var cmd tea.Cmd
	if m.mode == modeChat && !m.streaming {
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
}

func (m *Model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		if m.mode != modeChat {
			m.mode = modeChat
			m.status = "Ready."
			return m, nil
		}
		m.input.SetValue("")
		return m, nil
	}

	switch m.mode {
	case modeModels:
		return m.updateModelsKey(msg)
	case modeSessions:
		return m.updateSessionsKey(msg)
	case modeHelp:
		if msg.String() == "enter" {
			m.mode = modeChat
			m.status = "Ready."
		}
		return m, nil
	}

	switch msg.String() {
	case "enter":
		value := strings.TrimSpace(m.input.Value())
		if value == "" || m.streaming {
			return m, nil
		}
		m.input.SetValue("")
		if strings.HasPrefix(value, "/") {
			return m, m.handleSlashCommand(value)
		}
		return m, m.beginStream(value)
	case "ctrl+n":
		m.input.SetValue(m.input.Value() + "\n")
		m.input.CursorEnd()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) updateModelsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.modelCursor > 0 {
			m.modelCursor--
		}
	case "down", "j":
		if m.modelCursor < len(m.models)-1 {
			m.modelCursor++
		}
	case "enter":
		m.selectCurrentModel()
	}
	return m, nil
}

func (m *Model) updateSessionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.sessionCursor > 0 {
			m.sessionCursor--
		}
	case "down", "j":
		if m.sessionCursor < len(m.sessions)-1 {
			m.sessionCursor++
		}
	case "enter":
		if len(m.sessions) > 0 {
			if err := m.resumeSession(m.sessions[m.sessionCursor].ID); err != nil {
				m.status = err.Error()
			}
		}
	}
	return m, nil
}

func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	header := headerStyle.Width(m.width).Render(fmt.Sprintf("Aether Chat  model=%s  session=%s", m.activeModel, m.displaySessionID()))
	body := m.viewport.View()
	if m.mode == modeModels {
		body = m.renderModels()
	} else if m.mode == modeSessions {
		body = m.renderSessions()
	} else if m.mode == modeHelp {
		body = m.renderHelp()
	}
	status := statusStyle.Width(m.width).Render(m.status)
	input := m.input.View()
	if m.streaming {
		input = mutedStyle.Render("Streaming response...")
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status, input)
}

func (m *Model) handleSlashCommand(input string) tea.Cmd {
	cmd := parseSlashCommand(input)
	switch cmd.name {
	case "models":
		if err := m.showModels(context.Background()); err != nil {
			m.status = err.Error()
		}
	case "model":
		if strings.TrimSpace(cmd.arg) == "" {
			m.status = "Usage: /model <id>"
			return nil
		}
		m.activeModel = strings.TrimSpace(cmd.arg)
		m.status = "Model set to " + m.activeModel
	case "sessions":
		if err := m.showSessions(); err != nil {
			m.status = err.Error()
		}
	case "resume":
		if strings.TrimSpace(cmd.arg) == "" {
			m.status = "Usage: /resume <id>"
			return nil
		}
		if err := m.resumeSession(cmd.arg); err != nil {
			m.status = err.Error()
		}
	case "new":
		if err := m.newSession(); err != nil {
			m.status = err.Error()
		}
	case "clear":
		m.clearVisible()
	case "help":
		m.showHelp()
	case "quit":
		m.requestQuit()
		return tea.Quit
	default:
		m.status = "Unknown command. Type /help."
	}
	m.refreshViewport()
	return nil
}

func parseSlashCommand(input string) slashCommand {
	input = strings.TrimSpace(strings.TrimPrefix(input, "/"))
	if input == "" {
		return slashCommand{}
	}
	name, arg, _ := strings.Cut(input, " ")
	return slashCommand{name: strings.ToLower(strings.TrimSpace(name)), arg: strings.TrimSpace(arg)}
}

func (m *Model) showModels(ctx context.Context) error {
	models, err := m.client.Models(ctx)
	if err != nil {
		return err
	}
	m.models = api.FilterChatModels(models)
	m.modelCursor = 0
	m.mode = modeModels
	m.status = "Select a chat model with Enter."
	if len(m.models) == 0 {
		m.status = "No chat models found."
	}
	return nil
}

func (m *Model) selectCurrentModel() {
	if len(m.models) == 0 {
		m.mode = modeChat
		return
	}
	m.activeModel = m.models[m.modelCursor].ID
	m.mode = modeChat
	m.status = "Model set to " + m.activeModel
}

func (m *Model) showSessions() error {
	summaries, err := m.store.List()
	if err != nil {
		return err
	}
	m.sessions = summaries
	m.sessionCursor = 0
	m.mode = modeSessions
	m.status = "Select a session with Enter."
	if len(m.sessions) == 0 {
		m.status = "No saved sessions."
	}
	return nil
}

func (m *Model) resumeSession(id string) error {
	item, err := m.store.Load(strings.TrimSpace(id))
	if err != nil {
		return err
	}
	m.loadSession(item)
	return nil
}

func (m *Model) loadSession(item session.Session) {
	m.current = item
	m.sessionID = item.ID
	if strings.TrimSpace(item.Model) != "" {
		m.activeModel = item.Model
	}
	m.messages = append([]session.Message(nil), item.Messages...)
	m.mode = modeChat
	m.status = "Loaded session " + item.ID
	m.refreshViewport()
}

func (m *Model) newSession() error {
	m.current = session.Session{}
	m.sessionID = ""
	m.messages = nil
	m.mode = modeChat
	m.status = "Started a new session."
	m.refreshViewport()
	return nil
}

func (m *Model) clearVisible() {
	m.messages = nil
	m.mode = modeChat
	m.status = "Cleared visible chat."
	m.refreshViewport()
}

func (m *Model) showHelp() {
	m.mode = modeHelp
	m.status = "/models /model <id> /sessions /resume <id> /new /clear /help /quit"
}

func (m *Model) requestQuit() {
	m.quitting = true
}

func (m *Model) sendPrompt(ctx context.Context, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	userMessage := session.Message{Role: "user", Content: prompt}
	m.messages = append(m.messages, userMessage)
	requestPrompt := conversationPrompt(m.current.Messages, prompt)

	var assistant strings.Builder
	err := m.client.StreamChat(ctx, api.ChatRequest{Model: m.activeModel, Prompt: requestPrompt}, func(delta string) error {
		assistant.WriteString(delta)
		return nil
	})
	if err != nil {
		m.status = err.Error()
		return err
	}
	answer := assistant.String()
	m.messages = append(m.messages, session.Message{Role: "assistant", Content: answer})
	if err := m.saveCompletedTurn(prompt, answer); err != nil {
		m.status = err.Error()
		return err
	}
	m.status = "Saved."
	m.refreshViewport()
	return nil
}

func (m *Model) beginStream(prompt string) tea.Cmd {
	m.streaming = true
	m.status = "Streaming..."
	m.messages = append(m.messages, session.Message{Role: "user", Content: prompt})
	requestPrompt := conversationPrompt(m.current.Messages, prompt)
	activeModel := m.activeModel
	client := m.client
	ch := m.streamCh

	return func() tea.Msg {
		go func() {
			var assistant strings.Builder
			err := client.StreamChat(context.Background(), api.ChatRequest{Model: activeModel, Prompt: requestPrompt}, func(delta string) error {
				assistant.WriteString(delta)
				ch <- streamDeltaMsg{delta: delta}
				return nil
			})
			ch <- streamDoneMsg{prompt: prompt, answer: assistant.String(), err: err}
		}()
		return waitForStream(ch)()
	}
}

func waitForStream(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func (m *Model) appendAssistantDelta(delta string) {
	if delta == "" {
		return
	}
	last := len(m.messages) - 1
	if last < 0 || m.messages[last].Role != "assistant" {
		m.messages = append(m.messages, session.Message{Role: "assistant", Content: delta})
		return
	}
	m.messages[last].Content += delta
}

func (m *Model) removeStreamingAssistant() {
	if len(m.messages) == 0 {
		return
	}
	last := len(m.messages) - 1
	if m.messages[last].Role == "assistant" {
		m.messages = m.messages[:last]
	}
}

func (m *Model) saveCompletedTurn(prompt, answer string) error {
	if strings.TrimSpace(m.current.ID) == "" {
		item, err := m.store.New(m.activeModel, prompt)
		if err != nil {
			return err
		}
		m.current = item
		m.sessionID = item.ID
	}
	m.current.Model = m.activeModel
	m.store.Append(&m.current, "user", prompt)
	m.store.Append(&m.current, "assistant", answer)
	if m.current.Title == "" {
		m.current.Title = prompt
	}
	return m.store.Save(m.current)
}

func conversationPrompt(messages []session.Message, currentPrompt string) string {
	if len(messages) == 0 {
		return currentPrompt
	}
	var b strings.Builder
	b.WriteString("Continue the conversation using the transcript below.\n\nTranscript:\n")
	for _, message := range messages {
		switch message.Role {
		case "user":
			b.WriteString("User: ")
		case "assistant":
			b.WriteString("Assistant: ")
		default:
			b.WriteString(roleLabel(message.Role))
			b.WriteString(": ")
		}
		b.WriteString(message.Content)
		if !strings.HasSuffix(message.Content, "\n") {
			b.WriteByte('\n')
		}
	}
	b.WriteString("\nCurrent user message:\nUser: ")
	b.WriteString(currentPrompt)
	return b.String()
}

func roleLabel(role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		return "Message"
	}
	return strings.ToUpper(role[:1]) + role[1:]
}

func (m *Model) refreshViewport() {
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

func (m *Model) renderMessages() string {
	if len(m.messages) == 0 {
		return mutedStyle.Render("No messages yet.")
	}
	var b strings.Builder
	for _, message := range m.messages {
		label := "User"
		style := userStyle
		if message.Role == "assistant" {
			label = "Aether"
			style = assistantStyle
		}
		b.WriteString(style.Render(label))
		b.WriteString("\n")
		b.WriteString(message.Content)
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) renderModels() string {
	if len(m.models) == 0 {
		return mutedStyle.Render("No chat models found.")
	}
	var b strings.Builder
	for i, model := range m.models {
		cursor := " "
		if i == m.modelCursor {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %s  %s  %s\n", cursor, model.ID, model.OwnedBy, model.Context)
	}
	return b.String()
}

func (m *Model) renderSessions() string {
	if len(m.sessions) == 0 {
		return mutedStyle.Render("No saved sessions.")
	}
	var b strings.Builder
	for i, item := range m.sessions {
		cursor := " "
		if i == m.sessionCursor {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s %s  %s  %d messages  %s\n", cursor, item.ID, item.Model, item.Messages, item.Title)
	}
	return b.String()
}

func (m *Model) renderHelp() string {
	return strings.Join([]string{
		"/models        list chat models and select one",
		"/model <id>    set active model",
		"/sessions      list saved sessions",
		"/resume <id>   load a saved session",
		"/new           start a new session",
		"/clear         clear visible messages",
		"/help          show this help",
		"/quit          exit",
		"",
		"Enter sends. Ctrl+N inserts a newline. Esc closes overlays.",
	}, "\n")
}

func (m *Model) displaySessionID() string {
	if strings.TrimSpace(m.sessionID) == "" {
		return "new"
	}
	return m.sessionID
}

func configWithDefaults(cfg config.Config) config.Config {
	if cfg.BaseURL == "" {
		cfg.BaseURL = config.DefaultBaseURL
	}
	return cfg
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var (
	headerStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("62")).Padding(0, 1)
	statusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	mutedStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	userStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	assistantStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
)

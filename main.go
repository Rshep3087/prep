package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// CommandRunner runs commands.
type CommandRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner implements CommandRunner using os/exec.
type ExecRunner struct{}

// ErrNoCommand is returned when no command is provided to Run.
var ErrNoCommand = errors.New("no command provided")

// Run executes a command and returns its output.
func (ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, ErrNoCommand
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // args are controlled by the application
	return cmd.Output()
}

// MessageSender abstracts the ability to send messages.
type MessageSender interface {
	Send(msg tea.Msg)
}

// Task represents a mise task from JSON output.
type Task struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Hide        bool     `json:"hide"`
	Run         []string `json:"run"`
}

// Tool represents a mise tool (parsed from mise ls --json).
type Tool struct {
	Name             string
	Version          string
	RequestedVersion string
	Source           string
	Active           bool
}

// miseToolEntry represents a single tool version entry from mise ls --json.
type miseToolEntry struct {
	Version          string `json:"version"`
	RequestedVersion string `json:"requested_version"`
	Source           *struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
	Active bool `json:"active"`
}

// tasksLoadedMsg is sent when tasks are loaded from mise.
type tasksLoadedMsg struct {
	tasks []Task
	err   error
}

// toolsLoadedMsg is sent when tools are loaded from mise.
type toolsLoadedMsg struct {
	tools []Tool
	err   error
}

// EnvVar represents a mise environment variable.
type EnvVar struct {
	Name   string
	Value  string
	Masked bool
}

// envVarsLoadedMsg is sent when environment variables are loaded from mise.
type envVarsLoadedMsg struct {
	envVars []EnvVar
	err     error
}

// taskOutputMsg is sent when a running task produces output.
type taskOutputMsg struct {
	line string
}

// taskDoneMsg is sent when a task finishes executing.
type taskDoneMsg struct {
	err error
}

// loadJSON is a generic loader that runs a command and unmarshals JSON.
func loadJSON[T any](
	ctx context.Context,
	runner CommandRunner,
	args []string,
	transform func(T) tea.Msg,
	errMsg func(error) tea.Msg,
) tea.Cmd {
	return func() tea.Msg {
		output, err := runner.Run(ctx, args...)
		if err != nil {
			return errMsg(fmt.Errorf("failed to execute %s: %w", args[0], err))
		}

		var data T
		err = json.Unmarshal(output, &data)
		if err != nil {
			return errMsg(fmt.Errorf("failed to parse JSON: %w", err))
		}

		return transform(data)
	}
}

// loadMiseTasks returns a Cmd that loads tasks asynchronously.
func loadMiseTasks(ctx context.Context, runner CommandRunner) tea.Cmd {
	return loadJSON(ctx, runner, []string{"mise", "tasks", "--json"},
		func(tasks []Task) tea.Msg { return tasksLoadedMsg{tasks: tasks} },
		func(err error) tea.Msg { return tasksLoadedMsg{err: err} },
	)
}

// loadMiseTools returns a Cmd that loads tools asynchronously.
func loadMiseTools(ctx context.Context, runner CommandRunner) tea.Cmd {
	return loadJSON(ctx, runner, []string{"mise", "ls", "--json"},
		func(rawTools map[string][]miseToolEntry) tea.Msg {
			var tools []Tool
			for name, entries := range rawTools {
				for _, entry := range entries {
					if entry.Active {
						source := ""
						if entry.Source != nil {
							source = entry.Source.Type
						}
						tools = append(tools, Tool{
							Name:             name,
							Version:          entry.Version,
							RequestedVersion: entry.RequestedVersion,
							Source:           source,
							Active:           entry.Active,
						})
					}
				}
			}
			return toolsLoadedMsg{tools: tools}
		},
		func(err error) tea.Msg { return toolsLoadedMsg{err: err} },
	)
}

// loadMiseEnvVars returns a Cmd that loads environment variables asynchronously.
func loadMiseEnvVars(ctx context.Context, runner CommandRunner) tea.Cmd {
	return loadJSON(ctx, runner, []string{"mise", "env", "--json"},
		func(rawEnvVars map[string]string) tea.Msg {
			var envVars []EnvVar
			for name, value := range rawEnvVars {
				envVars = append(envVars, EnvVar{
					Name:   name,
					Value:  value,
					Masked: true,
				})
			}
			return envVarsLoadedMsg{envVars: envVars}
		},
		func(err error) tea.Msg { return envVarsLoadedMsg{err: err} },
	)
}

// runTask executes a mise task and streams output back to the TUI.
func runTask(ctx context.Context, taskName string, sender MessageSender) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "mise", "run", taskName)

		// Create pipes for stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return taskDoneMsg{err: fmt.Errorf("failed to create stdout pipe: %w", err)}
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return taskDoneMsg{err: fmt.Errorf("failed to create stderr pipe: %w", err)}
		}

		if startErr := cmd.Start(); startErr != nil {
			return taskDoneMsg{err: fmt.Errorf("failed to start task: %w", startErr)}
		}

		// Stream stdout
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				sender.Send(taskOutputMsg{line: scanner.Text()})
			}
		}()

		// Stream stderr
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				sender.Send(taskOutputMsg{line: scanner.Text()})
			}
		}()

		// Wait for the command to finish
		err = cmd.Wait()
		return taskDoneMsg{err: err}
	}
}

// maskValue returns a masked representation of a value.
func maskValue(value string) string {
	if len(value) == 0 {
		return ""
	}
	// Use a consistent mask length for cleaner display
	return "●●●●●●●●"
}

// focus constants.
const (
	focusTasks = iota
	focusTools
	focusEnvVars
	focusSectionCount // total number of focus sections for cycling
)

const (
	// Column width constants.
	colWidthName        = 20
	colWidthDescription = 60
	colWidthVersion     = 15
	colWidthValue       = 50
	colWidthEnvName     = 30

	// Table width constants.
	tableWidthWide   = 82
	tableWidthNarrow = 52
)

// tableConfig holds configuration for creating a table.
type tableConfig struct {
	columns []table.Column
	width   int
}

// styles holds the UI styles used throughout the application.
type styles struct {
	title    lipgloss.Style
	dimTitle lipgloss.Style
	help     lipgloss.Style
	err      lipgloss.Style
	success  lipgloss.Style
}

// newStyles creates the default UI styles.
func newStyles() styles {
	return styles{
		title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255")),
		dimTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241")),
		help:     lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		err:      lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		success:  lipgloss.NewStyle().Foreground(lipgloss.Color("82")),
	}
}

// renderTitle renders a section title with focus state.
func (s styles) renderTitle(name string, focused bool) string {
	if focused {
		return s.title.Render(name)
	}
	return s.dimTitle.Render(name)
}

// getTasksTableConfig returns the table configuration for tasks.
func getTasksTableConfig() tableConfig {
	return tableConfig{
		columns: []table.Column{
			{Title: "Name", Width: colWidthName},
			{Title: "Description", Width: colWidthDescription},
		},
		width: tableWidthWide,
	}
}

// getToolsTableConfig returns the table configuration for tools.
func getToolsTableConfig() tableConfig {
	return tableConfig{
		columns: []table.Column{
			{Title: "Name", Width: colWidthName},
			{Title: "Version", Width: colWidthVersion},
			{Title: "Requested", Width: colWidthVersion},
		},
		width: tableWidthNarrow,
	}
}

// getEnvVarsTableConfig returns the table configuration for env vars.
func getEnvVarsTableConfig() tableConfig {
	return tableConfig{
		columns: []table.Column{
			{Title: "Name", Width: colWidthEnvName},
			{Title: "Value", Width: colWidthValue},
		},
		width: tableWidthWide,
	}
}

// newTable creates a table with the given configuration.
func newTable(cfg tableConfig, rows []table.Row, focused bool) table.Model {
	const headerOffset = 2
	t := table.New(
		table.WithColumns(cfg.columns),
		table.WithRows(rows),
		table.WithFocused(focused),
		table.WithStyles(tableStyles()),
		table.WithWidth(cfg.width),
	)
	t.SetHeight(len(rows) + headerOffset)
	return t
}

type model struct {
	tasksTable     table.Model
	toolsTable     table.Model
	envVarsTable   table.Model
	tasks          []Task
	tools          []Tool
	envVars        []EnvVar
	focus          int // focusTasks, focusTools, or focusEnvVars
	tasksLoading   bool
	toolsLoading   bool
	envVarsLoading bool
	err            error

	// Task execution state
	showOutput   bool               // whether to show the output viewport
	runningTask  string             // name of the task being run
	taskRunning  bool               // whether a task is currently running
	taskErr      error              // error from task execution (if any)
	output       []string           // output lines from the task
	viewport     viewport.Model     // scrollable viewport for output
	cancelFunc   context.CancelFunc // to cancel the running task
	windowWidth  int
	windowHeight int

	// Dependencies (DIP)
	runner CommandRunner // for running commands
	sender MessageSender // for sending messages to the program
	styles styles        // UI styles
}

func (m model) Init() tea.Cmd {
	ctx := context.Background()
	return tea.Batch(
		loadMiseTasks(ctx, m.runner),
		loadMiseTools(ctx, m.runner),
		loadMiseEnvVars(ctx, m.runner),
	)
}

// Table layout constants for resize calculations.
const (
	tablePadding       = 4 // padding for table borders
	columnPadding      = 5 // padding between columns
	toolsColumnPadding = 8 // extra padding for tools table
)

// updateTableWidths adjusts table widths based on the current terminal width.
func updateTableWidths(m model) model {
	if m.windowWidth == 0 {
		return m
	}

	// Use available width (with some padding for borders)
	availableWidth := m.windowWidth - tablePadding

	// Tasks table: Name + Description columns
	tasksNameWidth := colWidthName
	tasksDescWidth := max(availableWidth-tasksNameWidth-columnPadding, colWidthDescription)
	m.tasksTable.SetColumns([]table.Column{
		{Title: "Name", Width: tasksNameWidth},
		{Title: "Description", Width: tasksDescWidth},
	})
	m.tasksTable.SetWidth(availableWidth)

	// Tools table: Name + Version + Requested columns
	toolsWidth := min(colWidthName+colWidthVersion*2+toolsColumnPadding, availableWidth)
	m.toolsTable.SetWidth(toolsWidth)

	// EnvVars table: Name + Value columns
	envNameWidth := colWidthEnvName
	envValueWidth := max(availableWidth-envNameWidth-columnPadding, colWidthValue)
	m.envVarsTable.SetColumns([]table.Column{
		{Title: "Name", Width: envNameWidth},
		{Title: "Value", Width: envValueWidth},
	})
	m.envVarsTable.SetWidth(availableWidth)

	return m
}

// handleTasksLoaded processes the tasksLoadedMsg and initializes the tasks table.
func handleTasksLoaded(m model, msg tasksLoadedMsg) model {
	if msg.err != nil {
		log.Printf("error loading tasks: %v", msg.err)
		m.err = msg.err
		m.tasksLoading = false
		return m
	}

	log.Printf("loaded %d tasks", len(msg.tasks))
	m.tasks = msg.tasks
	m.tasksLoading = false

	rows := make([]table.Row, 0, len(m.tasks))
	for _, task := range m.tasks {
		rows = append(rows, table.Row{task.Name, task.Description})
	}
	m.tasksTable = newTable(getTasksTableConfig(), rows, m.focus == focusTasks)
	return m
}

// handleToolsLoaded processes the toolsLoadedMsg and initializes the tools table.
func handleToolsLoaded(m model, msg toolsLoadedMsg) model {
	if msg.err != nil {
		log.Printf("error loading tools: %v", msg.err)
		m.err = msg.err
		m.toolsLoading = false
		return m
	}

	log.Printf("loaded %d tools", len(msg.tools))
	m.tools = msg.tools
	m.toolsLoading = false

	rows := make([]table.Row, 0, len(m.tools))
	for _, tool := range m.tools {
		rows = append(rows, table.Row{tool.Name, tool.Version, tool.RequestedVersion})
	}
	m.toolsTable = newTable(getToolsTableConfig(), rows, m.focus == focusTools)
	return m
}

// handleEnvVarsLoaded processes the envVarsLoadedMsg and initializes the env vars table.
func handleEnvVarsLoaded(m model, msg envVarsLoadedMsg) model {
	if msg.err != nil {
		log.Printf("error loading env vars: %v", msg.err)
		m.err = msg.err
		m.envVarsLoading = false
		return m
	}

	log.Printf("loaded %d env vars", len(msg.envVars))
	m.envVars = msg.envVars
	m.envVarsLoading = false

	rows := make([]table.Row, 0, len(m.envVars))
	for _, ev := range m.envVars {
		displayValue := maskValue(ev.Value)
		if !ev.Masked {
			displayValue = ev.Value
		}
		rows = append(rows, table.Row{ev.Name, displayValue})
	}
	m.envVarsTable = newTable(getEnvVarsTableConfig(), rows, m.focus == focusEnvVars)
	return m
}

// Update is called when a message is received. Use it to inspect messages
// and, in response, update the model and/or send a command.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Handle keys differently based on whether we're showing output
		if m.showOutput {
			return m.handleOutputKeys(msg)
		}
		newModel, newCmd, handled := m.handleMainKeys(msg)
		if handled {
			return newModel, newCmd
		}
		m = newModel
		// Fall through to let tables handle navigation keys

	case taskOutputMsg:
		m.output = append(m.output, msg.line)
		m.viewport.SetContent(strings.Join(m.output, "\n"))
		m.viewport.GotoBottom()
		return m, nil

	case taskDoneMsg:
		m.taskRunning = false
		m.taskErr = msg.err
		m.cancelFunc = nil
		if msg.err != nil {
			log.Printf("task %s finished with error: %v", m.runningTask, msg.err)
		} else {
			log.Printf("task %s finished successfully", m.runningTask)
		}
		return m, nil

	case tasksLoadedMsg:
		return handleTasksLoaded(m, msg), nil

	case toolsLoadedMsg:
		return handleToolsLoaded(m, msg), nil

	case envVarsLoadedMsg:
		return handleEnvVarsLoaded(m, msg), nil

	case tea.WindowSizeMsg:
		m.windowWidth = msg.Width
		m.windowHeight = msg.Height
		if m.showOutput {
			m.viewport = viewport.New(
				viewport.WithWidth(msg.Width),
				viewport.WithHeight(msg.Height-viewportHeaderFooterHeight),
			)
			m.viewport.SetContent(strings.Join(m.output, "\n"))
		} else {
			// Update table widths based on terminal width
			m = updateTableWidths(m)
		}
		return m, nil
	}

	// Update the focused table with any other messages (only when not showing output)
	if !m.showOutput && !m.tasksLoading && !m.toolsLoading && !m.envVarsLoading && m.err == nil {
		switch m.focus {
		case focusTasks:
			m.tasksTable, cmd = m.tasksTable.Update(msg)
		case focusTools:
			m.toolsTable, cmd = m.toolsTable.Update(msg)
		case focusEnvVars:
			m.envVarsTable, cmd = m.envVarsTable.Update(msg)
		}
	}

	// Update viewport when showing output
	if m.showOutput {
		m.viewport, cmd = m.viewport.Update(msg)
	}

	return m, cmd
}

// handleMainKeys handles key presses in the main view (task list).
// Returns (model, cmd, handled) where handled indicates if the key was consumed.
func (m model) handleMainKeys(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		return m, tea.Quit, true
	case "enter":
		// Only run tasks when focused on tasks table
		if m.focus == focusTasks && len(m.tasks) > 0 {
			selectedRow := m.tasksTable.SelectedRow()
			if selectedRow != nil {
				taskName := selectedRow[0]
				newModel, cmd := m.startTask(taskName)
				return newModel, cmd, true
			}
		}
		return m, nil, true
	case "tab":
		// Cycle focus: tasks -> tools -> envVars -> tasks
		m.tasksTable.Blur()
		m.toolsTable.Blur()
		m.envVarsTable.Blur()
		m.focus = (m.focus + 1) % focusSectionCount
		switch m.focus {
		case focusTasks:
			m.tasksTable.Focus()
		case focusTools:
			m.toolsTable.Focus()
		case focusEnvVars:
			m.envVarsTable.Focus()
		}
		return m, nil, true
	}
	// Key not handled - let tables process it
	return m, nil, false
}

// handleOutputKeys handles key presses in the output view.
func (m model) handleOutputKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		// Close output view (only if task is not running)
		if !m.taskRunning {
			m.showOutput = false
			m.output = nil
			m.runningTask = ""
			m.taskErr = nil
			return m, nil
		}
		return m, nil
	case "ctrl+c":
		// Cancel running task
		if m.taskRunning && m.cancelFunc != nil {
			log.Printf("cancelling task %s", m.runningTask)
			m.cancelFunc()
			return m, nil
		}
		// If not running, quit the app
		if !m.taskRunning {
			return m, tea.Quit
		}
		return m, nil
	}

	// Pass other keys to viewport for scrolling
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// viewportHeaderFooterHeight is the space reserved for header and footer in output view.
const viewportHeaderFooterHeight = 4

// startTask initializes and starts a task execution.
func (m model) startTask(taskName string) (model, tea.Cmd) {
	log.Printf("starting task: %s", taskName)

	// Create cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Initialize viewport
	width := m.windowWidth
	height := m.windowHeight
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}

	m.viewport = viewport.New(
		viewport.WithWidth(width),
		viewport.WithHeight(height-viewportHeaderFooterHeight),
	)

	m.showOutput = true
	m.runningTask = taskName
	m.taskRunning = true
	m.taskErr = nil
	m.output = []string{}
	m.cancelFunc = cancel

	return m, runTask(ctx, taskName, m.sender)
}

// tableStyles returns the default table styles.
func tableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(false)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)
	s.Cell = s.Cell.
		Foreground(lipgloss.Color("255"))
	return s
}

// View renders the program's UI, which can be a string or a [Layer]. The
// view is rendered after every Update.
func (m model) View() tea.View {
	// Show output view if running or viewing task output
	if m.showOutput {
		return m.renderOutputView()
	}

	if m.tasksLoading || m.toolsLoading || m.envVarsLoading {
		return tea.NewView("Loading mise data...\n")
	}

	if m.err != nil {
		return tea.NewView(fmt.Sprintf("Error: %v\n\nPress q to quit.\n", m.err))
	}

	// Build sections using shared renderTitle helper
	tasksTitle := m.styles.renderTitle("Tasks", m.focus == focusTasks)
	toolsTitle := m.styles.renderTitle("Tools", m.focus == focusTools)
	envVarsTitle := m.styles.renderTitle("Environment Variables", m.focus == focusEnvVars)
	help := m.styles.help.Render("Tab to switch • ↑/↓ to navigate • Enter to run task • q to quit")

	// Build the view using JoinVertical
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		tasksTitle,
		m.tasksTable.View(),
		"",
		toolsTitle,
		m.toolsTable.View(),
		"",
		envVarsTitle,
		m.envVarsTable.View(),
		"",
		help,
	)

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// renderOutputView renders the task output viewport.
func (m model) renderOutputView() tea.View {
	// Header with task name and status
	title := m.styles.title.Render(fmt.Sprintf("Task: %s", m.runningTask))

	var status string
	switch {
	case m.taskRunning:
		status = m.styles.dimTitle.Render("● Running...")
	case m.taskErr != nil:
		status = m.styles.err.Render(fmt.Sprintf("✗ Failed: %v", m.taskErr))
	default:
		status = m.styles.success.Render("✓ Completed")
	}

	header := lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", status)

	// Help text
	var help string
	if m.taskRunning {
		help = m.styles.help.Render("Ctrl+C to cancel • ↑/↓ to scroll")
	} else {
		help = m.styles.help.Render("Esc/q to close • ↑/↓ to scroll • Ctrl+C to quit")
	}

	// Build the view
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		m.viewport.View(),
		"",
		help,
	)

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func run(_ context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	debug := fs.Bool("debug", false, "enable debug logging to debug.log")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if *debug {
		lf, err := tea.LogToFile("debug.log", "debug")
		if err != nil {
			return fmt.Errorf("setup logging to file: %w", err)
		}
		defer func() { _ = lf.Close() }()
		log.Println("debug logging enabled")
	} else {
		log.SetOutput(io.Discard)
	}

	m := &model{
		tasksLoading:   true,
		toolsLoading:   true,
		envVarsLoading: true,
		runner:         ExecRunner{},
		styles:         newStyles(),
	}
	program := tea.NewProgram(m, tea.WithInput(stdin), tea.WithOutput(stdout))
	m.sender = program // *tea.Program implements MessageSender
	_, err := program.Run()
	return err
}

func main() {
	ctx := context.Background()
	err := run(ctx, os.Args, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

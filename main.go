package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

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

// loadMiseTasks returns a Cmd that loads tasks asynchronously.
func loadMiseTasks(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "mise", "tasks", "--json")
		output, err := cmd.Output()
		if err != nil {
			return tasksLoadedMsg{err: fmt.Errorf("failed to execute mise tasks: %w", err)}
		}

		var tasks []Task
		err = json.Unmarshal(output, &tasks)
		if err != nil {
			return tasksLoadedMsg{err: fmt.Errorf("failed to parse mise tasks JSON: %w", err)}
		}

		return tasksLoadedMsg{tasks: tasks}
	}
}

// loadMiseTools returns a Cmd that loads tools asynchronously.
func loadMiseTools(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "mise", "ls", "--json")
		output, err := cmd.Output()
		if err != nil {
			return toolsLoadedMsg{err: fmt.Errorf("failed to execute mise ls: %w", err)}
		}

		// Parse the nested JSON structure: map[toolName][]miseToolEntry
		var rawTools map[string][]miseToolEntry
		err = json.Unmarshal(output, &rawTools)
		if err != nil {
			return toolsLoadedMsg{err: fmt.Errorf("failed to parse mise ls JSON: %w", err)}
		}

		// Convert to flat list, only including active tools
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
	}
}

// loadMiseEnvVars returns a Cmd that loads environment variables asynchronously.
func loadMiseEnvVars(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.CommandContext(ctx, "mise", "env", "--json")
		output, err := cmd.Output()
		if err != nil {
			return envVarsLoadedMsg{err: fmt.Errorf("failed to execute mise env: %w", err)}
		}

		// Parse the JSON structure: map[string]string
		var rawEnvVars map[string]string
		err = json.Unmarshal(output, &rawEnvVars)
		if err != nil {
			return envVarsLoadedMsg{err: fmt.Errorf("failed to parse mise env JSON: %w", err)}
		}

		// Convert to EnvVar slice with masked values by default
		var envVars []EnvVar
		for name, value := range rawEnvVars {
			envVars = append(envVars, EnvVar{
				Name:   name,
				Value:  value,
				Masked: true,
			})
		}

		return envVarsLoadedMsg{envVars: envVars}
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
}

func (m model) Init() tea.Cmd {
	ctx := context.Background()
	return tea.Batch(loadMiseTasks(ctx), loadMiseTools(ctx), loadMiseEnvVars(ctx))
}

// handleTasksLoaded processes the tasksLoadedMsg and initializes the tasks table.
func handleTasksLoaded(m model, msg tasksLoadedMsg) model {
	const (
		nameWidth        = 20
		descriptionWidth = 60
		tableWidth       = 82
		headerOffset     = 2
	)

	if msg.err != nil {
		log.Printf("error loading tasks: %v", msg.err)
		m.err = msg.err
		m.tasksLoading = false
		return m
	}

	log.Printf("loaded %d tasks", len(msg.tasks))
	m.tasks = msg.tasks
	m.tasksLoading = false

	columns := []table.Column{
		{Title: "Name", Width: nameWidth},
		{Title: "Description", Width: descriptionWidth},
	}

	rows := []table.Row{}
	for _, task := range m.tasks {
		rows = append(rows, table.Row{task.Name, task.Description})
	}

	s := tableStyles()
	m.tasksTable = table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(m.focus == focusTasks),
		table.WithStyles(s),
		table.WithWidth(tableWidth),
	)
	m.tasksTable.SetHeight(len(rows) + headerOffset)
	return m
}

// handleToolsLoaded processes the toolsLoadedMsg and initializes the tools table.
func handleToolsLoaded(m model, msg toolsLoadedMsg) model {
	const (
		nameWidth      = 20
		versionWidth   = 15
		requestedWidth = 15
		tableWidth     = 52
		headerOffset   = 2
	)

	if msg.err != nil {
		log.Printf("error loading tools: %v", msg.err)
		m.err = msg.err
		m.toolsLoading = false
		return m
	}

	log.Printf("loaded %d tools", len(msg.tools))
	m.tools = msg.tools
	m.toolsLoading = false

	columns := []table.Column{
		{Title: "Name", Width: nameWidth},
		{Title: "Version", Width: versionWidth},
		{Title: "Requested", Width: requestedWidth},
	}

	rows := []table.Row{}
	for _, tool := range m.tools {
		rows = append(rows, table.Row{tool.Name, tool.Version, tool.RequestedVersion})
	}

	s := tableStyles()
	m.toolsTable = table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(m.focus == focusTools),
		table.WithStyles(s),
		table.WithWidth(tableWidth),
	)
	m.toolsTable.SetHeight(len(rows) + headerOffset)
	return m
}

// handleEnvVarsLoaded processes the envVarsLoadedMsg and initializes the env vars table.
func handleEnvVarsLoaded(m model, msg envVarsLoadedMsg) model {
	const (
		nameWidth    = 30
		valueWidth   = 50
		tableWidth   = 82
		headerOffset = 2
	)

	if msg.err != nil {
		log.Printf("error loading env vars: %v", msg.err)
		m.err = msg.err
		m.envVarsLoading = false
		return m
	}

	log.Printf("loaded %d env vars", len(msg.envVars))
	m.envVars = msg.envVars
	m.envVarsLoading = false

	columns := []table.Column{
		{Title: "Name", Width: nameWidth},
		{Title: "Value", Width: valueWidth},
	}

	rows := []table.Row{}
	for _, ev := range m.envVars {
		displayValue := maskValue(ev.Value)
		if !ev.Masked {
			displayValue = ev.Value
		}
		rows = append(rows, table.Row{ev.Name, displayValue})
	}

	s := tableStyles()
	m.envVarsTable = table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(m.focus == focusEnvVars),
		table.WithStyles(s),
		table.WithWidth(tableWidth),
	)
	m.envVarsTable.SetHeight(len(rows) + headerOffset)
	return m
}

// Update is called when a message is received. Use it to inspect messages
// and, in response, update the model and/or send a command.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			// Future: execute selected task
			return m, tea.Quit
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
			return m, nil
		}

	case tasksLoadedMsg:
		return handleTasksLoaded(m, msg), nil

	case toolsLoadedMsg:
		return handleToolsLoaded(m, msg), nil

	case envVarsLoadedMsg:
		return handleEnvVarsLoaded(m, msg), nil

	case tea.WindowSizeMsg:
		// Handle window resize if needed
		return m, nil
	}

	// Update the focused table with any other messages
	if !m.tasksLoading && !m.toolsLoading && !m.envVarsLoading && m.err == nil {
		switch m.focus {
		case focusTasks:
			m.tasksTable, cmd = m.tasksTable.Update(msg)
		case focusTools:
			m.toolsTable, cmd = m.toolsTable.Update(msg)
		case focusEnvVars:
			m.envVarsTable, cmd = m.envVarsTable.Update(msg)
		}
	}

	return m, cmd
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
	if m.tasksLoading || m.toolsLoading || m.envVarsLoading {
		return tea.NewView("Loading mise data...\n")
	}

	if m.err != nil {
		return tea.NewView(fmt.Sprintf("Error: %v\n\nPress q to quit.\n", m.err))
	}

	// Styles
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	dimTitleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("241"))
	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	// Tasks section
	var tasksTitle string
	if m.focus == focusTasks {
		tasksTitle = titleStyle.Render("Tasks")
	} else {
		tasksTitle = dimTitleStyle.Render("Tasks")
	}
	tasksView := m.tasksTable.View()

	// Tools section
	var toolsTitle string
	if m.focus == focusTools {
		toolsTitle = titleStyle.Render("Tools")
	} else {
		toolsTitle = dimTitleStyle.Render("Tools")
	}
	toolsView := m.toolsTable.View()

	// Environment Variables section
	var envVarsTitle string
	if m.focus == focusEnvVars {
		envVarsTitle = titleStyle.Render("Environment Variables")
	} else {
		envVarsTitle = dimTitleStyle.Render("Environment Variables")
	}
	envVarsView := m.envVarsTable.View()

	help := helpStyle.Render("Tab to switch • ↑/↓ to navigate • Enter to select • q to quit")

	// Build the view using JoinVertical
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		tasksTitle,
		tasksView,
		"",
		toolsTitle,
		toolsView,
		"",
		envVarsTitle,
		envVarsView,
		"",
		help,
	)

	return tea.NewView(content)
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
	}
	program := tea.NewProgram(m, tea.WithInput(stdin), tea.WithOutput(stdout))
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

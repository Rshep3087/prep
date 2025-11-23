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

// Task represents a mise task from JSON output
type Task struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
	Source      string   `json:"source"`
	Hide        bool     `json:"hide"`
	Run         []string `json:"run"`
}

// Tool represents a mise tool (parsed from mise ls --json)
type Tool struct {
	Name             string
	Version          string
	RequestedVersion string
	Source           string
	Active           bool
}

// miseToolEntry represents a single tool version entry from mise ls --json
type miseToolEntry struct {
	Version          string `json:"version"`
	RequestedVersion string `json:"requested_version"`
	Source           *struct {
		Type string `json:"type"`
		Path string `json:"path"`
	} `json:"source"`
	Active bool `json:"active"`
}

// tasksLoadedMsg is sent when tasks are loaded from mise
type tasksLoadedMsg struct {
	tasks []Task
	err   error
}

// toolsLoadedMsg is sent when tools are loaded from mise
type toolsLoadedMsg struct {
	tools []Tool
	err   error
}

// loadMiseTasks is a Cmd that loads tasks asynchronously
func loadMiseTasks() tea.Msg {
	cmd := exec.Command("mise", "tasks", "--json")
	output, err := cmd.Output()
	if err != nil {
		return tasksLoadedMsg{err: fmt.Errorf("failed to execute mise tasks: %w", err)}
	}

	var tasks []Task
	if err := json.Unmarshal(output, &tasks); err != nil {
		return tasksLoadedMsg{err: fmt.Errorf("failed to parse mise tasks JSON: %w", err)}
	}

	return tasksLoadedMsg{tasks: tasks}
}

// loadMiseTools is a Cmd that loads tools asynchronously
func loadMiseTools() tea.Msg {
	cmd := exec.Command("mise", "ls", "--json")
	output, err := cmd.Output()
	if err != nil {
		return toolsLoadedMsg{err: fmt.Errorf("failed to execute mise ls: %w", err)}
	}

	// Parse the nested JSON structure: map[toolName][]miseToolEntry
	var rawTools map[string][]miseToolEntry
	if err := json.Unmarshal(output, &rawTools); err != nil {
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

// focus constants
const (
	focusTasks = iota
	focusTools
)

type model struct {
	tasksTable   table.Model
	toolsTable   table.Model
	tasks        []Task
	tools        []Tool
	focus        int // focusTasks or focusTools
	tasksLoading bool
	toolsLoading bool
	err          error
}

func (m model) Init() tea.Cmd {
	return tea.Batch(loadMiseTasks, loadMiseTools)
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
			// Toggle focus between tasks and tools
			if m.focus == focusTasks {
				m.focus = focusTools
				m.tasksTable.Blur()
				m.toolsTable.Focus()
			} else {
				m.focus = focusTasks
				m.tasksTable.Focus()
				m.toolsTable.Blur()
			}
			return m, nil
		}

	case tasksLoadedMsg:
		if msg.err != nil {
			log.Printf("error loading tasks: %v", msg.err)
			m.err = msg.err
			m.tasksLoading = false
			return m, nil
		}

		log.Printf("loaded %d tasks", len(msg.tasks))
		m.tasks = msg.tasks
		m.tasksLoading = false

		// Initialize table with loaded tasks
		columns := []table.Column{
			{Title: "Name", Width: 20},
			{Title: "Description", Width: 60},
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
			table.WithWidth(82),
		)
		m.tasksTable.SetHeight(len(rows) + 2)

		return m, nil

	case toolsLoadedMsg:
		if msg.err != nil {
			log.Printf("error loading tools: %v", msg.err)
			m.err = msg.err
			m.toolsLoading = false
			return m, nil
		}

		log.Printf("loaded %d tools", len(msg.tools))
		m.tools = msg.tools
		m.toolsLoading = false

		// Initialize table with loaded tools
		columns := []table.Column{
			{Title: "Name", Width: 20},
			{Title: "Version", Width: 15},
			{Title: "Requested", Width: 15},
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
			table.WithWidth(52),
		)
		m.toolsTable.SetHeight(len(rows) + 2)

		return m, nil

	case tea.WindowSizeMsg:
		// Handle window resize if needed
		return m, nil
	}

	// Update the focused table with any other messages
	if !m.tasksLoading && !m.toolsLoading && m.err == nil {
		if m.focus == focusTasks {
			m.tasksTable, cmd = m.tasksTable.Update(msg)
		} else {
			m.toolsTable, cmd = m.toolsTable.Update(msg)
		}
	}

	return m, cmd
}

// tableStyles returns the default table styles
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
	if m.tasksLoading || m.toolsLoading {
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
			return fmt.Errorf("setup logging to file: %v", err)
		}
		defer func() { _ = lf.Close() }()
		log.Println("debug logging enabled")
	} else {
		log.SetOutput(io.Discard)
	}

	m := &model{
		tasksLoading: true,
		toolsLoading: true,
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

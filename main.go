package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// tasksLoadedMsg is sent when tasks are loaded from mise
type tasksLoadedMsg struct {
	tasks []Task
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

type model struct {
	table   table.Model
	tasks   []Task
	loading bool
	err     error
}

func (m model) Init() tea.Cmd {
	return loadMiseTasks
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
		}

	case tasksLoadedMsg:
		if msg.err != nil {
			m.err = msg.err
			m.loading = false
			return m, nil
		}

		m.tasks = msg.tasks
		m.loading = false

		// Initialize table with loaded tasks
		columns := []table.Column{
			{Title: "Name", Width: 20},
			{Title: "Description", Width: 60},
		}

		rows := []table.Row{}
		for _, task := range m.tasks {
			rows = append(rows, table.Row{task.Name, task.Description})
		}

		m.table = table.New(
			table.WithColumns(columns),
			table.WithRows(rows),
			table.WithFocused(true),
			table.WithHeight(50),
		)

		// Set table styles
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
		m.table.SetStyles(s)

		return m, nil

	case tea.WindowSizeMsg:
		// Handle window resize if needed
		return m, nil
	}

	// Update the table with any other messages
	if !m.loading && m.err == nil {
		m.table, cmd = m.table.Update(msg)
	}

	return m, cmd
}

// View renders the program's UI, which can be a string or a [Layer]. The
// view is rendered after every Update.
func (m model) View() tea.View {
	if m.loading {
		return tea.NewView("Loading mise tasks...\n")
	}

	if m.err != nil {
		return tea.NewView(fmt.Sprintf("Error loading tasks: %v\n\nPress q to quit.\n", m.err))
	}

	if len(m.tasks) == 0 {
		return tea.NewView("No mise tasks found.\n\nPress q to quit.\n")
	}

	// Create lipgloss layers for better rendering
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	title := titleStyle.Render("Mise Tasks")
	help := helpStyle.Render("Use ↑/↓ to navigate • Enter to select • q to quit")

	// Build the view using JoinVertical
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		m.table.View(),
		"",
		help,
	)

	return tea.NewView(content)
}

func run(_ context.Context) error {
	m := &model{
		loading: true,
	}
	program := tea.NewProgram(m)
	_, err := program.Run()
	return err
}

func main() {
	ctx := context.Background()
	err := run(ctx)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

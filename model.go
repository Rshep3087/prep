package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/rshep3087/prep/internal/loader"
	"github.com/rshep3087/prep/internal/watcher"
)

// commandRunner runs commands.
type commandRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// execRunner implements commandRunner using os/exec.
type execRunner struct{}

// ErrNoCommand is returned when no command is provided to Run.
var ErrNoCommand = errors.New("no command provided")

// Run executes a command and returns its output.
func (execRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, ErrNoCommand
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // args are controlled by the application
	return cmd.Output()
}

// messageSender abstracts the ability to send messages.
type messageSender interface {
	Send(msg tea.Msg)
}

// taskOutputMsg is sent when a running task produces output.
type taskOutputMsg struct {
	line string
}

// taskDoneMsg is sent when a task finishes executing.
type taskDoneMsg struct {
	err error
}

type model struct {
	tasksTable     table.Model
	toolsTable     table.Model
	envVarsTable   table.Model
	tasks          []loader.Task
	tools          []loader.Tool
	envVars        []loader.EnvVar
	focus          int // focusTasks, focusTools, or focusEnvVars
	tasksLoading   bool
	toolsLoading   bool
	envVarsLoading bool
	err            error

	// Mise info for header
	miseVersion string

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
	runner commandRunner // for running commands
	sender messageSender // for sending messages to the program
	styles styles        // UI styles
	logger *slog.Logger  // for logging

	// File watching state
	watcher     *fsnotify.Watcher // watches config files for changes
	configPaths []string          // paths being watched
	lastReload  time.Time         // for debouncing file change events
}

func (m model) Init() tea.Cmd {
	ctx := context.Background()
	return tea.Batch(
		loader.LoadMiseTasks(ctx, m.runner),
		loader.LoadMiseTools(ctx, m.runner),
		loader.LoadMiseEnvVars(ctx, m.runner),
		loader.LoadMiseVersion(ctx, m.runner),
		loader.LoadMiseConfigFiles(ctx, m.runner),
	)
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
		return m.handleTaskOutput(msg), nil

	case taskDoneMsg:
		return m.handleTaskDone(msg), nil

	case loader.TasksLoadedMsg:
		return m.handleTasksLoaded(msg), nil

	case loader.ToolsLoadedMsg:
		return m.handleToolsLoaded(msg), nil

	case loader.EnvVarsLoadedMsg:
		return m.handleEnvVarsLoaded(msg), nil

	case loader.MiseVersionMsg:
		return m.handleMiseVersion(msg), nil

	case loader.ConfigFilesLoadedMsg:
		return m.handleConfigFilesLoaded(msg), nil

	case watcher.FileChangedMsg:
		return m.handleFileChanged(msg)

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
			// Update table layout based on terminal size
			m = updateTableLayout(m)
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

// renderHeader renders the application header with branding and mise version.
func (m model) renderHeader() string {
	tagline := m.styles.title.Render("prep") + m.styles.dimTitle.Render(" — mise en place, now prep")

	var versionLine string
	if m.miseVersion != "" {
		versionLine = m.styles.help.Render("mise v" + m.miseVersion)
	}

	return lipgloss.JoinVertical(lipgloss.Left, tagline, versionLine)
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
	header := m.renderHeader()
	tasksTitle := m.styles.renderTitle("Tasks", m.focus == focusTasks)
	toolsTitle := m.styles.renderTitle("Tools", m.focus == focusTools)
	envVarsTitle := m.styles.renderTitle("Environment Variables", m.focus == focusEnvVars)

	// Contextual help text based on focus
	var help string
	switch m.focus {
	case focusTasks:
		help = m.styles.help.Render("Tab to switch • ↑/↓ to navigate • Enter to run task • q to quit")
	case focusEnvVars:
		help = m.styles.help.Render("Tab to switch • ↑/↓ to navigate • v show • V show all • h hide all • q to quit")
	default:
		help = m.styles.help.Render("Tab to switch • ↑/↓ to navigate • q to quit")
	}

	// Build the view using JoinVertical
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
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

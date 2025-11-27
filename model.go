package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
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

// editorClosedMsg is sent when the external editor closes.
type editorClosedMsg struct {
	err error
}

// pickerState represents the state of the tool installation picker.
type pickerState int

const (
	pickerClosed          pickerState = iota // picker not showing
	pickerSelectTool                         // showing tool list
	pickerLoadingVersions                    // loading versions for selected tool
	pickerSelectVersion                      // showing version list
	pickerSelectConfig                       // showing config file list
	pickerInstalling                         // installing tool@version
)

// toolItem represents a tool in the picker list.
type toolItem struct {
	name    string
	backend string
}

// FilterValue implements list.Item.
func (t toolItem) FilterValue() string { return t.name }

// Title implements list.DefaultItem.
func (t toolItem) Title() string { return t.name }

// Description implements list.DefaultItem.
func (t toolItem) Description() string { return t.backend }

// versionItem represents a version in the picker list.
type versionItem struct {
	version string
}

// FilterValue implements list.Item.
func (v versionItem) FilterValue() string { return v.version }

// Title implements list.DefaultItem.
func (v versionItem) Title() string { return v.version }

// Description implements list.DefaultItem.
func (v versionItem) Description() string { return "" }

// configItem represents a config file in the picker list.
type configItem struct {
	path string
}

// FilterValue implements list.Item.
func (c configItem) FilterValue() string { return c.path }

// Title implements list.DefaultItem.
func (c configItem) Title() string { return c.path }

// Description implements list.DefaultItem.
func (c configItem) Description() string { return "" }

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
	showOutput       bool               // whether to show the output viewport
	runningTask      string             // name of the task being run
	taskRunning      bool               // whether a task is currently running
	taskSpinner      spinner.Model      // animated spinner for running tasks
	taskErr          error              // error from task execution (if any)
	output           []string           // output lines from the task
	totalOutputLines int                // total number of output lines received
	viewport         viewport.Model     // scrollable viewport for output
	wrapOutput       bool               // whether word wrapping is enabled for output
	cancelFunc       context.CancelFunc // to cancel the running task
	windowWidth      int
	windowHeight     int

	// Task arguments state
	argInputActive bool            // whether argument input mode is active
	argInput       textinput.Model // text input for task arguments
	argInputTask   string          // task name that arguments are for

	// Dependencies (DIP)
	runner commandRunner // for running commands
	sender messageSender // for sending messages to the program
	styles styles        // UI styles
	logger *slog.Logger  // for logging
	editor string        // editor command for editing source files

	// File watching state
	watcher     *fsnotify.Watcher // watches config files for changes
	configPaths []string          // paths being watched
	lastReload  time.Time         // for debouncing file change events

	// Tool picker state
	pickerState     pickerState // current picker state
	toolList        list.Model  // list of available tools
	versionList     list.Model  // list of versions for selected tool
	configList      list.Model  // list of config files for installation target
	selectedTool    string      // tool selected in first step
	selectedVersion string      // version selected in second step
	versionsLoading bool        // loading versions

	// Cached directory paths for source priority sorting
	cwd     string
	homeDir string

	// Help bubbles for each context
	tasksHelp    help.Model
	envVarsHelp  help.Model
	toolsHelp    help.Model
	outputHelp   help.Model
	argInputHelp help.Model
	filterHelp   help.Model

	// Key maps
	tasksKeys    tasksKeyMap
	envVarsKeys  envVarsKeyMap
	toolsKeys    toolsKeyMap
	outputKeys   outputKeyMap
	argInputKeys argInputKeyMap
	filterKeys   filterKeyMap

	// Task filter state
	filterActive  bool            // whether filter mode is active
	filterInput   textinput.Model // text input for filtering tasks
	filteredTasks []loader.Task   // tasks matching current filter
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
	// When picker is open, route messages to the picker (lists need all msg types for filtering)
	if m.pickerState != pickerClosed {
		return m.handlePickerUpdate(msg)
	}

	// When filter is active, route messages to filter input handler
	if m.filterActive {
		return m.handleFilterInput(msg)
	}

	// When argument input is active, route messages to argument input handler
	if m.argInputActive {
		return m.handleArgInput(msg)
	}

	switch msg := msg.(type) {
	case spinner.TickMsg:
		if m.taskRunning {
			var cmd tea.Cmd
			m.taskSpinner, cmd = m.taskSpinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		m.logger.Debug("handling key pess", "key", msg)
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

	case loader.RegistryLoadedMsg:
		return m.handleRegistryLoaded(msg), nil

	case loader.VersionsLoadedMsg:
		return m.handleVersionsLoaded(msg), nil

	case loader.ToolInstalledMsg:
		return m.handleToolInstalled(msg)

	case loader.ToolRemovedMsg:
		return m.handleToolRemoved(msg)

	case watcher.FileChangedMsg:
		return m.handleFileChanged(msg)

	case editorClosedMsg:
		return m.handleEditorClosed(msg), nil

	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg), nil
	}

	return m.updateFocusedComponent(msg)
}

// updateFocusedComponent updates the currently focused table or viewport.
func (m model) updateFocusedComponent(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	// Update viewport when showing output
	if m.showOutput {
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	// Update the focused table with any other messages (only when not showing output or picker)
	canUpdateTables := m.pickerState == pickerClosed &&
		!m.tasksLoading && !m.toolsLoading && !m.envVarsLoading && m.err == nil
	if canUpdateTables {
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
	// Show picker view if picker is open
	if m.pickerState != pickerClosed {
		return m.renderPickerView()
	}

	// Show argument input view if active
	if m.argInputActive {
		return m.renderArgInputView()
	}

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

	// Build tasks section with optional filter input
	tasksSection := tasksTitle
	if filterView := m.renderFilterInput(); filterView != "" {
		tasksSection = lipgloss.JoinVertical(lipgloss.Left, tasksSection, filterView)
	}

	// Get contextual help based on focus or filter state
	var helpView string
	if m.filterActive {
		helpView = m.filterHelp.View(m.filterKeys)
	} else {
		switch m.focus {
		case focusTasks:
			helpView = m.tasksHelp.View(m.tasksKeys)
		case focusTools:
			helpView = m.toolsHelp.View(m.toolsKeys)
		case focusEnvVars:
			helpView = m.envVarsHelp.View(m.envVarsKeys)
		}
	}

	// Build the view using JoinVertical
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		tasksSection,
		m.tasksTable.View(),
		"",
		toolsTitle,
		m.toolsTable.View(),
		"",
		envVarsTitle,
		m.envVarsTable.View(),
		"",
		helpView,
	)

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// renderPickerView renders the tool picker overlay.
func (m model) renderPickerView() tea.View {
	var content string

	switch m.pickerState {
	case pickerClosed:
		// Should not reach here, but handle for completeness
		content = ""
	case pickerSelectTool:
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			m.toolList.View(),
		)
	case pickerLoadingVersions:
		content = fmt.Sprintf("Loading versions for %s...", m.selectedTool)
	case pickerSelectVersion:
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			m.versionList.View(),
		)
	case pickerSelectConfig:
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			m.configList.View(),
		)
	case pickerInstalling:
		content = fmt.Sprintf("Installing %s@%s...", m.selectedTool, m.selectedVersion)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// renderOutputView renders the task output viewport.
func (m model) renderOutputView() tea.View {
	var title string
	if m.totalOutputLines > maxOutputLines {
		title = m.styles.title.Render(fmt.Sprintf("Task: %s (showing last %d of %d lines)",
			m.runningTask, maxOutputLines, m.totalOutputLines))
	} else {
		title = m.styles.title.Render(fmt.Sprintf("Task: %s", m.runningTask))
	}

	var status string
	switch {
	case m.taskRunning:
		status = m.styles.dimTitle.Render(m.taskSpinner.View() + " Running...")
	case m.taskErr != nil:
		status = m.styles.err.Render(fmt.Sprintf("✗ Failed: %v", m.taskErr))
	default:
		status = m.styles.success.Render("✓ Completed")
	}

	header := lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", status)

	// Update output keys based on running state and render help
	m.outputKeys = newOutputKeyMap(m.taskRunning)
	helpView := m.outputHelp.View(m.outputKeys)

	// Build the view
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		m.viewport.View(),
		"",
		helpView,
	)

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

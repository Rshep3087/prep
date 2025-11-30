package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/google/shlex"
	"github.com/muesli/reflow/wordwrap"
	"github.com/sahilm/fuzzy"

	"github.com/rshep3087/prep/internal/loader"
	"github.com/rshep3087/prep/internal/watcher"
)

// debounceInterval is the minimum time between file change reloads.
const debounceInterval = 500 * time.Millisecond

// Key constants for common key bindings.
const (
	keyEsc      = "esc"
	keyEnter    = "enter"
	keyAltEnter = "alt+enter"
)

// minToolRowFields is the minimum number of fields expected in a tool table row (name, version).
const minToolRowFields = 2

// Source priority constants for sorting.
// Lower values = higher priority (closer to current directory).
const (
	priorityParentDirBase = 1000   // Base priority for parent directories
	priorityHomeDir       = 10000  // Priority for home directory configs
	prioritySystemDir     = 100000 // Priority for system configs (/etc/mise)
	priorityUnknown       = 999999 // Priority for unresolvable paths
)

// keyHanlder handler key presses.
type keyHandler func(m model) (model, tea.Cmd, bool)

// sourcePriority returns the priority of a source path for sorting.
// Following mise's configuration hierarchy: configs closer to cwd have HIGHER priority (lower number).
// Priority is based on directory depth relative to cwd:
// - Configs in cwd or subdirectories: negative depth (closer = higher priority).
// - System/home configs: large positive number (lower priority).
func (m model) sourcePriority(sourcePath string) int {
	// Normalize the source path to absolute
	absPath := sourcePath
	if !filepath.IsAbs(sourcePath) {
		var err error
		absPath, err = filepath.Abs(sourcePath)
		if err != nil {
			return priorityUnknown // If we can't resolve, lowest priority
		}
	}

	// Get the directory containing the config file
	configDir := filepath.Dir(absPath)

	// Check if this config is in cwd or a subdirectory of cwd
	relPath, err := filepath.Rel(m.cwd, configDir)
	if err == nil && !strings.HasPrefix(relPath, "..") {
		// Config is in cwd or subdirectory
		// Count directory depth: fewer levels = higher priority (lower number)
		// cwd itself = 0, cwd/subdir = 1, cwd/subdir/subdir = 2, etc.
		if relPath == "." {
			return 0 // Config in cwd has highest priority
		}
		depth := strings.Count(relPath, string(filepath.Separator)) + 1
		return depth
	}

	// Check if this is a parent directory of cwd (mise walks up the tree)
	relPath, err = filepath.Rel(configDir, m.cwd)
	if err == nil && !strings.HasPrefix(relPath, "..") {
		// Config is in a parent directory of cwd
		// More levels up = lower priority (higher number)
		levelsUp := strings.Count(relPath, string(filepath.Separator)) + 1
		return priorityParentDirBase + levelsUp
	}

	// Check home directory configs (lower priority than project configs)
	// Only for configs that are NOT in the project tree
	if strings.HasPrefix(absPath, m.homeDir) {
		return priorityHomeDir
	}

	// Check system configs (lowest priority)
	if strings.HasPrefix(absPath, "/etc/mise") {
		return prioritySystemDir
	}

	// Everything else
	return priorityUnknown
}

// filterTasks filters tasks using fuzzy matching against name and description.
func filterTasks(tasks []loader.Task, filter string) []loader.Task {
	if filter == "" {
		return tasks
	}

	// Create searchable strings (name + description for each task)
	var sources []string
	taskMap := make(map[int]loader.Task)
	for i, task := range tasks {
		// Fuzzy match against name and description combined
		sources = append(sources, task.Name+" "+task.Description)
		taskMap[i] = task
	}

	// Use fuzzy.Find for intelligent matching
	matches := fuzzy.Find(filter, sources)

	// Build filtered results maintaining fuzzy match order (best matches first)
	filtered := make([]loader.Task, 0, len(matches))
	for _, match := range matches {
		filtered = append(filtered, taskMap[match.Index])
	}

	return filtered
}

// applyTaskFilter applies filter and updates table rows.
func (m model) applyTaskFilter(resetCursor bool) model {
	filterValue := m.filterInput.Value()
	if filterValue == "" {
		m.filteredTasks = m.tasks
	} else {
		m.filteredTasks = filterTasks(m.tasks, filterValue)
	}

	rows := make([]table.Row, 0, len(m.filteredTasks))
	for _, task := range m.filteredTasks {
		rows = append(rows, table.Row{task.Name, task.Description, formatSourcePath(task.Source)})
	}
	m.tasksTable.SetRows(rows)

	if resetCursor && len(rows) > 0 {
		m.tasksTable.SetCursor(0)
	}
	return m
}

// handleTasksLoaded processes the tasksLoadedMsg and initializes the tasks table.
func (m model) handleTasksLoaded(msg loader.TasksLoadedMsg) model {
	if msg.Err != nil {
		m.logger.Error("error loading tasks", "error", msg.Err)
		m.err = msg.Err
		m.tasksLoading = false
		return m
	}

	m.logger.Debug("loaded tasks", "count", len(msg.Tasks))

	// Sort tasks by source priority (closer to cwd = higher priority), then by name
	slices.SortFunc(msg.Tasks, func(a, b loader.Task) int {
		priorityA := m.sourcePriority(a.Source)
		priorityB := m.sourcePriority(b.Source)
		if c := cmp.Compare(priorityA, priorityB); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})

	m.tasks = msg.Tasks
	m.tasksLoading = false

	rows := make([]table.Row, 0, len(m.tasks))
	for _, task := range m.tasks {
		rows = append(rows, table.Row{task.Name, task.Description, formatSourcePath(task.Source)})
	}

	// Update rows on existing table instead of recreating
	m.tasksTable.SetRows(rows)

	// Re-apply layout settings if we have window dimensions
	if m.windowWidth > 0 {
		m = updateTableLayout(m)
	}

	// Re-apply filter if active (preserve cursor position during reload)
	if m.filterActive {
		m = m.applyTaskFilter(false)
	}

	return m
}

// handleToolsLoaded processes the toolsLoadedMsg and initializes the tools table.
func (m model) handleToolsLoaded(msg loader.ToolsLoadedMsg) model {
	if msg.Err != nil {
		m.logger.Error("error loading tools", "error", msg.Err)
		m.err = msg.Err
		m.toolsLoading = false
		return m
	}

	m.logger.Debug("loaded tools", "count", len(msg.Tools))

	// Sort tools by source priority (closer to cwd = higher priority), then by name
	slices.SortFunc(msg.Tools, func(a, b loader.Tool) int {
		priorityA := m.sourcePriority(a.SourcePath)
		priorityB := m.sourcePriority(b.SourcePath)
		if c := cmp.Compare(priorityA, priorityB); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})

	m.tools = msg.Tools
	m.toolsLoading = false

	rows := make([]table.Row, 0, len(m.tools))
	for _, tool := range m.tools {
		rows = append(rows, table.Row{
			tool.Name,
			tool.Version,
			tool.RequestedVersion,
			formatSourcePath(tool.SourcePath),
		})
	}

	// Update rows on existing table instead of recreating
	m.toolsTable.SetRows(rows)

	// Re-apply layout settings if we have window dimensions
	if m.windowWidth > 0 {
		m = updateTableLayout(m)
	}
	return m
}

// handleEnvVarsLoaded processes the envVarsLoadedMsg and initializes the env vars table.
func (m model) handleEnvVarsLoaded(msg loader.EnvVarsLoadedMsg) model {
	if msg.Err != nil {
		m.logger.Error("error loading env vars", "error", msg.Err)
		m.err = msg.Err
		m.envVarsLoading = false
		return m
	}

	m.logger.Debug("loaded env vars", "count", len(msg.EnvVars))

	// Sort env vars by name for stable ordering
	slices.SortFunc(msg.EnvVars, func(a, b loader.EnvVar) int {
		return cmp.Compare(a.Name, b.Name)
	})

	// Build a map of previously unmasked env vars to preserve state
	unmasked := make(map[string]bool)
	for _, ev := range m.envVars {
		if !ev.Masked {
			unmasked[ev.Name] = true
		}
	}

	// Apply preserved mask state to new env vars
	for i := range msg.EnvVars {
		if unmasked[msg.EnvVars[i].Name] {
			msg.EnvVars[i].Masked = false
		}
	}

	m.envVars = msg.EnvVars
	m.envVarsLoading = false

	rows := make([]table.Row, 0, len(m.envVars))
	for _, ev := range m.envVars {
		displayValue := maskValue(ev.Value)
		if !ev.Masked {
			displayValue = ev.Value
		}
		rows = append(rows, table.Row{ev.Name, displayValue})
	}

	// Update rows on existing table instead of recreating
	m.envVarsTable.SetRows(rows)

	// Re-apply layout settings if we have window dimensions
	if m.windowWidth > 0 {
		m = updateTableLayout(m)
	}
	return m
}

// handleMiseVersion processes the miseVersionMsg and updates the model.
func (m model) handleMiseVersion(msg loader.MiseVersionMsg) model {
	if msg.Err != nil {
		m.logger.Error("error loading mise version", "error", msg.Err)
		return m
	}
	m.miseVersion = msg.Version
	m.logger.Debug("loaded mise version", "version", msg.Version)
	return m
}

// handleTaskOutput appends task output and updates the viewport.
// Implements a rolling buffer: when output exceeds maxOutputLines.
func (m model) handleTaskOutput(msg taskOutputMsg) model {
	m.totalOutputLines++

	// Implement rolling buffer: keep only the last maxOutputLines
	if len(m.output) >= maxOutputLines {
		m.output = m.output[len(m.output)-(maxOutputLines-1):]
	}

	m.output = append(m.output, msg.line)

	// Apply word wrapping if enabled
	displayLines := wrapOutputLines(m.output, m.viewport.Width(), m.wrapOutput)
	m.viewport.SetContentLines(displayLines)
	m.viewport.GotoBottom()
	return m
}

// wrapOutputLines applies word wrapping to output lines if enabled.
// Returns the original lines if wrapping is disabled or width is invalid.
func wrapOutputLines(lines []string, width int, wrapEnabled bool) []string {
	if !wrapEnabled {
		return lines
	}

	// Minimum practical width to prevent excessive wrapping
	const minWrapWidth = 20
	if width < minWrapWidth {
		return lines
	}

	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			// Preserve empty lines
			wrapped = append(wrapped, "")
			continue
		}

		// Apply word wrapping
		wrappedLine := wordwrap.String(line, width)
		// wordwrap.String returns a single string with newlines
		// Split it into separate lines for the viewport
		splitLines := strings.Split(wrappedLine, "\n")
		wrapped = append(wrapped, splitLines...)
	}

	return wrapped
}

// handleTaskDone processes task completion.
func (m model) handleTaskDone(msg taskDoneMsg) model {
	m.taskRunning = false
	m.taskErr = msg.err
	m.cancelFunc = nil
	if msg.err != nil {
		m.logger.Error("task finished with error", "task", m.runningTask, "error", msg.err)
	} else {
		m.logger.Debug("task finished successfully", "task", m.runningTask)
	}
	return m
}

// handleConfigFilesLoaded processes config files and starts the file watcher.
func (m model) handleConfigFilesLoaded(msg loader.ConfigFilesLoadedMsg) model {
	if msg.Err != nil {
		m.logger.Error("error loading config files", "error", msg.Err)
		return m
	}
	m.configPaths = msg.Paths
	m.logger.Debug("loaded config files to watch", "count", len(msg.Paths))
	w, err := watcher.StartFileWatcher(msg.Paths, m.sender)
	if err != nil {
		m.logger.Error("error starting file watcher", "error", err)
		return m
	}
	m.watcher = w
	return m
}

// handleEditorClosed processes the editor closed message.
func (m model) handleEditorClosed(msg editorClosedMsg) model {
	if msg.err != nil {
		m.logger.Error("editor closed with error", "error", msg.err)
	} else {
		m.logger.Debug("editor closed successfully")
	}
	// File watcher will detect changes and trigger reload automatically
	return m
}

// handleInteractiveTaskClosed processes the interactive task closed message.
func (m model) handleInteractiveTaskClosed(msg interactiveTaskClosedMsg) model {
	if msg.err != nil {
		m.logger.Error("interactive task closed with error", "task", msg.taskName, "error", msg.err)
	} else {
		m.logger.Debug("interactive task completed successfully", "task", msg.taskName)
	}
	return m
}

// handleFileChanged processes file change events with debouncing.
func (m model) handleFileChanged(msg watcher.FileChangedMsg) (model, tea.Cmd) {
	if time.Since(m.lastReload) < debounceInterval {
		return m, nil
	}
	m.lastReload = time.Now()
	m.logger.Debug("config file changed, reloading mise data", "path", msg.Path)
	return m, loader.ReloadMiseData(m.runner)
}

//nolint:funlen // Function is 106 lines, slightly over 100 limit
func (m model) handleMainKeys(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	key := msg.String()

	globalKeys := map[string]keyHandler{
		"q": func(m model) (model, tea.Cmd, bool) {
			watcher.Close(m.watcher)
			return m, tea.Quit, true
		},
		"ctrl+c": func(m model) (model, tea.Cmd, bool) {
			watcher.Close(m.watcher)
			return m, tea.Quit, true
		},
		keyEsc: func(m model) (model, tea.Cmd, bool) {
			watcher.Close(m.watcher)
			return m, tea.Quit, true
		},
		"tab": func(m model) (model, tea.Cmd, bool) {
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
		},
		"e": func(m model) (model, tea.Cmd, bool) {
			// edit allowed in tasks or tools
			return m.editSourceFile()
		},
	}

	if fn, ok := globalKeys[key]; ok {
		return fn(m)
	}

	taskKeyHandlers := map[string]keyHandler{
		keyEnter: func(m model) (model, tea.Cmd, bool) {
			if len(m.tasks) == 0 {
				return m, nil, true
			}
			return m.handleTaskEnter()
		},
		keyAltEnter: func(m model) (model, tea.Cmd, bool) {
			if len(m.tasks) == 0 {
				return m, nil, true
			}
			return m.handleTaskAltEnter()
		},
		"ctrl+enter": func(m model) (model, tea.Cmd, bool) {
			if len(m.tasks) == 0 {
				return m, nil, true
			}
			return m.handleTaskCtrlEnter()
		},
		"ctrl+shift+enter": func(m model) (model, tea.Cmd, bool) {
			if len(m.tasks) == 0 {
				return m, nil, true
			}
			return m.handleTaskCtrlAltEnter()
		},
		"/": func(m model) (model, tea.Cmd, bool) {
			m.filterActive = true
			m.filterInput.Focus()
			m.filterInput.SetValue("")
			m.filteredTasks = m.tasks
			return m, nil, true
		},
	}

	toolKeyHandlers := map[string]keyHandler{
		"a": func(m model) (model, tea.Cmd, bool) {
			return m.openToolPicker()
		},
		"u": func(m model) (model, tea.Cmd, bool) {
			return m.unuseTool()
		},
	}

	envKeyHandlers := map[string]keyHandler{
		"v": func(m model) (model, tea.Cmd, bool) {
			return showSelectedEnvVar(m), nil, true
		},
		"V": func(m model) (model, tea.Cmd, bool) {
			return showAllEnvVars(m), nil, true
		},
		"h": func(m model) (model, tea.Cmd, bool) {
			return hideAllEnvVars(m), nil, true
		},
	}

	// 2) focus specific
	switch m.focus {
	case focusTasks:
		if fn, ok := taskKeyHandlers[key]; ok {
			return fn(m)
		}
	case focusTools:
		if fn, ok := toolKeyHandlers[key]; ok {
			return fn(m)
		}
	case focusEnvVars:
		if fn, ok := envKeyHandlers[key]; ok {
			return fn(m)
		}
	}

	// not handled → bubble up
	return m, nil, false
}

func (m model) handleTaskEnter() (model, tea.Cmd, bool) {
	selectedRow := m.tasksTable.SelectedRow()
	if selectedRow != nil {
		taskName := selectedRow[0]
		newModel, cmd := m.startTask(taskName)
		return newModel, cmd, true
	}

	return model{}, nil, false
}

func (m model) handleTaskAltEnter() (model, tea.Cmd, bool) {
	selectedRow := m.tasksTable.SelectedRow()
	if selectedRow != nil {
		taskName := selectedRow[0]
		m.argInputActive = true
		m.argInputTask = taskName
		m.argInput.Focus()
		m.argInput.SetValue("")
		return m, nil, true
	}

	return model{}, nil, false
}

// handleTaskCtrlEnter runs an interactive task immediately without prompting for arguments.
func (m model) handleTaskCtrlEnter() (model, tea.Cmd, bool) {
	selectedRow := m.tasksTable.SelectedRow()
	if selectedRow != nil {
		taskName := selectedRow[0]
		cmd := m.runInteractiveTask(taskName)
		return m, cmd, true
	}

	return model{}, nil, false
}

// handleTaskCtrlAltEnter opens argument input for interactive task execution.
func (m model) handleTaskCtrlAltEnter() (model, tea.Cmd, bool) {
	selectedRow := m.tasksTable.SelectedRow()
	if selectedRow != nil {
		taskName := selectedRow[0]
		m.argInputActive = true
		m.argInputInteractive = true
		m.argInputTask = taskName
		m.argInput.Focus()
		m.argInput.SetValue("")
		return m, nil, true
	}

	return model{}, nil, false
}

// handleArgInput handles input when argument input mode is active.
func (m model) handleArgInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		switch keyMsg.String() {
		case keyEsc:
			// Cancel argument input
			m.argInputActive = false
			m.argInputInteractive = false // Reset flag
			m.argInputTask = ""
			m.argInput.SetValue("")
			return m, nil
		case keyEnter:
			// Run task with arguments
			args := m.argInput.Value()
			taskName := m.argInputTask
			isInteractive := m.argInputInteractive

			// Deactivate argument input
			m.argInputActive = false
			m.argInputInteractive = false // Reset flag
			m.argInputTask = ""
			m.argInput.SetValue("")

			// Parse arguments with proper quote handling
			var argSlice []string
			if args != "" {
				var err error
				argSlice, err = shlex.Split(args)
				if err != nil {
					m.logger.Error("failed to parse arguments", "args", args, "error", err)
					argSlice = strings.Fields(args) // fallback
				}
			}

			// Branch on execution mode
			if isInteractive {
				return m, m.runInteractiveTask(taskName, argSlice...)
			}
			return m.startTask(taskName, argSlice...)
		}
	}

	// Pass message to text input for normal editing
	var cmd tea.Cmd
	m.argInput, cmd = m.argInput.Update(msg)
	return m, cmd
}

// clearFilter deactivates filter mode and restores the full task list.
func (m model) clearFilter() model {
	m.filterActive = false
	m.filterInput.SetValue("")
	m.filteredTasks = m.tasks

	// Restore full task list
	rows := make([]table.Row, 0, len(m.tasks))
	for _, task := range m.tasks {
		rows = append(rows, table.Row{task.Name, task.Description, formatSourcePath(task.Source)})
	}
	m.tasksTable.SetRows(rows)
	if len(rows) > 0 {
		m.tasksTable.SetCursor(0)
	}
	return m
}

// handleFilterInput handles input when filter mode is active.
func (m model) handleFilterInput(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		// Update filter input and apply filter in real-time
		var cmd tea.Cmd
		m.filterInput, cmd = m.filterInput.Update(msg)
		m = m.applyTaskFilter(false)
		return m, cmd
	}

	switch keyMsg.String() {
	case keyEsc:
		return m.clearFilter(), nil

	case keyEnter:
		// Run selected filtered task (deactivate filter to show output)
		if len(m.filteredTasks) > 0 {
			cursor := m.tasksTable.Cursor()
			if cursor >= 0 && cursor < len(m.filteredTasks) {
				taskName := m.filteredTasks[cursor].Name
				m.filterActive = false
				return m.startTask(taskName)
			}
		}
		return m, nil

	case keyAltEnter:
		// Open argument input for selected filtered task (deactivate filter)
		if len(m.filteredTasks) > 0 {
			cursor := m.tasksTable.Cursor()
			if cursor >= 0 && cursor < len(m.filteredTasks) {
				taskName := m.filteredTasks[cursor].Name
				m.filterActive = false
				m.argInputActive = true
				m.argInputTask = taskName
				m.argInput.Focus()
				m.argInput.SetValue("")
				return m, nil
			}
		}
		return m, nil

	case "ctrl+enter":
		// Open argument input for interactive execution of filtered task
		if len(m.filteredTasks) > 0 {
			cursor := m.tasksTable.Cursor()
			if cursor >= 0 && cursor < len(m.filteredTasks) {
				taskName := m.filteredTasks[cursor].Name
				m.filterActive = false
				m.argInputActive = true
				m.argInputInteractive = true
				m.argInputTask = taskName
				m.argInput.Focus()
				m.argInput.SetValue("")
				return m, nil
			}
		}
		return m, nil

	case "up", "down", "j", "k":
		// Pass navigation keys to table
		var cmd tea.Cmd
		m.tasksTable, cmd = m.tasksTable.Update(msg)
		return m, cmd
	}

	// Update filter input and apply filter in real-time
	// Reset cursor to 0 when filter text changes (user typed a character)
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m = m.applyTaskFilter(true)
	return m, cmd
}

func (m model) unuseTool() (model, tea.Cmd, bool) {
	row := m.toolsTable.SelectedRow()
	if len(row) < minToolRowFields {
		return m, nil, false
	}

	tool := row[0]
	version := row[1]
	m.logger.Debug("removing tool", "tool", tool, "version", version)

	ctx := context.Background()
	return m, loader.RemoveTool(ctx, m.runner, tool, version), true
}

// editSourceFile opens the source file for the selected task or tool in the editor.
func (m model) editSourceFile() (model, tea.Cmd, bool) {
	source := m.getSelectedSourcePath()
	if source == "" {
		return m, nil, true
	}
	m.logger.Debug("opening editor for source", "source", source)
	return m, m.openEditor(source), true
}

// getSelectedSourcePath returns the source file path for the currently selected row.
func (m model) getSelectedSourcePath() string {
	switch m.focus {
	case focusTasks:
		idx := m.tasksTable.Cursor()
		if idx >= 0 && idx < len(m.tasks) {
			return m.tasks[idx].Source
		}
	case focusTools:
		idx := m.toolsTable.Cursor()
		if idx >= 0 && idx < len(m.tools) {
			return m.tools[idx].SourcePath
		}
	}
	return ""
}

// handleWrapToggle toggles word wrapping and preserves scroll position.
func (m model) handleWrapToggle() model {
	// Preserve scroll position ratio
	oldYOffset := m.viewport.YOffset()
	oldTotalHeight := m.viewport.TotalLineCount()

	// Toggle wrap state
	m.wrapOutput = !m.wrapOutput

	// Re-apply content with new wrap state
	displayLines := wrapOutputLines(m.output, m.viewport.Width(), m.wrapOutput)
	m.viewport.SetContentLines(displayLines)

	// Restore relative scroll position
	newTotalHeight := m.viewport.TotalLineCount()
	if oldTotalHeight > 0 && newTotalHeight > 0 {
		newYOffset := (oldYOffset * newTotalHeight) / oldTotalHeight
		m.viewport.SetYOffset(newYOffset)
	}

	return m
}

// handleOutputKeys handles key presses in the output view.
func (m model) handleOutputKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "w":
		return m.handleWrapToggle(), nil
	case "q", keyEsc:
		// Close output view (only if task is not running)
		if !m.taskRunning {
			m.showOutput = false
			m.output = nil
			m.runningTask = ""
			m.taskErr = nil
			m.wrapOutput = false // Reset wrap state
			// Clear filter data when returning from output view (filter may have been used to select task)
			if len(m.filteredTasks) > 0 && len(m.filteredTasks) < len(m.tasks) {
				m = m.clearFilter()
			}
			return m, nil
		}
		return m, nil
	case "ctrl+c":
		// Cancel running task
		if m.taskRunning && m.cancelFunc != nil {
			m.logger.Debug("cancelling task", "task", m.runningTask)
			m.cancelFunc()
			return m, nil
		}
		// If not running, quit the app
		if !m.taskRunning {
			watcher.Close(m.watcher)
			return m, tea.Quit
		}
		return m, nil
	}

	// Pass other keys to viewport for scrolling
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

// maskValue returns a masked representation of a value.
func maskValue(value string) string {
	if len(value) == 0 {
		return ""
	}
	// Use a consistent mask length for cleaner display
	return "●●●●●●●●"
}

// showSelectedEnvVar unmasks the currently selected environment variable.
func showSelectedEnvVar(m model) model {
	if len(m.envVars) == 0 {
		return m
	}
	selectedRow := m.envVarsTable.SelectedRow()
	if selectedRow == nil {
		return m
	}
	selectedName := selectedRow[0]
	for i := range m.envVars {
		if m.envVars[i].Name == selectedName {
			m.envVars[i].Masked = false
			break
		}
	}
	return refreshEnvVarsTable(m)
}

// showAllEnvVars unmasks all environment variables.
func showAllEnvVars(m model) model {
	for i := range m.envVars {
		m.envVars[i].Masked = false
	}
	return refreshEnvVarsTable(m)
}

// hideAllEnvVars masks all environment variables.
func hideAllEnvVars(m model) model {
	for i := range m.envVars {
		m.envVars[i].Masked = true
	}
	return refreshEnvVarsTable(m)
}

// refreshEnvVarsTable rebuilds the env vars table rows based on current mask state.
func refreshEnvVarsTable(m model) model {
	rows := make([]table.Row, 0, len(m.envVars))
	for _, ev := range m.envVars {
		displayValue := maskValue(ev.Value)
		if !ev.Masked {
			displayValue = ev.Value
		}
		rows = append(rows, table.Row{ev.Name, displayValue})
	}

	// Update rows on existing table instead of recreating
	m.envVarsTable.SetRows(rows)

	return m
}

// runTask executes a mise task and streams output back to the TUI.
func runTask(ctx context.Context, taskName string, sender messageSender, args ...string) tea.Cmd {
	return func() tea.Msg {
		cmdArgs := []string{"mise", "run", taskName}
		// If there are arguments, add -- separator so mise passes them to the task
		if len(args) > 0 {
			cmdArgs = append(cmdArgs, "--")
			cmdArgs = append(cmdArgs, args...)
		}
		//nolint:gosec // cmdArgs are controlled: mise command is hardcoded, taskName from config, args from user
		cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)

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

// startTask initializes and starts a task execution.
func (m model) startTask(taskName string, args ...string) (model, tea.Cmd) {
	m.logger.Debug("starting task", "task", taskName, "args", args)

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

	// Enable high performance rendering for alternate screen buffer
	m.viewport.YPosition = 0

	m.showOutput = true
	m.runningTask = taskName
	m.taskRunning = true
	m.taskErr = nil
	m.output = []string{}
	m.totalOutputLines = 0
	m.cancelFunc = cancel

	return m, tea.Batch(
		runTask(ctx, taskName, m.sender, args...),
		m.taskSpinner.Tick,
	)
}

// openToolPicker opens the tool picker and starts loading the registry.
func (m model) openToolPicker() (model, tea.Cmd, bool) {
	m.logger.Debug("opening tool picker")
	m.pickerState = pickerSelectTool

	// Initialize empty list while loading
	delegate := list.NewDefaultDelegate()
	width := m.windowWidth
	height := m.windowHeight
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}

	m.toolList = list.New([]list.Item{}, delegate, width, height-pickerListPadding)
	m.toolList.Title = "Select a Tool to Install"
	m.toolList.SetShowStatusBar(true)
	m.toolList.SetFilteringEnabled(true)

	// Start loading registry
	ctx := context.Background()
	return m, loader.LoadMiseRegistry(ctx, m.runner), true
}

// closeToolPicker closes the tool picker and resets state.
func (m model) closeToolPicker() model {
	m.logger.Debug("closing tool picker")
	m.pickerState = pickerClosed
	m.selectedTool = ""
	m.selectedVersion = ""
	m.versionsLoading = false
	return m
}

// handleRegistryLoaded processes the registry loaded message.
func (m model) handleRegistryLoaded(msg loader.RegistryLoadedMsg) model {
	if msg.Err != nil {
		m.logger.Error("error loading registry", "error", msg.Err)
		m.pickerState = pickerClosed
		return m
	}

	m.logger.Debug("loaded registry", "count", len(msg.Tools))

	// Convert to list items
	items := make([]list.Item, len(msg.Tools))
	for i, tool := range msg.Tools {
		items[i] = toolItem{name: tool.Name, backend: tool.Backend}
	}

	m.toolList.SetItems(items)
	return m
}

// handleVersionsLoaded processes the versions loaded message.
func (m model) handleVersionsLoaded(msg loader.VersionsLoadedMsg) model {
	m.versionsLoading = false

	if msg.Err != nil {
		m.logger.Error("error loading versions", "error", msg.Err)
		// Go back to tool selection
		m.pickerState = pickerSelectTool
		return m
	}

	m.logger.Debug("loaded versions", "tool", msg.Tool, "count", len(msg.Versions))

	// Initialize version list
	delegate := list.NewDefaultDelegate()
	width := m.windowWidth
	height := m.windowHeight
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}

	items := make([]list.Item, len(msg.Versions))
	for i, v := range msg.Versions {
		items[i] = versionItem{version: v}
	}

	m.versionList = list.New(items, delegate, width, height-pickerListPadding)
	m.versionList.Title = fmt.Sprintf("Select version for: %s", m.selectedTool)
	m.versionList.SetShowStatusBar(true)
	m.versionList.SetFilteringEnabled(true)

	m.pickerState = pickerSelectVersion
	return m
}

// handleToolInstalled processes the tool installed message.
func (m model) handleToolInstalled(msg loader.ToolInstalledMsg) (model, tea.Cmd) {
	if msg.Err != nil {
		m.logger.Error("error installing tool", "tool", msg.Tool, "version", msg.Version, "error", msg.Err)
		m.pickerState = pickerClosed
		return m, nil
	}

	m.logger.Debug("tool installed", "tool", msg.Tool, "version", msg.Version)
	m.pickerState = pickerClosed
	m.selectedTool = ""

	// Reload tools to show the new tool
	ctx := context.Background()
	return m, loader.LoadMiseTools(ctx, m.runner)
}

// handleToolRemoved processes the tool removed message.
func (m model) handleToolRemoved(msg loader.ToolRemovedMsg) (model, tea.Cmd) {
	if msg.Err != nil {
		m.logger.Error("error removing tool", "tool", msg.Tool, "version", msg.Version, "error", msg.Err)
		return m, nil
	}

	m.logger.Debug("tool removed", "tool", msg.Tool, "version", msg.Version)

	// Reload tools to reflect the removal
	ctx := context.Background()
	return m, loader.LoadMiseTools(ctx, m.runner)
}

// handlePickerUpdate handles all messages when the picker is open.
// The list component needs all message types (not just key presses) for filtering to work.
func (m model) handlePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handlePickerKeys(msg)

	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg), nil

	case loader.RegistryLoadedMsg:
		return m.handleRegistryLoaded(msg), nil

	case loader.VersionsLoadedMsg:
		return m.handleVersionsLoaded(msg), nil

	case loader.ToolInstalledMsg:
		return m.handleToolInstalled(msg)
	}

	// Pass all other messages to the active list for filtering/cursor blink etc.
	var cmd tea.Cmd
	switch m.pickerState {
	case pickerSelectTool:
		m.toolList, cmd = m.toolList.Update(msg)
	case pickerSelectVersion:
		m.versionList, cmd = m.versionList.Update(msg)
	case pickerSelectConfig:
		m.configList, cmd = m.configList.Update(msg)
	case pickerClosed, pickerLoadingVersions, pickerInstalling:
		// No list to update
	}
	return m, cmd
}

// handlePickerKeys handles key presses in the picker views.
func (m model) handlePickerKeys(msg tea.KeyPressMsg) (model, tea.Cmd) {
	switch m.pickerState {
	case pickerClosed:
		// Should not reach here, but handle for completeness
		return m, nil
	case pickerSelectTool:
		return m.handleToolListKeys(msg)
	case pickerSelectVersion:
		return m.handleVersionListKeys(msg)
	case pickerSelectConfig:
		return m.handleConfigListKeys(msg)
	case pickerLoadingVersions, pickerInstalling:
		// Only allow escape during loading/installing
		if msg.String() == keyEsc || msg.String() == "q" {
			return m.closeToolPicker(), nil
		}
	}
	return m, nil
}

// handleToolListKeys handles keys when selecting a tool.
func (m model) handleToolListKeys(msg tea.KeyPressMsg) (model, tea.Cmd) {
	// If the list is filtering, let it handle all keys (including esc to cancel filter)
	if m.toolList.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.toolList, cmd = m.toolList.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case keyEsc, "q":
		return m.closeToolPicker(), nil
	case keyEnter:
		if item := m.toolList.SelectedItem(); item != nil {
			tool, ok := item.(toolItem)
			if !ok {
				return m, nil
			}
			m.selectedTool = tool.name
			m.pickerState = pickerLoadingVersions
			m.versionsLoading = true
			m.logger.Debug("loading versions for tool", "tool", tool.name)
			ctx := context.Background()
			return m, loader.LoadToolVersions(ctx, m.runner, tool.name)
		}
		return m, nil
	}

	// Let list handle other keys (navigation, filtering)
	var cmd tea.Cmd
	m.toolList, cmd = m.toolList.Update(msg)
	return m, cmd
}

// handleVersionListKeys handles keys when selecting a version.
func (m model) handleVersionListKeys(msg tea.KeyPressMsg) (model, tea.Cmd) {
	// If the list is filtering, let it handle all keys (including esc to cancel filter)
	if m.versionList.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.versionList, cmd = m.versionList.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "q":
		return m.closeToolPicker(), nil
	case keyEsc:
		// Go back to tool selection
		m.pickerState = pickerSelectTool
		return m, nil
	case keyEnter:
		if item := m.versionList.SelectedItem(); item != nil {
			version, ok := item.(versionItem)
			if !ok {
				return m, nil
			}
			m.selectedVersion = version.version
			m.logger.Debug(
				"version selected, showing config picker",
				"tool", m.selectedTool,
				"version", version.version,
			)

			// Initialize config list with available config files
			return m.openConfigPicker()
		}
		return m, nil
	}

	// Let list handle other keys (navigation, filtering)
	var cmd tea.Cmd
	m.versionList, cmd = m.versionList.Update(msg)
	return m, cmd
}

// openConfigPicker opens the config file picker.
func (m model) openConfigPicker() (model, tea.Cmd) {
	m.logger.Debug("opening config picker", "configPaths", m.configPaths)
	m.pickerState = pickerSelectConfig

	// Initialize config list
	delegate := list.NewDefaultDelegate()
	width := m.windowWidth
	height := m.windowHeight
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}

	items := make([]list.Item, len(m.configPaths))
	for i, path := range m.configPaths {
		items[i] = configItem{path: path}
	}

	m.configList = list.New(items, delegate, width, height-pickerListPadding)
	m.configList.Title = fmt.Sprintf("Select config file for: %s@%s", m.selectedTool, m.selectedVersion)
	m.configList.SetShowStatusBar(true)
	m.configList.SetFilteringEnabled(true)

	return m, nil
}

// handleConfigListKeys handles keys when selecting a config file.
func (m model) handleConfigListKeys(msg tea.KeyPressMsg) (model, tea.Cmd) {
	// If the list is filtering, let it handle all keys (including esc to cancel filter)
	if m.configList.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.configList, cmd = m.configList.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "q":
		return m.closeToolPicker(), nil
	case keyEsc:
		// Go back to version selection
		m.pickerState = pickerSelectVersion
		return m, nil
	case keyEnter:
		if item := m.configList.SelectedItem(); item != nil {
			config, ok := item.(configItem)
			if !ok {
				return m, nil
			}
			m.pickerState = pickerInstalling
			m.logger.Debug(
				"installing tool",
				"tool", m.selectedTool,
				"version", m.selectedVersion,
				"config", config.path,
			)
			ctx := context.Background()
			return m, loader.InstallTool(
				ctx, m.runner, m.selectedTool, m.selectedVersion, config.path,
			)
		}
		return m, nil
	}

	// Let list handle other keys (navigation, filtering)
	var cmd tea.Cmd
	m.configList, cmd = m.configList.Update(msg)
	return m, cmd
}

func (m model) handleWindowSize(msg tea.WindowSizeMsg) tea.Model {
	m.windowWidth = msg.Width
	m.windowHeight = msg.Height

	// Update help bubble widths for graceful truncation
	m.tasksHelp.SetWidth(msg.Width)
	m.envVarsHelp.SetWidth(msg.Width)
	m.toolsHelp.SetWidth(msg.Width)
	m.outputHelp.SetWidth(msg.Width)
	m.argInputHelp.SetWidth(msg.Width)
	m.filterHelp.SetWidth(msg.Width)

	switch m.pickerState {
	case pickerSelectTool:
		m.toolList.SetSize(msg.Width, msg.Height-pickerListPadding)
	case pickerSelectVersion:
		m.versionList.SetSize(msg.Width, msg.Height-pickerListPadding)
	case pickerSelectConfig:
		m.configList.SetSize(msg.Width, msg.Height-pickerListPadding)
	case pickerClosed, pickerLoadingVersions, pickerInstalling:
		// No list to resize
	}
	if m.showOutput {
		// Preserve scroll position ratio
		oldYOffset := m.viewport.YOffset()
		oldTotalHeight := m.viewport.TotalLineCount()

		// Update viewport dimensions (reuse instance instead of recreating)
		m.viewport.SetWidth(msg.Width)
		m.viewport.SetHeight(msg.Height - viewportHeaderFooterHeight)

		// Re-apply content with wrapping at new width
		displayLines := wrapOutputLines(m.output, m.viewport.Width(), m.wrapOutput)
		m.viewport.SetContentLines(displayLines)

		// Restore relative scroll position
		if oldTotalHeight > 0 && m.viewport.TotalLineCount() > 0 {
			newYOffset := (oldYOffset * m.viewport.TotalLineCount()) / oldTotalHeight
			m.viewport.SetYOffset(newYOffset)
		} else {
			m.viewport.GotoBottom()
		}
	} else {
		// Update table layout based on terminal size
		m = updateTableLayout(m)
	}
	return m
}

// openEditor launches the configured editor to edit a file.
// The TUI is suspended while the editor runs.
func (m model) openEditor(filePath string) tea.Cmd {
	parts, err := shlex.Split(m.editor)
	if err != nil || len(parts) == 0 {
		m.logger.Error("failed to parse editor command", "editor", m.editor, "error", err)
		return func() tea.Msg {
			return editorClosedMsg{err: fmt.Errorf("invalid editor command: %w", err)}
		}
	}

	executable := parts[0]
	var args []string
	args = append(args, parts[1:]...)
	args = append(args, filePath)

	m.logger.Debug("launching editor", "executable", executable, "args", args)

	cmd := exec.CommandContext(context.Background(), executable, args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorClosedMsg{err: err}
	})
}

var _ tea.ExecCommand = &interactiveTaskCommand{}

// interactiveTaskCommand implements tea.ExecCommand to run a mise task
// interactively and wait for user confirmation before returning to the TUI.
type interactiveTaskCommand struct {
	taskName string
	args     []string
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
}

// Run executes the task and waits for user confirmation.
func (c *interactiveTaskCommand) Run() error {
	cmdArgs := []string{"run", c.taskName}
	if len(c.args) > 0 {
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, c.args...)
	}

	cmd := exec.CommandContext(context.Background(), "mise", cmdArgs...)

	cmd.Stdin = c.stdin
	cmd.Stdout = c.stdout
	cmd.Stderr = c.stderr

	// Run the command and capture the error
	err := cmd.Run()

	// Determine the exit code from the error
	exitCode := 0
	if err != nil {
		exitErr := &exec.ExitError{}
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1 // Indicates a non-exit error (e.g., command not found)
		}
	}

	// Print status and prompt for user confirmation
	fmt.Fprintln(c.stdout)
	fmt.Fprintln(c.stdout, "────────────────────────────────")
	if exitCode == 0 {
		fmt.Fprintln(c.stdout, "Task completed successfully.")
	} else {
		fmt.Fprintf(c.stdout, "Task failed with exit code %d.\n", exitCode)
	}
	fmt.Fprintln(c.stdout, "Press Enter to return to the task list.")

	// Wait for user to press Enter
	if reader, ok := c.stdin.(*os.File); ok {
		_, _ = bufio.NewReader(reader).ReadBytes('\n')
	}

	return err
}

// SetStdin sets the stdin for the command.
func (c *interactiveTaskCommand) SetStdin(r io.Reader) {
	c.stdin = r
}

// SetStdout sets the stdout for the command.
func (c *interactiveTaskCommand) SetStdout(w io.Writer) {
	c.stdout = w
}

// SetStderr sets the stderr for the command.
func (c *interactiveTaskCommand) SetStderr(w io.Writer) {
	c.stderr = w
}

// runInteractiveTask suspends the TUI and executes a mise task with full
// terminal access, then waits for user confirmation before returning.
func (m model) runInteractiveTask(taskName string, args ...string) tea.Cmd {
	m.logger.Debug("launching interactive task", "task", taskName, "args", args)

	cmd := &interactiveTaskCommand{
		taskName: taskName,
		args:     args,
	}

	return tea.Exec(cmd, func(err error) tea.Msg {
		return interactiveTaskClosedMsg{
			taskName: taskName,
			err:      err,
		}
	})
}

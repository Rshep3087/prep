package main

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/rshep3087/prep/internal/loader"
	"github.com/rshep3087/prep/internal/watcher"
)

// debounceInterval is the minimum time between file change reloads.
const debounceInterval = 500 * time.Millisecond

// Key constants for common key bindings.
const (
	keyEsc   = "esc"
	keyEnter = "enter"
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
	m.viewport.SetContent(strings.Join(m.output, "\n"))
	m.viewport.GotoBottom()
	return m
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

// handleFileChanged processes file change events with debouncing.
func (m model) handleFileChanged(msg watcher.FileChangedMsg) (model, tea.Cmd) {
	if time.Since(m.lastReload) < debounceInterval {
		return m, nil
	}
	m.lastReload = time.Now()
	m.logger.Debug("config file changed, reloading mise data", "path", msg.Path)
	return m, loader.ReloadMiseData(m.runner)
}

// handleMainKeys handles key presses in the main view (task list).
// Returns (model, cmd, handled) where handled indicates if the key was consumed.
func (m model) handleMainKeys(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "ctrl+c", keyEsc:
		watcher.Close(m.watcher)
		return m, tea.Quit, true
	case keyEnter:
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
	case "v":
		// Show selected env var value (only when focused on env vars)
		if m.focus == focusEnvVars {
			m = showSelectedEnvVar(m)
			return m, nil, true
		}
	case "V":
		// Show all env var values (only when focused on env vars)
		if m.focus == focusEnvVars {
			m = showAllEnvVars(m)
			return m, nil, true
		}
	case "h":
		// Hide all env var values (only when focused on env vars)
		if m.focus == focusEnvVars {
			m = hideAllEnvVars(m)
			return m, nil, true
		}
	case "a":
		// Add tool (only when focused on tools table)
		if m.focus == focusTools {
			return m.openToolPicker()
		}
	case "u":
		// Unuse tool (only when focused on tools table)
		if m.focus == focusTools {
			return m.unuseTool()
		}
	case "e":
		// Edit source file (only when focused on tasks or tools table)
		return m.editSourceFile()
	}
	// Key not handled - let tables process it
	return m, nil, false
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

// handleOutputKeys handles key presses in the output view.
func (m model) handleOutputKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", keyEsc:
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
func runTask(ctx context.Context, taskName string, sender messageSender) tea.Cmd {
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

// startTask initializes and starts a task execution.
func (m model) startTask(taskName string) (model, tea.Cmd) {
	m.logger.Debug("starting task", "task", taskName)

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
	m.totalOutputLines = 0
	m.cancelFunc = cancel

	return m, runTask(ctx, taskName, m.sender)
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
		m.viewport = viewport.New(
			viewport.WithWidth(msg.Width),
			viewport.WithHeight(msg.Height-viewportHeaderFooterHeight),
		)
		m.viewport.SetContent(strings.Join(m.output, "\n"))
	} else {
		// Update table layout based on terminal size
		m = updateTableLayout(m)
	}
	return m
}

// openEditor launches the configured editor to edit a file.
// The TUI is suspended while the editor runs.
func (m model) openEditor(filePath string) tea.Cmd {
	//nolint:gosec // editor and filePath are controlled by the application
	cmd := exec.CommandContext(context.Background(), m.editor, filePath)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorClosedMsg{err: err}
	})
}

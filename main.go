package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/fsnotify/fsnotify"
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

// miseVersionMsg is sent when mise version is loaded.
type miseVersionMsg struct {
	version string
	err     error
}

// fileChangedMsg is sent when a watched config file changes.
type fileChangedMsg struct {
	path string
}

// configFilesLoadedMsg is sent when config file paths are loaded from mise.
type configFilesLoadedMsg struct {
	paths []string
	err   error
}

// debounceInterval is the minimum time between file change reloads.
const debounceInterval = 500 * time.Millisecond

// reloadMiseData returns commands to reload all mise data.
func reloadMiseData(runner CommandRunner) tea.Cmd {
	ctx := context.Background()
	return tea.Batch(
		loadMiseTasks(ctx, runner),
		loadMiseTools(ctx, runner),
		loadMiseEnvVars(ctx, runner),
	)
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

// loadMiseVersion returns a Cmd that loads the mise version asynchronously.
func loadMiseVersion(ctx context.Context, runner CommandRunner) tea.Cmd {
	return func() tea.Msg {
		output, err := runner.Run(ctx, "mise", "--version")
		if err != nil {
			return miseVersionMsg{err: err}
		}
		// mise --version outputs something like "2024.12.0 macos-arm64 (2024-12-01)"
		// We just want the version number
		version := strings.TrimSpace(string(output))
		if parts := strings.Fields(version); len(parts) > 0 {
			version = parts[0]
		}
		return miseVersionMsg{version: version}
	}
}

// miseConfigEntry represents a config file entry from mise cfg --json.
type miseConfigEntry struct {
	Path string `json:"path"`
}

// loadMiseConfigFiles returns a Cmd that loads config file paths from mise.
func loadMiseConfigFiles(ctx context.Context, runner CommandRunner) tea.Cmd {
	return loadJSON(ctx, runner, []string{"mise", "cfg", "--json"},
		func(configs []miseConfigEntry) tea.Msg {
			paths := make([]string, len(configs))
			for i, c := range configs {
				paths[i] = c.Path
			}
			return configFilesLoadedMsg{paths: paths}
		},
		func(err error) tea.Msg { return configFilesLoadedMsg{err: err} },
	)
}

// startFileWatcher creates an fsnotify watcher and monitors config files.
// It watches parent directories (more reliable for editor saves) and filters
// events to only the specified config files.
func startFileWatcher(paths []string, sender MessageSender) (*fsnotify.Watcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Build a set of config file paths for filtering
	configFiles := make(map[string]bool)
	for _, p := range paths {
		configFiles[p] = true
	}

	// Add parent directories to watch
	if addErr := addWatchDirs(watcher, paths); addErr != nil {
		_ = watcher.Close()
		return nil, addErr
	}

	// Start goroutine to listen for events
	go watchLoop(watcher, configFiles, sender)

	return watcher, nil
}

// addWatchDirs adds parent directories of the given paths to the watcher.
func addWatchDirs(watcher *fsnotify.Watcher, paths []string) error {
	watchedDirs := make(map[string]bool)
	for _, p := range paths {
		dir := filepath.Dir(p)
		if watchedDirs[dir] {
			continue
		}
		if err := watcher.Add(dir); err != nil {
			return fmt.Errorf("watching %s: %w", dir, err)
		}
		watchedDirs[dir] = true
	}
	return nil
}

// watchLoop listens for fsnotify events and sends messages for matching config files.
func watchLoop(watcher *fsnotify.Watcher, configFiles map[string]bool, sender MessageSender) {
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) && configFiles[event.Name] {
				sender.Send(fileChangedMsg{path: event.Name})
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
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

// renderHeader renders the application header with branding and mise version.
func (m model) renderHeader() string {
	tagline := m.styles.title.Render("prep") + m.styles.dimTitle.Render(" — mise en place, now prep")

	var versionLine string
	if m.miseVersion != "" {
		versionLine = m.styles.help.Render("mise v" + m.miseVersion)
	}

	return lipgloss.JoinVertical(lipgloss.Left, tagline, versionLine)
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
	t := table.New(
		table.WithColumns(cfg.columns),
		table.WithRows(rows),
		table.WithFocused(focused),
		table.WithStyles(tableStyles()),
		table.WithWidth(cfg.width),
		table.WithHeight(minTableHeight), // Start with minimum, updateTableLayout will adjust
	)
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
	runner CommandRunner // for running commands
	sender MessageSender // for sending messages to the program
	styles styles        // UI styles

	// File watching state
	watcher     *fsnotify.Watcher // watches config files for changes
	configPaths []string          // paths being watched
	lastReload  time.Time         // for debouncing file change events
}

func (m model) Init() tea.Cmd {
	ctx := context.Background()
	return tea.Batch(
		loadMiseTasks(ctx, m.runner),
		loadMiseTools(ctx, m.runner),
		loadMiseEnvVars(ctx, m.runner),
		loadMiseVersion(ctx, m.runner),
		loadMiseConfigFiles(ctx, m.runner),
	)
}

// Table layout constants for resize calculations.
const (
	tablePadding  = 4 // padding for table borders
	columnPadding = 5 // padding between columns

	// Vertical layout constants.
	minTableHeight     = 4 // minimum rows to show (header + at least 1 data row + border)
	headerLines        = 3 // header block (title + version + blank line)
	sectionTitleLines  = 1 // each section title
	sectionSpacerLines = 1 // blank line between sections
	helpLines          = 2 // help text + blank line before it
	numTables          = 3 // tasks, tools, env vars
)

// calculateTableHeights distributes available vertical space among tables.
// Returns heights for tasks, tools, and envVars tables.
func calculateTableHeights(windowHeight, taskRows, toolRows, envVarRows int) (int, int, int) {
	if windowHeight == 0 {
		return minTableHeight, minTableHeight, minTableHeight
	}

	// Calculate overhead: header + 3 section titles + 3 spacers + help
	overhead := headerLines + (numTables * sectionTitleLines) + (numTables * sectionSpacerLines) + helpLines
	availableHeight := windowHeight - overhead

	if availableHeight < numTables*minTableHeight {
		// Not enough space, give minimum to each
		return minTableHeight, minTableHeight, minTableHeight
	}

	// Calculate how much each table needs (rows + header line)
	const tableHeaderHeight = 2 // header row + border
	taskNeeds := taskRows + tableHeaderHeight
	toolNeeds := toolRows + tableHeaderHeight
	envVarNeeds := envVarRows + tableHeaderHeight

	totalNeeds := taskNeeds + toolNeeds + envVarNeeds

	if totalNeeds <= availableHeight {
		// Everything fits, give each table what it needs
		return taskNeeds, toolNeeds, envVarNeeds
	}

	// Not everything fits - distribute proportionally with minimums
	// First, ensure minimums
	taskHeight := minTableHeight
	toolHeight := minTableHeight
	envVarHeight := minTableHeight
	remaining := availableHeight - (numTables * minTableHeight)

	if remaining > 0 {
		// Distribute extra space proportionally based on row counts
		totalRows := taskRows + toolRows + envVarRows
		if totalRows > 0 {
			taskExtra := (remaining * taskRows) / totalRows
			toolExtra := (remaining * toolRows) / totalRows
			envVarExtra := remaining - taskExtra - toolExtra // give remainder to last

			taskHeight += taskExtra
			toolHeight += toolExtra
			envVarHeight += envVarExtra
		} else {
			// No rows, distribute evenly
			each := remaining / numTables
			taskHeight += each
			toolHeight += each
			envVarHeight += remaining - (numTables-1)*each
		}
	}

	return taskHeight, toolHeight, envVarHeight
}

// updateTableLayout adjusts table widths and heights based on the current terminal size.
func updateTableLayout(m model) model {
	if m.windowWidth == 0 {
		return m
	}

	// Calculate heights based on available space and row counts
	taskHeight, toolHeight, envVarHeight := calculateTableHeights(
		m.windowHeight,
		len(m.tasks),
		len(m.tools),
		len(m.envVars),
	)

	m.tasksTable.SetHeight(taskHeight)
	m.toolsTable.SetHeight(toolHeight)
	m.envVarsTable.SetHeight(envVarHeight)

	// Force viewport update after height change
	m.tasksTable.UpdateViewport()
	m.toolsTable.UpdateViewport()
	m.envVarsTable.UpdateViewport()

	return updateTableWidths(m)
}

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

	// Tools table: Name + Version + Requested columns (expand last column for consistent divider)
	toolsNameWidth := colWidthName
	toolsVersionWidth := colWidthVersion
	toolsRequestedWidth := max(
		availableWidth-toolsNameWidth-toolsVersionWidth-columnPadding-columnPadding,
		colWidthVersion,
	)
	m.toolsTable.SetColumns([]table.Column{
		{Title: "Name", Width: toolsNameWidth},
		{Title: "Version", Width: toolsVersionWidth},
		{Title: "Requested", Width: toolsRequestedWidth},
	})
	m.toolsTable.SetWidth(availableWidth)

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

	// Sort tasks by name for stable ordering
	slices.SortFunc(msg.tasks, func(a, b Task) int {
		return cmp.Compare(a.Name, b.Name)
	})

	m.tasks = msg.tasks
	m.tasksLoading = false

	rows := make([]table.Row, 0, len(m.tasks))
	for _, task := range m.tasks {
		rows = append(rows, table.Row{task.Name, task.Description})
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
func handleToolsLoaded(m model, msg toolsLoadedMsg) model {
	if msg.err != nil {
		log.Printf("error loading tools: %v", msg.err)
		m.err = msg.err
		m.toolsLoading = false
		return m
	}

	log.Printf("loaded %d tools", len(msg.tools))

	// Sort tools by name for stable ordering
	slices.SortFunc(msg.tools, func(a, b Tool) int {
		return cmp.Compare(a.Name, b.Name)
	})

	m.tools = msg.tools
	m.toolsLoading = false

	rows := make([]table.Row, 0, len(m.tools))
	for _, tool := range m.tools {
		rows = append(rows, table.Row{tool.Name, tool.Version, tool.RequestedVersion})
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
func handleEnvVarsLoaded(m model, msg envVarsLoadedMsg) model {
	if msg.err != nil {
		log.Printf("error loading env vars: %v", msg.err)
		m.err = msg.err
		m.envVarsLoading = false
		return m
	}

	log.Printf("loaded %d env vars", len(msg.envVars))

	// Sort env vars by name for stable ordering
	slices.SortFunc(msg.envVars, func(a, b EnvVar) int {
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
	for i := range msg.envVars {
		if unmasked[msg.envVars[i].Name] {
			msg.envVars[i].Masked = false
		}
	}

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

	// Update rows on existing table instead of recreating
	m.envVarsTable.SetRows(rows)

	// Re-apply layout settings if we have window dimensions
	if m.windowWidth > 0 {
		m = updateTableLayout(m)
	}
	return m
}

// handleMiseVersion processes the miseVersionMsg and updates the model.
func handleMiseVersion(m model, msg miseVersionMsg) model {
	if msg.err != nil {
		log.Printf("error loading mise version: %v", msg.err)
		return m
	}
	m.miseVersion = msg.version
	log.Printf("loaded mise version: %s", msg.version)
	return m
}

// handleTaskOutput appends task output and updates the viewport.
func handleTaskOutput(m model, msg taskOutputMsg) model {
	m.output = append(m.output, msg.line)
	m.viewport.SetContent(strings.Join(m.output, "\n"))
	m.viewport.GotoBottom()
	return m
}

// handleTaskDone processes task completion.
func handleTaskDone(m model, msg taskDoneMsg) model {
	m.taskRunning = false
	m.taskErr = msg.err
	m.cancelFunc = nil
	if msg.err != nil {
		log.Printf("task %s finished with error: %v", m.runningTask, msg.err)
	} else {
		log.Printf("task %s finished successfully", m.runningTask)
	}
	return m
}

// handleConfigFilesLoaded processes config files and starts the file watcher.
func handleConfigFilesLoaded(m model, msg configFilesLoadedMsg) model {
	if msg.err != nil {
		log.Printf("error loading config files: %v", msg.err)
		return m
	}
	m.configPaths = msg.paths
	log.Printf("loaded %d config files to watch", len(msg.paths))
	watcher, err := startFileWatcher(msg.paths, m.sender)
	if err != nil {
		log.Printf("error starting file watcher: %v", err)
		return m
	}
	m.watcher = watcher
	return m
}

// handleFileChanged processes file change events with debouncing.
func handleFileChanged(m model, msg fileChangedMsg) (model, tea.Cmd) {
	if time.Since(m.lastReload) < debounceInterval {
		return m, nil
	}
	m.lastReload = time.Now()
	log.Printf("config file changed: %s, reloading mise data", msg.path)
	return m, reloadMiseData(m.runner)
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
		return handleTaskOutput(m, msg), nil

	case taskDoneMsg:
		return handleTaskDone(m, msg), nil

	case tasksLoadedMsg:
		return handleTasksLoaded(m, msg), nil

	case toolsLoadedMsg:
		return handleToolsLoaded(m, msg), nil

	case envVarsLoadedMsg:
		return handleEnvVarsLoaded(m, msg), nil

	case miseVersionMsg:
		return handleMiseVersion(m, msg), nil

	case configFilesLoadedMsg:
		return handleConfigFilesLoaded(m, msg), nil

	case fileChangedMsg:
		return handleFileChanged(m, msg)

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

// closeWatcher safely closes the file watcher if it exists.
func closeWatcher(w *fsnotify.Watcher) {
	if w != nil {
		_ = w.Close()
	}
}

// handleMainKeys handles key presses in the main view (task list).
// Returns (model, cmd, handled) where handled indicates if the key was consumed.
func (m model) handleMainKeys(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	switch msg.String() {
	case "q", "ctrl+c", "esc":
		closeWatcher(m.watcher)
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
			closeWatcher(m.watcher)
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
		tasksTable:     newTable(getTasksTableConfig(), nil, true),
		toolsTable:     newTable(getToolsTableConfig(), nil, false),
		envVarsTable:   newTable(getEnvVarsTableConfig(), nil, false),
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

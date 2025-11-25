package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// formatSourcePath formats a config file path for display.
// It abbreviates the home directory to ~ and uses the current working directory
// to show relative paths for project-local configs.
func formatSourcePath(path string) string {
	if path == "" {
		return ""
	}

	// Get home directory and current working directory
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()

	// If path is under cwd, show relative path
	if cwd != "" && strings.HasPrefix(path, cwd) {
		rel, err := filepath.Rel(cwd, path)
		if err == nil {
			return rel
		}
	}

	// Otherwise, abbreviate home directory to ~
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}

	return path
}

const (
	// Focus constants.
	focusTasks = iota
	focusTools
	focusEnvVars
	focusSectionCount // total number of focus sections for cycling
)

const (
	// Column width constants.
	colWidthName        = 20
	colWidthDescription = 40
	colWidthVersion     = 15
	colWidthValue       = 50
	colWidthEnvName     = 30
	colWidthSource      = 25

	// Table width constants.
	tableWidthWide = 82

	// Input width constants.
	defaultInputWidth = 80 // default width for text input fields

	// Table layout constants for resize calculations.
	tablePadding  = 4 // padding for table borders
	columnPadding = 5 // padding between columns

	// Vertical layout constants.
	minTableHeight     = 4 // minimum rows to show (header + at least 1 data row + border)
	headerLines        = 3 // header block (title + version + blank line)
	sectionTitleLines  = 1 // each section title
	sectionSpacerLines = 1 // blank line between sections
	helpLines          = 2 // help text + blank line before it
	numTables          = 3 // tasks, tools, env vars

	// viewportHeaderFooterHeight is the space reserved for header and footer in output view.
	viewportHeaderFooterHeight = 4

	// pickerListPadding is the space reserved for header/footer in picker views.
	pickerListPadding = 4

	// maxOutputLines is the maximum number of output lines to keep in memory.
	// When this limit is exceeded, older lines are dropped in a rolling buffer fashion.
	maxOutputLines = 10000
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
			{Title: "Source", Width: colWidthSource},
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
			{Title: "Source", Width: colWidthSource},
		},
		width: tableWidthWide,
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

// renderArgInputView renders the argument input view.
func (m model) renderArgInputView() tea.View {
	title := m.styles.title.Render(fmt.Sprintf("Run task: %s", m.argInputTask))
	prompt := m.styles.help.Render("Enter arguments for the task:")
	help := m.styles.help.Render("Enter to run • Esc to cancel")

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		"",
		prompt,
		m.argInput.View(),
		"",
		help,
	)

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

// updateTableWidths adjusts table widths based on the current terminal width.
func updateTableWidths(m model) model {
	if m.windowWidth == 0 {
		return m
	}

	// Use available width (with some padding for borders)
	availableWidth := m.windowWidth - tablePadding

	// Tasks table: Name + Description + Source columns
	// Source column expands to fill remaining space
	tasksNameWidth := colWidthName
	tasksDescWidth := colWidthDescription
	tasksSourceWidth := max(
		availableWidth-tasksNameWidth-tasksDescWidth-columnPadding*2,
		colWidthSource,
	)
	m.tasksTable.SetColumns([]table.Column{
		{Title: "Name", Width: tasksNameWidth},
		{Title: "Description", Width: tasksDescWidth},
		{Title: "Source", Width: tasksSourceWidth},
	})
	m.tasksTable.SetWidth(availableWidth)

	// Tools table: Name + Version + Requested + Source columns
	toolsNameWidth := colWidthName
	toolsVersionWidth := colWidthVersion
	toolsRequestedWidth := colWidthVersion
	toolsSourceWidth := max(
		availableWidth-toolsNameWidth-toolsVersionWidth-toolsRequestedWidth-columnPadding*3,
		colWidthSource,
	)
	m.toolsTable.SetColumns([]table.Column{
		{Title: "Name", Width: toolsNameWidth},
		{Title: "Version", Width: toolsVersionWidth},
		{Title: "Requested", Width: toolsRequestedWidth},
		{Title: "Source", Width: toolsSourceWidth},
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

package main

import (
	"charm.land/bubbles/v2/key"
)

// tasksKeyMap defines key bindings for the tasks view.
type tasksKeyMap struct {
	Tab      key.Binding
	UpDown   key.Binding
	Enter    key.Binding
	AltEnter key.Binding
	Edit     key.Binding
	Quit     key.Binding
}

// newTasksKeyMap creates a new tasksKeyMap.
func newTasksKeyMap() tasksKeyMap {
	return tasksKeyMap{
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("Tab", "switch"),
		),
		UpDown: key.NewBinding(
			key.WithKeys("up", "down", "j", "k"),
			key.WithHelp("↑/↓/j/k", "navigate"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("Enter", "run"),
		),
		AltEnter: key.NewBinding(
			key.WithKeys("alt+enter"),
			key.WithHelp("Alt+Enter", "args"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit source"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns keybindings to be shown in the mini help view.
func (k tasksKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.UpDown, k.Enter, k.AltEnter, k.Edit, k.Quit}
}

// FullHelp returns keybindings for the expanded help view.
func (k tasksKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// toolsKeyMap defines key bindings for the tools view.
type toolsKeyMap struct {
	Tab    key.Binding
	UpDown key.Binding
	Add    key.Binding
	Unuse  key.Binding
	Edit   key.Binding
	Quit   key.Binding
}

// newToolsKeyMap creates a new toolsKeyMap.
func newToolsKeyMap() toolsKeyMap {
	return toolsKeyMap{
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("Tab", "switch"),
		),
		UpDown: key.NewBinding(
			key.WithKeys("up", "down", "j", "k"),
			key.WithHelp("↑/↓/j/k", "navigate"),
		),
		Add: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "add"),
		),
		Unuse: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "unuse"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit source"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns keybindings to be shown in the mini help view.
func (k toolsKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.UpDown, k.Add, k.Unuse, k.Edit, k.Quit}
}

// FullHelp returns keybindings for the expanded help view.
func (k toolsKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// envVarsKeyMap defines key bindings for the environment variables view.
type envVarsKeyMap struct {
	Tab     key.Binding
	UpDown  key.Binding
	ShowOne key.Binding
	ShowAll key.Binding
	HideAll key.Binding
	Quit    key.Binding
}

// newEnvVarsKeyMap creates a new envVarsKeyMap.
func newEnvVarsKeyMap() envVarsKeyMap {
	return envVarsKeyMap{
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("Tab", "switch"),
		),
		UpDown: key.NewBinding(
			key.WithKeys("up", "down", "j", "k"),
			key.WithHelp("↑/↓/j/k", "navigate"),
		),
		ShowOne: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "show"),
		),
		ShowAll: key.NewBinding(
			key.WithKeys("V"),
			key.WithHelp("V", "show all"),
		),
		HideAll: key.NewBinding(
			key.WithKeys("h"),
			key.WithHelp("h", "hide all"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns keybindings to be shown in the mini help view.
func (k envVarsKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.UpDown, k.ShowOne, k.ShowAll, k.HideAll, k.Quit}
}

// FullHelp returns keybindings for the expanded help view.
func (k envVarsKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// outputKeyMap defines key bindings for the output view.
type outputKeyMap struct {
	Cancel key.Binding
	Scroll key.Binding
	Close  key.Binding
}

// newOutputKeyMap creates a new outputKeyMap.
// running indicates if a task is currently running.
func newOutputKeyMap(running bool) outputKeyMap {
	if running {
		return outputKeyMap{
			Cancel: key.NewBinding(
				key.WithKeys("ctrl+c"),
				key.WithHelp("Ctrl+C", "cancel"),
			),
			Scroll: key.NewBinding(
				key.WithKeys("up", "down", "j", "k"),
				key.WithHelp("↑/↓/j/k", "scroll"),
			),
		}
	}
	return outputKeyMap{
		Close: key.NewBinding(
			key.WithKeys("esc", "q"),
			key.WithHelp("Esc/q", "close"),
		),
		Scroll: key.NewBinding(
			key.WithKeys("up", "down", "j", "k"),
			key.WithHelp("↑/↓/j/k", "scroll"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("Ctrl+C", "quit"),
		),
	}
}

// ShortHelp returns keybindings to be shown in the mini help view.
func (k outputKeyMap) ShortHelp() []key.Binding {
	if k.Close.Enabled() {
		return []key.Binding{k.Close, k.Scroll, k.Cancel}
	}
	return []key.Binding{k.Cancel, k.Scroll}
}

// FullHelp returns keybindings for the expanded help view.
func (k outputKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// argInputKeyMap defines key bindings for the argument input view.
type argInputKeyMap struct {
	Enter  key.Binding
	Cancel key.Binding
}

// newArgInputKeyMap creates a new argInputKeyMap.
func newArgInputKeyMap() argInputKeyMap {
	return argInputKeyMap{
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("Enter", "run"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("Esc", "cancel"),
		),
	}
}

// ShortHelp returns keybindings to be shown in the mini help view.
func (k argInputKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Enter, k.Cancel}
}

// FullHelp returns keybindings for the expanded help view.
func (k argInputKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

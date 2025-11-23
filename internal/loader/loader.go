// Package loader provides functions to load data from mise.
package loader

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// CommandRunner runs commands.
type CommandRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
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

// EnvVar represents a mise environment variable.
type EnvVar struct {
	Name   string
	Value  string
	Masked bool
}

// TasksLoadedMsg is sent when tasks are loaded from mise.
type TasksLoadedMsg struct {
	Tasks []Task
	Err   error
}

// ToolsLoadedMsg is sent when tools are loaded from mise.
type ToolsLoadedMsg struct {
	Tools []Tool
	Err   error
}

// EnvVarsLoadedMsg is sent when environment variables are loaded from mise.
type EnvVarsLoadedMsg struct {
	EnvVars []EnvVar
	Err     error
}

// MiseVersionMsg is sent when mise version is loaded.
type MiseVersionMsg struct {
	Version string
	Err     error
}

// miseConfigEntry represents a config file entry from mise cfg --json.
type miseConfigEntry struct {
	Path string `json:"path"`
}

// ConfigFilesLoadedMsg is sent when config file paths are loaded from mise.
type ConfigFilesLoadedMsg struct {
	Paths []string
	Err   error
}

// ReloadMiseData returns commands to reload all mise data.
func ReloadMiseData(runner CommandRunner) tea.Cmd {
	ctx := context.Background()
	return tea.Batch(
		LoadMiseTasks(ctx, runner),
		LoadMiseTools(ctx, runner),
		LoadMiseEnvVars(ctx, runner),
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

// LoadMiseTasks returns a Cmd that loads tasks asynchronously.
func LoadMiseTasks(ctx context.Context, runner CommandRunner) tea.Cmd {
	return loadJSON(ctx, runner, []string{"mise", "tasks", "--json"},
		func(tasks []Task) tea.Msg { return TasksLoadedMsg{Tasks: tasks} },
		func(err error) tea.Msg { return TasksLoadedMsg{Err: err} },
	)
}

// LoadMiseTools returns a Cmd that loads tools asynchronously.
func LoadMiseTools(ctx context.Context, runner CommandRunner) tea.Cmd {
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
			return ToolsLoadedMsg{Tools: tools}
		},
		func(err error) tea.Msg { return ToolsLoadedMsg{Err: err} },
	)
}

// LoadMiseEnvVars returns a Cmd that loads environment variables asynchronously.
func LoadMiseEnvVars(ctx context.Context, runner CommandRunner) tea.Cmd {
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
			return EnvVarsLoadedMsg{EnvVars: envVars}
		},
		func(err error) tea.Msg { return EnvVarsLoadedMsg{Err: err} },
	)
}

// LoadMiseVersion returns a Cmd that loads the mise version asynchronously.
func LoadMiseVersion(ctx context.Context, runner CommandRunner) tea.Cmd {
	return func() tea.Msg {
		output, err := runner.Run(ctx, "mise", "--version")
		if err != nil {
			return MiseVersionMsg{Err: err}
		}
		// mise --version outputs something like "2024.12.0 macos-arm64 (2024-12-01)"
		// We just want the version number
		version := strings.TrimSpace(string(output))
		if parts := strings.Fields(version); len(parts) > 0 {
			version = parts[0]
		}
		return MiseVersionMsg{Version: version}
	}
}

// LoadMiseConfigFiles returns a Cmd that loads config file paths from mise.
func LoadMiseConfigFiles(ctx context.Context, runner CommandRunner) tea.Cmd {
	return loadJSON(ctx, runner, []string{"mise", "cfg", "--json"},
		func(configs []miseConfigEntry) tea.Msg {
			paths := make([]string, len(configs))
			for i, c := range configs {
				paths[i] = c.Path
			}
			return ConfigFilesLoadedMsg{Paths: paths}
		},
		func(err error) tea.Msg { return ConfigFilesLoadedMsg{Err: err} },
	)
}

package loader_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rshep3087/prep/internal/loader"
)

// Ensure CommandRunnerMock implements loader.CommandRunner.
var _ loader.CommandRunner = &CommandRunnerMock{}

// CommandRunnerMock is a mock implementation of CommandRunner.
type CommandRunnerMock struct {
	RunFunc func(ctx context.Context, args ...string) ([]byte, error)
}

// Run calls RunFunc.
func (m *CommandRunnerMock) Run(ctx context.Context, args ...string) ([]byte, error) {
	return m.RunFunc(ctx, args...)
}

func TestLoadMiseRegistry(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		runErr     error
		wantErr    bool
		wantTools  int
		checkFirst *loader.RegistryTool
	}{
		{
			name: "parses valid registry output",
			output: `node    core:node
python  core:python
go      core:go`,
			wantTools:  3,
			checkFirst: &loader.RegistryTool{Name: "node", Backend: "core:node"},
		},
		{
			name:       "handles empty output",
			output:     "",
			wantTools:  0,
			checkFirst: nil,
		},
		{
			name: "skips blank lines",
			output: `node    core:node

python  core:python
`,
			wantTools: 2,
		},
		{
			name:       "skips lines with insufficient fields",
			output:     "node\npython  core:python",
			wantTools:  1,
			checkFirst: &loader.RegistryTool{Name: "python", Backend: "core:python"},
		},
		{
			name:    "handles runner error",
			runErr:  errors.New("command failed"),
			wantErr: true,
		},
		{
			name:      "handles extra whitespace",
			output:    "  node    core:node  \n  python    core:python  ",
			wantTools: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &CommandRunnerMock{
				RunFunc: func(_ context.Context, _ ...string) ([]byte, error) {
					return []byte(tt.output), tt.runErr
				},
			}
			cmd := loader.LoadMiseRegistry(context.Background(), runner)
			msg := cmd()

			loaded, ok := msg.(loader.RegistryLoadedMsg)
			if !ok {
				t.Fatalf("expected loader.RegistryLoadedMsg, got %T", msg)
			}

			if tt.wantErr {
				if loaded.Err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if loaded.Err != nil {
				t.Errorf("unexpected error: %v", loaded.Err)
				return
			}

			if len(loaded.Tools) != tt.wantTools {
				t.Errorf("got %d tools, want %d", len(loaded.Tools), tt.wantTools)
			}

			assertFirstRegistryTool(t, loaded.Tools, tt.checkFirst)
		})
	}
}

func assertFirstRegistryTool(t *testing.T, tools []loader.RegistryTool, want *loader.RegistryTool) {
	t.Helper()

	if want == nil || len(tools) == 0 {
		return
	}

	if tools[0].Name != want.Name {
		t.Errorf("first tool name = %q, want %q", tools[0].Name, want.Name)
	}
	if tools[0].Backend != want.Backend {
		t.Errorf("first tool backend = %q, want %q", tools[0].Backend, want.Backend)
	}
}

func TestLoadToolVersions(t *testing.T) {
	tests := []struct {
		name         string
		tool         string
		output       string
		runErr       error
		wantErr      bool
		wantVersions int
		wantFirst    string // newest should be first after reversal
		wantLast     string
	}{
		{
			name:         "reverses version order",
			tool:         "node",
			output:       "18.0.0\n20.0.0\n22.0.0",
			wantVersions: 3,
			wantFirst:    "22.0.0",
			wantLast:     "18.0.0",
		},
		{
			name:         "handles single version",
			tool:         "go",
			output:       "1.21.0",
			wantVersions: 1,
			wantFirst:    "1.21.0",
			wantLast:     "1.21.0",
		},
		{
			name:         "handles empty output",
			tool:         "unknown",
			output:       "",
			wantVersions: 0,
		},
		{
			name:         "skips blank lines",
			tool:         "python",
			output:       "3.11.0\n\n3.12.0\n",
			wantVersions: 2,
			wantFirst:    "3.12.0",
			wantLast:     "3.11.0",
		},
		{
			name:    "handles runner error",
			tool:    "node",
			runErr:  errors.New("command failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &CommandRunnerMock{
				RunFunc: func(_ context.Context, _ ...string) ([]byte, error) {
					return []byte(tt.output), tt.runErr
				},
			}
			cmd := loader.LoadToolVersions(context.Background(), runner, tt.tool)
			msg := cmd()

			loaded, ok := msg.(loader.VersionsLoadedMsg)
			if !ok {
				t.Fatalf("expected loader.VersionsLoadedMsg, got %T", msg)
			}

			if loaded.Tool != tt.tool {
				t.Errorf("tool = %q, want %q", loaded.Tool, tt.tool)
			}

			assertVersionsLoaded(t, loaded, tt.wantErr, tt.wantVersions, tt.wantFirst, tt.wantLast)
		})
	}
}

func assertVersionsLoaded(
	t *testing.T,
	loaded loader.VersionsLoadedMsg,
	wantErr bool,
	wantCount int,
	wantFirst, wantLast string,
) {
	t.Helper()

	if wantErr {
		if loaded.Err == nil {
			t.Error("expected error, got nil")
		}
		return
	}

	if loaded.Err != nil {
		t.Errorf("unexpected error: %v", loaded.Err)
		return
	}

	if len(loaded.Versions) != wantCount {
		t.Errorf("got %d versions, want %d", len(loaded.Versions), wantCount)
	}

	if wantCount == 0 {
		return
	}

	if loaded.Versions[0] != wantFirst {
		t.Errorf("first version = %q, want %q", loaded.Versions[0], wantFirst)
	}
	if loaded.Versions[len(loaded.Versions)-1] != wantLast {
		t.Errorf("last version = %q, want %q", loaded.Versions[len(loaded.Versions)-1], wantLast)
	}
}

func TestLoadMiseTools(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		runErr    error
		wantErr   bool
		wantTools int
		checkTool *loader.Tool
	}{
		{
			name: "parses active tools with source",
			output: `{
				"node": [{"version": "20.0.0", "requested_version": "20", "source": {"type": "mise.toml", "path": "/home/user/.mise.toml"}, "active": true}],
				"python": [{"version": "3.12.0", "requested_version": "3.12", "source": {"type": "mise.toml", "path": "/home/user/.mise.toml"}, "active": true}]
			}`,
			wantTools: 2,
		},
		{
			name: "filters out inactive tools",
			output: `{
				"node": [
					{"version": "18.0.0", "requested_version": "18", "source": null, "active": false},
					{"version": "20.0.0", "requested_version": "20", "source": {"type": "mise.toml", "path": "/p"}, "active": true}
				]
			}`,
			wantTools: 1,
			checkTool: &loader.Tool{
				Name:             "node",
				Version:          "20.0.0",
				RequestedVersion: "20",
				SourcePath:       "/p",
				Active:           true,
			},
		},
		{
			name: "handles nil source",
			output: `{
				"go": [{"version": "1.21.0", "requested_version": "1.21", "source": null, "active": true}]
			}`,
			wantTools: 1,
			checkTool: &loader.Tool{
				Name:             "go",
				Version:          "1.21.0",
				RequestedVersion: "1.21",
				SourcePath:       "",
				Active:           true,
			},
		},
		{
			name:      "handles empty tools",
			output:    `{}`,
			wantTools: 0,
		},
		{
			name:    "handles runner error",
			runErr:  errors.New("command failed"),
			wantErr: true,
		},
		{
			name:    "handles invalid JSON",
			output:  `{invalid json}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &CommandRunnerMock{
				RunFunc: func(_ context.Context, _ ...string) ([]byte, error) {
					return []byte(tt.output), tt.runErr
				},
			}
			cmd := loader.LoadMiseTools(context.Background(), runner)
			msg := cmd()

			loaded, ok := msg.(loader.ToolsLoadedMsg)
			if !ok {
				t.Fatalf("expected loader.ToolsLoadedMsg, got %T", msg)
			}

			assertToolsLoaded(t, loaded, tt.wantErr, tt.wantTools, tt.checkTool)
		})
	}
}

func assertToolsLoaded(
	t *testing.T,
	loaded loader.ToolsLoadedMsg,
	wantErr bool,
	wantCount int,
	checkTool *loader.Tool,
) {
	t.Helper()

	if wantErr {
		if loaded.Err == nil {
			t.Error("expected error, got nil")
		}
		return
	}

	if loaded.Err != nil {
		t.Errorf("unexpected error: %v", loaded.Err)
		return
	}

	if len(loaded.Tools) != wantCount {
		t.Errorf("got %d tools, want %d", len(loaded.Tools), wantCount)
	}

	assertToolMatch(t, loaded.Tools, checkTool)
}

func assertToolMatch(t *testing.T, tools []loader.Tool, want *loader.Tool) {
	t.Helper()

	if want == nil {
		return
	}

	for _, tool := range tools {
		if tool.Name != want.Name {
			continue
		}
		if tool.Version != want.Version {
			t.Errorf("version = %q, want %q", tool.Version, want.Version)
		}
		if tool.RequestedVersion != want.RequestedVersion {
			t.Errorf("requested_version = %q, want %q", tool.RequestedVersion, want.RequestedVersion)
		}
		if tool.SourcePath != want.SourcePath {
			t.Errorf("source = %q, want %q", tool.SourcePath, want.SourcePath)
		}
		return
	}
	t.Errorf("tool %q not found in results", want.Name)
}

func TestLoadMiseVersion(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		runErr      error
		wantErr     bool
		wantVersion string
	}{
		{
			name:        "parses version with platform info",
			output:      "2024.12.0 macos-arm64 (2024-12-01)",
			wantVersion: "2024.12.0",
		},
		{
			name:        "parses simple version",
			output:      "2024.12.0",
			wantVersion: "2024.12.0",
		},
		{
			name:        "handles version with extra whitespace",
			output:      "  2024.12.0  linux-x64  ",
			wantVersion: "2024.12.0",
		},
		{
			name:    "handles runner error",
			runErr:  errors.New("command failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &CommandRunnerMock{
				RunFunc: func(_ context.Context, _ ...string) ([]byte, error) {
					return []byte(tt.output), tt.runErr
				},
			}
			cmd := loader.LoadMiseVersion(context.Background(), runner)
			msg := cmd()

			loaded, ok := msg.(loader.MiseVersionMsg)
			if !ok {
				t.Fatalf("expected loader.MiseVersionMsg, got %T", msg)
			}

			if tt.wantErr {
				if loaded.Err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if loaded.Err != nil {
				t.Errorf("unexpected error: %v", loaded.Err)
				return
			}

			if loaded.Version != tt.wantVersion {
				t.Errorf("version = %q, want %q", loaded.Version, tt.wantVersion)
			}
		})
	}
}

func TestLoadMiseTasks(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		runErr    error
		wantErr   bool
		wantTasks int
	}{
		{
			name: "parses tasks",
			output: `[
				{"name": "build", "aliases": [], "description": "Build the project", "source": "mise.toml", "hide": false, "run": ["go build"]},
				{"name": "test", "aliases": ["t"], "description": "Run tests", "source": "mise.toml", "hide": false, "run": ["go test ./..."]}
			]`,
			wantTasks: 2,
		},
		{
			name:      "handles empty tasks",
			output:    `[]`,
			wantTasks: 0,
		},
		{
			name:    "handles runner error",
			runErr:  errors.New("command failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &CommandRunnerMock{
				RunFunc: func(_ context.Context, _ ...string) ([]byte, error) {
					return []byte(tt.output), tt.runErr
				},
			}
			cmd := loader.LoadMiseTasks(context.Background(), runner)
			msg := cmd()

			loaded, ok := msg.(loader.TasksLoadedMsg)
			if !ok {
				t.Fatalf("expected loader.TasksLoadedMsg, got %T", msg)
			}

			if tt.wantErr {
				if loaded.Err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if loaded.Err != nil {
				t.Errorf("unexpected error: %v", loaded.Err)
				return
			}

			if len(loaded.Tasks) != tt.wantTasks {
				t.Errorf("got %d tasks, want %d", len(loaded.Tasks), tt.wantTasks)
			}
		})
	}
}

func TestLoadMiseEnvVars(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		runErr      error
		wantErr     bool
		wantEnvVars int
		checkMasked bool // all env vars should start masked
	}{
		{
			name:        "parses env vars",
			output:      `{"PATH": "/usr/bin", "HOME": "/home/user"}`,
			wantEnvVars: 2,
			checkMasked: true,
		},
		{
			name:        "handles empty env vars",
			output:      `{}`,
			wantEnvVars: 0,
		},
		{
			name:    "handles runner error",
			runErr:  errors.New("command failed"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &CommandRunnerMock{
				RunFunc: func(_ context.Context, _ ...string) ([]byte, error) {
					return []byte(tt.output), tt.runErr
				},
			}
			cmd := loader.LoadMiseEnvVars(context.Background(), runner)
			msg := cmd()

			loaded, ok := msg.(loader.EnvVarsLoadedMsg)
			if !ok {
				t.Fatalf("expected loader.EnvVarsLoadedMsg, got %T", msg)
			}

			assertEnvVarsLoaded(t, loaded, tt.wantErr, tt.wantEnvVars, tt.checkMasked)
		})
	}
}

func assertEnvVarsLoaded(
	t *testing.T,
	loaded loader.EnvVarsLoadedMsg,
	wantErr bool,
	wantCount int,
	checkMasked bool,
) {
	t.Helper()

	if wantErr {
		if loaded.Err == nil {
			t.Error("expected error, got nil")
		}
		return
	}

	if loaded.Err != nil {
		t.Errorf("unexpected error: %v", loaded.Err)
		return
	}

	if len(loaded.EnvVars) != wantCount {
		t.Errorf("got %d env vars, want %d", len(loaded.EnvVars), wantCount)
	}

	if checkMasked {
		assertAllEnvVarsMasked(t, loaded.EnvVars)
	}
}

func assertAllEnvVarsMasked(t *testing.T, envVars []loader.EnvVar) {
	t.Helper()

	for _, ev := range envVars {
		if !ev.Masked {
			t.Errorf("env var %q should be masked by default", ev.Name)
		}
	}
}

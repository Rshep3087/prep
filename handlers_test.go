package main

import (
	"path/filepath"
	"testing"

	"charm.land/bubbles/v2/table"

	"github.com/rshep3087/prep/internal/loader"
)

func TestSourcePriority(t *testing.T) {
	// Create a test model with known cwd and homeDir
	m := model{
		cwd:     "/home/user/work/myproject",
		homeDir: "/home/user",
	}

	tests := []struct {
		name       string
		sourcePath string
		want       int
	}{
		{
			name:       "mise.toml in current directory has highest priority",
			sourcePath: "/home/user/work/myproject/mise.toml",
			want:       0,
		},
		{
			name:       "config in subdirectory has lower priority",
			sourcePath: "/home/user/work/myproject/backend/mise.toml",
			want:       1,
		},
		{
			name:       "config in nested subdirectory",
			sourcePath: "/home/user/work/myproject/backend/api/mise.toml",
			want:       2,
		},
		{
			name:       "config in parent directory",
			sourcePath: "/home/user/work/mise.toml",
			want:       priorityParentDirBase + 1,
		},
		{
			name:       "config two levels up",
			sourcePath: "/home/user/mise.toml",
			want:       priorityParentDirBase + 2,
		},
		{
			name:       "home directory config",
			sourcePath: "/home/user/.config/mise/config.toml",
			want:       priorityHomeDir,
		},
		{
			name:       "home directory tool-versions (parent of project, 2 levels up)",
			sourcePath: "/home/user/.tool-versions",
			want:       priorityParentDirBase + 2, // This is in a parent dir of the project
		},
		{
			name:       "system config",
			sourcePath: "/etc/mise/config.toml",
			want:       prioritySystemDir,
		},
		{
			name:       "system conf.d fragment",
			sourcePath: "/etc/mise/conf.d/defaults.toml",
			want:       prioritySystemDir,
		},
		{
			name:       "unrelated directory",
			sourcePath: "/opt/other/mise.toml",
			want:       priorityUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.sourcePriority(tt.sourcePath)
			if got != tt.want {
				t.Errorf("sourcePriority(%q) = %d, want %d", tt.sourcePath, got, tt.want)
			}
		})
	}
}

func TestSourcePrioritySorting(t *testing.T) {
	// Test that configs are sorted in the correct order
	m := model{
		cwd:     "/home/user/work/myproject",
		homeDir: "/home/user",
	}

	expectedOrder := []string{
		"/home/user/work/myproject/mise.toml",         // 0 - cwd
		"/home/user/work/myproject/backend/mise.toml", // 1 - subdir
		"/home/user/work/mise.toml",                   // 1001 - parent
		"/home/user/.config/mise/config.toml",         // 10000 - home
		"/etc/mise/config.toml",                       // 100000 - system
		"/opt/random/mise.toml",                       // 999999 - unknown
	}

	// Get priorities and verify ordering
	for i := range len(expectedOrder) - 1 {
		current := m.sourcePriority(expectedOrder[i])
		next := m.sourcePriority(expectedOrder[i+1])
		if current >= next {
			t.Errorf(
				"incorrect ordering: %q (priority=%d) should have lower priority than %q (priority=%d)",
				expectedOrder[i], current, expectedOrder[i+1], next,
			)
		}
	}
}

func TestSourcePriorityWithAbsolutePaths(t *testing.T) {
	// Test with absolute paths
	cwd, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("failed to get absolute cwd: %v", err)
	}

	m := model{
		cwd:     cwd,
		homeDir: "/home/testuser",
	}

	// Test that a mise.toml in the actual cwd gets priority 0
	cwdConfig := filepath.Join(cwd, "mise.toml")
	priority := m.sourcePriority(cwdConfig)
	if priority != 0 {
		t.Errorf(
			"sourcePriority(%q) = %d, want 0 (current directory should have highest priority)",
			cwdConfig,
			priority,
		)
	}
}

func TestMaskValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string returns empty",
			input: "",
			want:  "",
		},
		{
			name:  "short value gets masked",
			input: "abc",
			want:  "●●●●●●●●",
		},
		{
			name:  "long value gets same mask",
			input: "this-is-a-very-long-secret-value-12345",
			want:  "●●●●●●●●",
		},
		{
			name:  "single character gets masked",
			input: "x",
			want:  "●●●●●●●●",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskValue(tt.input)
			if got != tt.want {
				t.Errorf("maskValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// createTestModel creates a minimal model for testing handlers.
func createTestModel(envVars []loader.EnvVar) model {
	// Create table with env vars
	rows := make([]table.Row, len(envVars))
	for i, ev := range envVars {
		displayValue := maskValue(ev.Value)
		if !ev.Masked {
			displayValue = ev.Value
		}
		rows[i] = table.Row{ev.Name, displayValue}
	}

	envVarsTable := newTable(getEnvVarsTableConfig(), rows, true)

	return model{
		envVars:      envVars,
		envVarsTable: envVarsTable,
	}
}

func TestShowSelectedEnvVar(t *testing.T) {
	tests := []struct {
		name          string
		envVars       []loader.EnvVar
		selectedIndex int
		wantMasked    []bool
	}{
		{
			name: "unmasks selected env var",
			envVars: []loader.EnvVar{
				{Name: "SECRET", Value: "secret123", Masked: true},
				{Name: "API_KEY", Value: "key456", Masked: true},
			},
			selectedIndex: 0,
			wantMasked:    []bool{false, true}, // first should be unmasked
		},
		{
			name: "unmasks second env var",
			envVars: []loader.EnvVar{
				{Name: "SECRET", Value: "secret123", Masked: true},
				{Name: "API_KEY", Value: "key456", Masked: true},
			},
			selectedIndex: 1,
			wantMasked:    []bool{true, false}, // second should be unmasked
		},
		{
			name:          "handles empty env vars",
			envVars:       []loader.EnvVar{},
			selectedIndex: 0,
			wantMasked:    []bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := createTestModel(tt.envVars)

			// Select the row
			if len(tt.envVars) > 0 && tt.selectedIndex < len(tt.envVars) {
				for range tt.selectedIndex {
					m.envVarsTable.MoveDown(1)
				}
			}

			m = showSelectedEnvVar(m)

			for i, want := range tt.wantMasked {
				if m.envVars[i].Masked != want {
					t.Errorf("envVars[%d].Masked = %v, want %v", i, m.envVars[i].Masked, want)
				}
			}
		})
	}
}

func TestShowAllEnvVars(t *testing.T) {
	envVars := []loader.EnvVar{
		{Name: "SECRET", Value: "secret123", Masked: true},
		{Name: "API_KEY", Value: "key456", Masked: true},
		{Name: "TOKEN", Value: "token789", Masked: true},
	}

	m := createTestModel(envVars)
	m = showAllEnvVars(m)

	for i, ev := range m.envVars {
		if ev.Masked {
			t.Errorf("envVars[%d] should be unmasked after showAllEnvVars", i)
		}
	}
}

func TestHideAllEnvVars(t *testing.T) {
	envVars := []loader.EnvVar{
		{Name: "SECRET", Value: "secret123", Masked: false},
		{Name: "API_KEY", Value: "key456", Masked: false},
		{Name: "TOKEN", Value: "token789", Masked: false},
	}

	m := createTestModel(envVars)
	m = hideAllEnvVars(m)

	for i, ev := range m.envVars {
		if !ev.Masked {
			t.Errorf("envVars[%d] should be masked after hideAllEnvVars", i)
		}
	}
}

func TestRefreshEnvVarsTable(t *testing.T) {
	envVars := []loader.EnvVar{
		{Name: "SECRET", Value: "secret123", Masked: true},
		{Name: "VISIBLE", Value: "visible456", Masked: false},
	}

	m := createTestModel(envVars)
	m = refreshEnvVarsTable(m)

	rows := m.envVarsTable.Rows()
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	// First row should be masked
	if rows[0][1] != "●●●●●●●●" {
		t.Errorf("masked value = %q, want %q", rows[0][1], "●●●●●●●●")
	}

	// Second row should show actual value
	if rows[1][1] != "visible456" {
		t.Errorf("visible value = %q, want %q", rows[1][1], "visible456")
	}
}

func TestEnvVarVisibilityToggleCycle(t *testing.T) {
	// Test a realistic cycle: start masked -> show all -> hide all
	envVars := []loader.EnvVar{
		{Name: "SECRET", Value: "secret123", Masked: true},
		{Name: "API_KEY", Value: "key456", Masked: true},
	}

	m := createTestModel(envVars)

	// Verify initial state is masked
	for i, ev := range m.envVars {
		if !ev.Masked {
			t.Errorf("initial: envVars[%d] should be masked", i)
		}
	}

	// Show all
	m = showAllEnvVars(m)
	for i, ev := range m.envVars {
		if ev.Masked {
			t.Errorf("after showAll: envVars[%d] should be unmasked", i)
		}
	}

	// Hide all
	m = hideAllEnvVars(m)
	for i, ev := range m.envVars {
		if !ev.Masked {
			t.Errorf("after hideAll: envVars[%d] should be masked", i)
		}
	}
}

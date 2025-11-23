package watcher_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/rshep3087/prep/internal/watcher"
)

// mockSender captures messages sent by the watcher.
type mockSender struct {
	mu       sync.Mutex
	messages []tea.Msg
}

func (m *mockSender) Send(msg tea.Msg) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
}

func (m *mockSender) Messages() []tea.Msg {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]tea.Msg{}, m.messages...)
}

func TestStartFileWatcher(t *testing.T) {
	// Create a temp directory with a config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "mise.toml")

	// Create the config file
	if err := os.WriteFile(configPath, []byte("initial"), 0o644); err != nil {
		t.Fatalf("failed to create config file: %v", err)
	}

	sender := &mockSender{}
	w, err := watcher.StartFileWatcher([]string{configPath}, sender)
	if err != nil {
		t.Fatalf("StartFileWatcher failed: %v", err)
	}
	defer watcher.Close(w)

	// Give the watcher time to start
	time.Sleep(50 * time.Millisecond)

	// Write to the config file
	if writeErr := os.WriteFile(configPath, []byte("modified"), 0o644); writeErr != nil {
		t.Fatalf("failed to write config file: %v", writeErr)
	}

	// Wait for the event to be processed
	time.Sleep(100 * time.Millisecond)

	messages := sender.Messages()
	if len(messages) == 0 {
		t.Error("expected at least one FileChangedMsg, got none")
		return
	}

	// Check that we got a FileChangedMsg for our file
	found := false
	for _, msg := range messages {
		if changed, ok := msg.(watcher.FileChangedMsg); ok {
			if changed.Path == configPath {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("expected FileChangedMsg for %s, got %v", configPath, messages)
	}
}

func TestStartFileWatcher_IgnoresUnwatchedFiles(t *testing.T) {
	tmpDir := t.TempDir()
	watchedPath := filepath.Join(tmpDir, "mise.toml")
	unwatchedPath := filepath.Join(tmpDir, "other.txt")

	// Create both files
	if err := os.WriteFile(watchedPath, []byte("watched"), 0o644); err != nil {
		t.Fatalf("failed to create watched file: %v", err)
	}
	if err := os.WriteFile(unwatchedPath, []byte("unwatched"), 0o644); err != nil {
		t.Fatalf("failed to create unwatched file: %v", err)
	}

	sender := &mockSender{}
	w, err := watcher.StartFileWatcher([]string{watchedPath}, sender)
	if err != nil {
		t.Fatalf("StartFileWatcher failed: %v", err)
	}
	defer watcher.Close(w)

	time.Sleep(50 * time.Millisecond)

	// Modify the unwatched file
	if writeErr := os.WriteFile(unwatchedPath, []byte("modified unwatched"), 0o644); writeErr != nil {
		t.Fatalf("failed to write unwatched file: %v", writeErr)
	}

	time.Sleep(100 * time.Millisecond)

	// Should not have received any messages for the unwatched file
	messages := sender.Messages()
	for _, msg := range messages {
		if changed, ok := msg.(watcher.FileChangedMsg); ok {
			if changed.Path == unwatchedPath {
				t.Errorf("should not receive FileChangedMsg for unwatched file %s", unwatchedPath)
			}
		}
	}
}

func TestStartFileWatcher_MultipleFilesInSameDir(t *testing.T) {
	tmpDir := t.TempDir()
	config1 := filepath.Join(tmpDir, "mise.toml")
	config2 := filepath.Join(tmpDir, ".mise.local.toml")

	// Create both config files
	if err := os.WriteFile(config1, []byte("config1"), 0o644); err != nil {
		t.Fatalf("failed to create config1: %v", err)
	}
	if err := os.WriteFile(config2, []byte("config2"), 0o644); err != nil {
		t.Fatalf("failed to create config2: %v", err)
	}

	sender := &mockSender{}
	// Watch both files - they share the same parent directory
	w, err := watcher.StartFileWatcher([]string{config1, config2}, sender)
	if err != nil {
		t.Fatalf("StartFileWatcher failed: %v", err)
	}
	defer watcher.Close(w)

	time.Sleep(50 * time.Millisecond)

	// Modify both files
	if writeErr := os.WriteFile(config1, []byte("modified1"), 0o644); writeErr != nil {
		t.Fatalf("failed to write config1: %v", writeErr)
	}
	time.Sleep(50 * time.Millisecond)
	if writeErr := os.WriteFile(config2, []byte("modified2"), 0o644); writeErr != nil {
		t.Fatalf("failed to write config2: %v", writeErr)
	}

	time.Sleep(100 * time.Millisecond)

	messages := sender.Messages()

	// Check that we received messages for both files
	gotConfig1 := false
	gotConfig2 := false
	for _, msg := range messages {
		if changed, ok := msg.(watcher.FileChangedMsg); ok {
			if changed.Path == config1 {
				gotConfig1 = true
			}
			if changed.Path == config2 {
				gotConfig2 = true
			}
		}
	}

	if !gotConfig1 {
		t.Error("expected FileChangedMsg for config1")
	}
	if !gotConfig2 {
		t.Error("expected FileChangedMsg for config2")
	}
}

func TestStartFileWatcher_NonExistentDirectory(t *testing.T) {
	nonExistentPath := "/nonexistent/directory/mise.toml"

	sender := &mockSender{}
	_, err := watcher.StartFileWatcher([]string{nonExistentPath}, sender)

	// Should fail because the parent directory doesn't exist
	if err == nil {
		t.Error("expected error for non-existent directory, got nil")
	}
}

func TestClose_NilWatcher(_ *testing.T) {
	// Should not panic when closing a nil watcher
	watcher.Close(nil)
}

func TestStartFileWatcher_EmptyPaths(t *testing.T) {
	sender := &mockSender{}
	w, err := watcher.StartFileWatcher([]string{}, sender)
	if err != nil {
		t.Fatalf("StartFileWatcher with empty paths failed: %v", err)
	}
	defer watcher.Close(w)

	// Should work fine with no paths to watch
}

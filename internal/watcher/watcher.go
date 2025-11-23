// Package watcher provides file watching functionality for config files.
package watcher

import (
	"fmt"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"
)

// MessageSender abstracts the ability to send messages.
type MessageSender interface {
	Send(msg tea.Msg)
}

// FileChangedMsg is sent when a watched config file changes.
type FileChangedMsg struct {
	Path string
}

// StartFileWatcher creates an fsnotify watcher and monitors config files.
// It watches parent directories (more reliable for editor saves) and filters
// events to only the specified config files.
func StartFileWatcher(paths []string, sender MessageSender) (*fsnotify.Watcher, error) {
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
				sender.Send(FileChangedMsg{Path: event.Name})
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

// Close safely closes a file watcher if it exists.
func Close(w *fsnotify.Watcher) {
	if w != nil {
		_ = w.Close()
	}
}

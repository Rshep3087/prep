package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"

	tea "charm.land/bubbletea/v2"
)

func run(_ context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	debug := fs.Bool("debug", false, "enable debug logging to debug.log")
	editorFlag := fs.String("editor", "", "editor command for editing source files (overrides $EDITOR)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	// Determine editor: flag takes precedence over env var, fallback to "vi"
	editor := *editorFlag
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	// Setup logger
	var logger *slog.Logger
	if *debug {
		lf, err := tea.LogToFile("debug.log", "debug")
		if err != nil {
			return fmt.Errorf("setup logging to file: %w", err)
		}
		defer func() { _ = lf.Close() }()
		log.Println("debug logging enabled")
		logger = slog.New(slog.NewTextHandler(lf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	} else {
		log.SetOutput(io.Discard)
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	// Get current working directory and home directory for source priority sorting
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return fmt.Errorf("get current working directory: %w", cwdErr)
	}
	homeDir, homeDirErr := os.UserHomeDir()
	if homeDirErr != nil {
		return fmt.Errorf("get user home directory: %w", homeDirErr)
	}

	m := &model{
		tasksTable:     newTable(getTasksTableConfig(), nil, true),
		toolsTable:     newTable(getToolsTableConfig(), nil, false),
		envVarsTable:   newTable(getEnvVarsTableConfig(), nil, false),
		tasksLoading:   true,
		toolsLoading:   true,
		envVarsLoading: true,
		runner:         execRunner{},
		styles:         newStyles(),
		logger:         logger,
		editor:         editor,
		cwd:            cwd,
		homeDir:        homeDir,
	}
	program := tea.NewProgram(m, tea.WithInput(stdin), tea.WithOutput(stdout))
	m.sender = program // *tea.Program implements messageSender
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

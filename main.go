package main

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
)

type model struct {
	tasks    []string
	cursor   int
	selected map[int]struct{}
}

func (m model) Init() tea.Cmd {
	return nil
}

// Update is called when a message is received. Use it to inspect messages
// and, in response, update the model and/or send a command.
func (m model) Update(_ tea.Msg) (tea.Model, tea.Cmd) {
	panic("not implemented") // TODO: Implement
}

// View renders the program's UI, which can be a string or a [Layer]. The
// view is rendered after every Update.
func (m model) View() tea.View {
	panic("not implemented") // TODO: Implement
}

func run(_ context.Context) error {
	m := &model{
		tasks:    []string{},
		cursor:   0,
		selected: map[int]struct{}{},
	}
	program := tea.NewProgram(m)
	_, err := program.Run()
	return err
}

func main() {
	ctx := context.Background()
	err := run(ctx)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

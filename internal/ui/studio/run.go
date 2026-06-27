package studio

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
)

func Run() error {
	model := NewModel()
	p := tea.NewProgram(model)
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running monitor studio: %v\n", err)
		return err
	}
	if fm, ok := final.(Model); ok {
		if fm.cancel != nil {
			fm.cancel()
		}
		if fm.unsubscribe != nil {
			fm.unsubscribe()
		}
	}
	return nil
}

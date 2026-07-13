package studio

import (
	"fmt"
	"os"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// ProgramReloader safely bridges the reload HTTP handler to Bubble Tea's
// external message API. It is inactive before RunWithOptions starts and after
// the program exits.
type ProgramReloader struct {
	mu      sync.RWMutex
	program *tea.Program
}

// NewProgramReloader creates a bridge suitable for internal/reload.Server.
func NewProgramReloader() *ProgramReloader { return &ProgramReloader{} }

// Reload injects a refresh message into the active Studio program.
func (r *ProgramReloader) Reload() error {
	r.mu.RLock()
	p := r.program
	r.mu.RUnlock()
	if p == nil {
		return fmt.Errorf("studio is not active")
	}
	p.Send(externalReloadMsg{})
	return nil
}

func (r *ProgramReloader) attach(p *tea.Program) {
	r.mu.Lock()
	r.program = p
	r.mu.Unlock()
}

func (r *ProgramReloader) detach(p *tea.Program) {
	r.mu.Lock()
	if r.program == p {
		r.program = nil
	}
	r.mu.Unlock()
}

func Run() error {
	return RunWithOptions(Options{})
}

// RunWithOptions launches Studio with CLI-provided integration policy.
func RunWithOptions(opts Options) error {
	model := NewModelWithOptions(opts)
	p := tea.NewProgram(model)
	if opts.Reloader != nil {
		opts.Reloader.attach(p)
		defer opts.Reloader.detach(p)
	}
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running monitor studio: %v\n", err)
		return err
	}
	if fm, ok := final.(Model); ok {
		if fm.cancel != nil {
			fm.cancel()
		}
	}
	return nil
}

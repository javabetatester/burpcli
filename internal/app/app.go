package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"burpui/internal/proxy"
	"burpui/internal/tui"
)

type Config struct {
	ListenAddr   string
	MaxBodyBytes int
}

func Run(cfg Config) error {
	flowCh := make(chan *proxy.FlowSnapshot, 1024)
	ctrl := proxy.NewController()
	px := proxy.New(proxy.Config{ListenAddr: cfg.ListenAddr, MaxBodyBytes: cfg.MaxBodyBytes}, ctrl, flowCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- px.Serve(ctx)
	}()

	model := tui.New(tui.Config{
		ListenAddr: cfg.ListenAddr,
		FlowCh:     flowCh,
		SetIntercept: func(on bool) {
			ctrl.SetIntercept(on)
		},
		ListBreakpoints: func() []proxy.BreakpointRule {
			return ctrl.ListBreakpoints()
		},
		AddBreakpoint: func(match string) {
			ctrl.AddBreakpoint(match)
		},
		ToggleBreakpoint: func(id int64) {
			ctrl.ToggleBreakpoint(id)
		},
		RemoveBreakpoint: func(id int64) {
			ctrl.RemoveBreakpoint(id)
		},
	})

	p := tea.NewProgram(model, tea.WithAltScreen())

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
		time.AfterFunc(500*time.Millisecond, func() {
			p.Quit()
		})
	}()

	if _, err := p.Run(); err != nil {
		cancel()
		return fmt.Errorf("tui: %w", err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			return nil
		}
		return err
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

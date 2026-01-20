package tui

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Quit            key.Binding
	ToggleIntercept key.Binding
	Forward         key.Binding
	Drop            key.Binding
	Repeater        key.Binding
	Export          key.Binding
	Back            key.Binding
	Send            key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Quit:            key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "sair")),
		ToggleIntercept: key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "intercept")),
		Forward:         key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "forward")),
		Drop:            key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "drop")),
		Repeater:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "repeater")),
		Export:          key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "export")),
		Back:            key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "voltar")),
		Send:            key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "enviar")),
	}
}

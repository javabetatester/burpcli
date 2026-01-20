package tui

import "github.com/charmbracelet/lipgloss"

type styles struct {
	app       lipgloss.Style
	title     lipgloss.Style
	status    lipgloss.Style
	statusDim lipgloss.Style
	border    lipgloss.Style
	badgeOn   lipgloss.Style
	badgeOff  lipgloss.Style
	badgeWarn lipgloss.Style
	key       lipgloss.Style
	err       lipgloss.Style
	dim       lipgloss.Style
}

func newStyles() styles {
	base := lipgloss.NewStyle()
	return styles{
		app:       base.Padding(1, 2),
		title:     base.Bold(true).Foreground(lipgloss.Color("230")),
		status:    base.Foreground(lipgloss.Color("253")).Background(lipgloss.Color("236")).Padding(0, 1),
		statusDim: base.Foreground(lipgloss.Color("244")).Background(lipgloss.Color("236")).Padding(0, 1),
		border:    base.Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1),
		badgeOn:   base.Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("34")).Padding(0, 1),
		badgeOff:  base.Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("238")).Padding(0, 1),
		badgeWarn: base.Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("160")).Padding(0, 1),
		key:       base.Bold(true).Foreground(lipgloss.Color("81")),
		err:       base.Foreground(lipgloss.Color("203")),
		dim:       base.Foreground(lipgloss.Color("244")),
	}
}

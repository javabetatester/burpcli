package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

var (
	headerName = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	jsonKey    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	jsonString = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	jsonNumber = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	jsonLit    = lipgloss.NewStyle().Foreground(lipgloss.Color("177"))
	dim        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	addStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

func highlightHeaders(h http.Header) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range h[k] {
			b.WriteString(headerName.Render(k) + ": " + v + "\n")
		}
	}
	return b.String()
}

func highlightBody(h http.Header, body []byte) string {
	if len(body) == 0 {
		return dim.Render("(empty body)")
	}
	if !utf8.Valid(body) {
		return dim.Render(fmt.Sprintf("(binary body, %d bytes)", len(body)))
	}
	if strings.Contains(h.Get("Content-Type"), "json") || json.Valid(body) {
		var out bytes.Buffer
		if json.Indent(&out, body, "", "  ") == nil {
			return colorJSON(out.String())
		}
	}
	return string(body)
}

func colorJSON(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			style := jsonString
			if strings.HasPrefix(strings.TrimLeft(s[j:], " "), ":") {
				style = jsonKey
			}
			b.WriteString(style.Render(s[i:j]))
			i = j
		case c == '-' || (c >= '0' && c <= '9'):
			j := i + 1
			for j < len(s) && strings.IndexByte("0123456789.eE+-", s[j]) >= 0 {
				j++
			}
			b.WriteString(jsonNumber.Render(s[i:j]))
			i = j
		case strings.HasPrefix(s[i:], "true"), strings.HasPrefix(s[i:], "false"), strings.HasPrefix(s[i:], "null"):
			j := i + strings.IndexAny(s[i:]+",", ",}] \n")
			b.WriteString(jsonLit.Render(s[i:j]))
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func colorDiff(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			lines[i] = addStyle.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = delStyle.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

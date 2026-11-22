package history

import "path"

// Scope selects hosts by glob. Empty Include means every host; Exclude always wins.
type Scope struct {
	Include []string
	Exclude []string
}

// Match reports whether host (without port) is in scope.
func (s Scope) Match(host string) bool {
	for _, g := range s.Exclude {
		if ok, _ := path.Match(g, host); ok {
			return false
		}
	}
	if len(s.Include) == 0 {
		return true
	}
	for _, g := range s.Include {
		if ok, _ := path.Match(g, host); ok {
			return true
		}
	}
	return false
}

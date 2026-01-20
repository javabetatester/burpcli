package proxy

import (
	"strings"
	"sync"
	"sync/atomic"
)

type Controller struct {
	intercept atomic.Bool

	mu          sync.RWMutex
	nextRuleID  atomic.Int64
	breakpoints []BreakpointRule
}

type BreakpointRule struct {
	ID      int64
	Enabled bool
	Match   string
}

func NewController() *Controller {
	return &Controller{}
}

func (c *Controller) InterceptEnabled() bool {
	return c.intercept.Load()
}

func (c *Controller) SetIntercept(on bool) {
	c.intercept.Store(on)
}

func (c *Controller) AddBreakpoint(match string) BreakpointRule {
	r := BreakpointRule{ID: c.nextRuleID.Add(1), Enabled: true, Match: strings.TrimSpace(match)}
	c.mu.Lock()
	c.breakpoints = append(c.breakpoints, r)
	c.mu.Unlock()
	return r
}

func (c *Controller) ListBreakpoints() []BreakpointRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]BreakpointRule, len(c.breakpoints))
	copy(out, c.breakpoints)
	return out
}

func (c *Controller) ToggleBreakpoint(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.breakpoints {
		if c.breakpoints[i].ID == id {
			c.breakpoints[i].Enabled = !c.breakpoints[i].Enabled
			return
		}
	}
}

func (c *Controller) RemoveBreakpoint(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.breakpoints {
		if c.breakpoints[i].ID == id {
			c.breakpoints = append(c.breakpoints[:i], c.breakpoints[i+1:]...)
			return
		}
	}
}

func (c *Controller) ShouldBreak(method, urlStr, host string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, r := range c.breakpoints {
		if !r.Enabled {
			continue
		}
		m := strings.ToLower(r.Match)
		if m == "" {
			continue
		}
		if strings.Contains(strings.ToLower(urlStr), m) || strings.Contains(strings.ToLower(host), m) || strings.Contains(strings.ToLower(method), m) {
			return true
		}
	}
	return false
}

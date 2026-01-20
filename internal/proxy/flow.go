package proxy

import (
	"net/http"
	"sync/atomic"
	"time"
)

type Decision int

const (
	DecisionForward Decision = iota
	DecisionDrop
)

type Flow struct {
	ID             int64
	StartedAt      time.Time
	Duration       time.Duration
	Method         string
	URL            string
	Host           string
	RequestHeader  http.Header
	RequestBody    []byte
	ReqTruncated   bool
	StatusCode     int
	ResponseHeader http.Header
	ResponseBody   []byte
	RespTruncated  bool
	Error          string
	Intercepted    bool
	Pending        bool
	decisionCh     chan Decision
}

type FlowSnapshot struct {
	Flow *Flow
}

var nextID atomic.Int64

func newFlow() *Flow {
	return &Flow{ID: nextID.Add(1), StartedAt: time.Now(), Pending: true, decisionCh: make(chan Decision, 1)}
}

func (f *Flow) Decide(d Decision) {
	select {
	case f.decisionCh <- d:
	default:
	}
}

func (f *Flow) waitDecision() Decision {
	return <-f.decisionCh
}

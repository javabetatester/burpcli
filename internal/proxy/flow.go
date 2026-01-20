package proxy

import (
	"net/http"
	"sync/atomic"
	"time"
)

type ActionKind int

const (
	ActionForward ActionKind = iota
	ActionDrop
	ActionForwardRaw
)

type Action struct {
	Kind       ActionKind
	RawRequest string
}

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
	RawRequest     string
	StatusCode     int
	ResponseHeader http.Header
	ResponseBody   []byte
	RespTruncated  bool
	Error          string
	Intercepted    bool
	Pending        bool
	actionCh       chan Action
}

type FlowSnapshot struct {
	Flow *Flow
}

var nextID atomic.Int64

func newFlow() *Flow {
	return &Flow{ID: nextID.Add(1), StartedAt: time.Now(), Pending: true, actionCh: make(chan Action, 1)}
}

func (f *Flow) Forward() {
	f.send(Action{Kind: ActionForward})
}

func (f *Flow) Drop() {
	f.send(Action{Kind: ActionDrop})
}

func (f *Flow) ForwardRaw(rawRequest string) {
	f.send(Action{Kind: ActionForwardRaw, RawRequest: rawRequest})
}

func (f *Flow) send(a Action) {
	select {
	case f.actionCh <- a:
	default:
	}
}

func (f *Flow) waitAction() Action {
	return <-f.actionCh
}

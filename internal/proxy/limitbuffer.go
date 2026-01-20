package proxy

import "bytes"

type LimitBuffer struct {
	Limit     int
	Truncated bool
	buf       bytes.Buffer
}

func NewLimitBuffer(limit int) *LimitBuffer {
	return &LimitBuffer{Limit: limit}
}

func (l *LimitBuffer) Write(p []byte) (int, error) {
	if l.Limit <= 0 {
		l.Truncated = true
		return len(p), nil
	}

	remaining := l.Limit - l.buf.Len()
	if remaining <= 0 {
		l.Truncated = true
		return len(p), nil
	}

	if len(p) > remaining {
		_, _ = l.buf.Write(p[:remaining])
		l.Truncated = true
		return len(p), nil
	}

	_, _ = l.buf.Write(p)
	return len(p), nil
}

func (l *LimitBuffer) Bytes() []byte {
	return l.buf.Bytes()
}

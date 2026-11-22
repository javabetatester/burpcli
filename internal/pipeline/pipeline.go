// Package pipeline chains request and response interceptors and holds requests for manual review.
package pipeline

import (
	"context"
	"errors"

	"github.com/javabetatester/burpcli/internal/flow"
)

// ErrDropped aborts the exchange; the proxy answers the client without contacting upstream.
var ErrDropped = errors.New("pipeline: request dropped")

// Interceptor may rewrite a request or response in place, or return an error to abort the exchange.
type Interceptor interface {
	Request(ctx context.Context, id uint64, req *flow.Request) error
	Response(ctx context.Context, id uint64, resp *flow.Response) error
}

// Passthrough is a no-op Interceptor to embed.
type Passthrough struct{}

// Request does nothing.
func (Passthrough) Request(context.Context, uint64, *flow.Request) error { return nil }

// Response does nothing.
func (Passthrough) Response(context.Context, uint64, *flow.Response) error { return nil }

// Chain runs interceptors in order and stops at the first error.
type Chain []Interceptor

// Request runs the request side of every interceptor.
func (c Chain) Request(ctx context.Context, id uint64, req *flow.Request) error {
	for _, i := range c {
		if err := i.Request(ctx, id, req); err != nil {
			return err
		}
	}
	return nil
}

// Response runs the response side of every interceptor.
func (c Chain) Response(ctx context.Context, id uint64, resp *flow.Response) error {
	for _, i := range c {
		if err := i.Response(ctx, id, resp); err != nil {
			return err
		}
	}
	return nil
}

var _ Interceptor = Chain(nil)

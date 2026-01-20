package proxy

import "sync/atomic"

type Controller struct {
	intercept atomic.Bool
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

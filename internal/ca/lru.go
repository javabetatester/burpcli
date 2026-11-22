package ca

import "container/list"

type lru[V any] struct {
	cap   int
	order *list.List
	items map[string]*list.Element
}

type lruEntry[V any] struct {
	key string
	val V
}

func newLRU[V any](capacity int) *lru[V] {
	return &lru[V]{cap: capacity, order: list.New(), items: map[string]*list.Element{}}
}

func (l *lru[V]) get(key string) (V, bool) {
	el, ok := l.items[key]
	if !ok {
		var zero V
		return zero, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*lruEntry[V]).val, true
}

func (l *lru[V]) put(key string, val V) {
	if el, ok := l.items[key]; ok {
		el.Value.(*lruEntry[V]).val = val
		l.order.MoveToFront(el)
		return
	}
	l.items[key] = l.order.PushFront(&lruEntry[V]{key, val})
	if l.order.Len() > l.cap {
		oldest := l.order.Back()
		l.order.Remove(oldest)
		delete(l.items, oldest.Value.(*lruEntry[V]).key)
	}
}

func (l *lru[V]) len() int { return l.order.Len() }

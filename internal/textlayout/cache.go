// Package textlayout contains the bounded ownership and pixel-bound contracts
// shared by the native DirectWrite renderer and its portable regression tests.
package textlayout

import "container/list"

type Key struct {
	Text string
	Bold bool
}

type entry[T any] struct {
	key   Key
	value T
	cost  int
}

// Cache owns every inserted value, including values too large to retain. The
// caller must not insert an object still in use by an unfinished native draw.
// Limits bound both COM object count and retained text, even during long chats.
type Cache[T any] struct {
	items                       map[Key]*list.Element
	lru                         list.List
	units, maxEntries, maxUnits int
	release                     func(T)
}

func NewCache[T any](entries, units int, release func(T)) *Cache[T] {
	return &Cache[T]{items: make(map[Key]*list.Element), maxEntries: entries, maxUnits: units, release: release}
}

func (c *Cache[T]) Get(key Key) (T, bool) {
	if e := c.items[key]; e != nil {
		c.lru.MoveToFront(e)
		return e.Value.(entry[T]).value, true
	}
	var zero T
	return zero, false
}

func (c *Cache[T]) remove(e *list.Element) {
	v := e.Value.(entry[T])
	delete(c.items, v.key)
	c.lru.Remove(e)
	c.units -= v.cost
	c.release(v.value)
}

func (c *Cache[T]) Put(key Key, value T, units int) bool {
	if e := c.items[key]; e != nil {
		c.remove(e)
	}
	if units < 0 || units > c.maxUnits || c.maxEntries <= 0 {
		c.release(value)
		return false
	}
	for len(c.items) >= c.maxEntries || c.units > c.maxUnits-units {
		c.remove(c.lru.Back())
	}
	c.items[key] = c.lru.PushFront(entry[T]{key, value, units})
	c.units += units
	return true
}

func (c *Cache[T]) Close() {
	for c.lru.Len() != 0 {
		c.remove(c.lru.Back())
	}
}

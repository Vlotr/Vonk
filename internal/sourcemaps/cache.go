package sourcemaps

import (
	"container/list"
	"fmt"
	"sync"
)

// DefaultCacheBytes is how much memory parsed source maps and minified
// files may take (ruling 15): the ones used least recently go first.
const DefaultCacheBytes = 64 << 20

// cache keeps parsed files by their source_maps row id, the ones used
// least recently dropped first once they take more than max bytes. A file
// is loaded once even when several requests ask for it at the same time.
type cache struct {
	max int64

	mu      sync.Mutex
	size    int64
	order   *list.List // front: used most recently
	items   map[int64]*list.Element
	loading map[int64]*load
}

type cacheEntry struct {
	key   int64
	value any
	size  int64
}

// load is a load in progress; others asking for the same key wait for it.
type load struct {
	done  chan struct{}
	value any
	err   error
}

func newCache(max int64) *cache {
	return &cache{max: max, order: list.New(), items: map[int64]*list.Element{}, loading: map[int64]*load{}}
}

// get returns the value for key, calling fill to make it when it is not
// cached. fill returns the value and its size in bytes; a value larger
// than the whole cache is returned without being kept. Errors are not cached.
func (c *cache) get(key int64, fill func() (any, int64, error)) (any, error) {
	c.mu.Lock()
	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		value := el.Value.(*cacheEntry).value
		c.mu.Unlock()
		return value, nil
	}
	if l, ok := c.loading[key]; ok {
		c.mu.Unlock()
		<-l.done
		return l.value, l.err
	}
	l := &load{done: make(chan struct{})}
	c.loading[key] = l
	c.mu.Unlock()

	var size int64
	func() {
		// A file that makes the parser panic is an error like any other,
		// and those waiting for it must not wait forever.
		defer func() {
			if r := recover(); r != nil {
				l.value, size, l.err = nil, 0, fmt.Errorf("reading the file failed: %v", r)
			}
		}()
		l.value, size, l.err = fill()
	}()

	c.mu.Lock()
	delete(c.loading, key)
	if l.err == nil && size <= c.max {
		c.items[key] = c.order.PushFront(&cacheEntry{key: key, value: l.value, size: size})
		c.size += size
		for c.size > c.max {
			oldest := c.order.Back()
			entry := oldest.Value.(*cacheEntry)
			c.order.Remove(oldest)
			delete(c.items, entry.key)
			c.size -= entry.size
		}
	}
	c.mu.Unlock()
	close(l.done)
	return l.value, l.err
}

// bytes returns how much the cached values take.
func (c *cache) bytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}

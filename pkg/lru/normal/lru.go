package lru

import "sync"

type LRUNode struct {
	key   string
	value interface{}
	prev  *LRUNode
	next  *LRUNode
}

type LRUCache interface {
	Get(key string) (interface{}, bool)
	Put(key string, value interface{})
}

func NewLRUCache(capacity int) LRUCache {
	return newLRUCache(capacity)
}

var _ LRUCache = (*LRUCacheImpl)(nil)

type LRUCacheImpl struct {
	capacity int
	cache    map[string]*LRUNode
	head     *LRUNode
	tail     *LRUNode
	mu       sync.RWMutex
}

func newLRUCache(capacity int) *LRUCacheImpl {
	if capacity <= 0 {
		capacity = 100
	}

	cache := &LRUCacheImpl{
		capacity: capacity,
		cache:    make(map[string]*LRUNode),
		head:     &LRUNode{},
		tail:     &LRUNode{},
	}
	cache.head.next = cache.tail
	cache.tail.prev = cache.head

	return cache
}

func (l *LRUCacheImpl) Get(key string) (interface{}, bool) {
	l.mu.RLock()
	node, ok := l.cache[key]
	l.mu.RUnlock()

	if !ok {
		return nil, false
	}

	l.mu.Lock()
	l.moveToHead(node)
	l.mu.Unlock()

	return node.value, true
}

func (l *LRUCacheImpl) Put(key string, value interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if node, ok := l.cache[key]; ok {
		node.value = value
		l.moveToHead(node)
		return
	}

	if len(l.cache) >= l.capacity {
		l.removeTail()
	}

	newNode := &LRUNode{
		key:   key,
		value: value,
	}
	l.cache[key] = newNode
	l.addToHead(newNode)

	if len(l.cache) > l.capacity {
		l.removeTail()
	}
}

func (l *LRUCacheImpl) moveToHead(node *LRUNode) {
	// remove node from current position
	node.prev.next = node.next
	node.next.prev = node.prev

	// insert node to head
	node.next = l.head.next
	node.prev = l.head
	l.head.next.prev = node
	l.head.next = node
}

func (l *LRUCacheImpl) removeTail() {
	// remove node from tail
	removed := l.tail.prev
	l.tail.prev = removed.prev
	removed.prev.next = l.tail

	// clear node ptr
	removed.prev = nil
	removed.next = nil

	delete(l.cache, removed.key)
}

func (l *LRUCacheImpl) addToHead(node *LRUNode) {
	// insert node to head
	node.next = l.head.next
	node.prev = l.head
	l.head.next.prev = node
	l.head.next = node
}

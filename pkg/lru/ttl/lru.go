package ttl

import (
	"sync"
	"time"
)

type LRUNode struct {
	key   string
	value interface{}
	prev  *LRUNode
	next  *LRUNode

	expire time.Time
}

type LRUCache interface {
	Get(key string) (interface{}, bool)
	Put(key string, value interface{})
	PutWithTTL(key string, value interface{}, ttl time.Duration)
	Len() int
	Close()
}

// ---------------------- 核心：Options模式定义 ----------------------
// LRUCacheOptions 缓存配置项，封装所有可选参数
type LRUCacheOptions struct {
	cleanInterval time.Duration // 定时清理间隔（0表示关闭定时清理）
	defaultTTL    time.Duration // Put方法的默认TTL（0表示永不过期）
}

// Option 配置函数类型，用于修改LRUCacheOptions
type Option func(*LRUCacheOptions)

// WithCleanInterval 设置定时清理过期节点的间隔（替代原有的cleanInterval参数）
func WithCleanInterval(interval time.Duration) Option {
	return func(o *LRUCacheOptions) {
		if interval > 0 { // 校验：间隔必须大于0
			o.cleanInterval = interval
		}
	}
}

// WithDefaultTTL 设置Put方法的默认TTL（新增：让Put也支持默认过期时间）
func WithDefaultTTL(ttl time.Duration) Option {
	return func(o *LRUCacheOptions) {
		o.defaultTTL = ttl // 允许0（永不过期）
	}
}

type LRUCacheImpl struct {
	opts     LRUCacheOptions
	capacity int
	cache    map[string]*LRUNode
	head     *LRUNode
	tail     *LRUNode
	mu       sync.Mutex

	cleanTicker *time.Ticker
	stopChan    chan struct{}
}

func NewLRUCache(capacity int, opts ...Option) LRUCache {
	if capacity <= 0 {
		capacity = 100
	}
	// 1. 设置默认配置
	defaultOpts := LRUCacheOptions{
		cleanInterval: 0, // 默认关闭定时清理
		defaultTTL:    0, // 默认Put永不过期
	}

	// 2. 应用用户传入的配置
	for _, opt := range opts {
		opt(&defaultOpts)
	}

	// 3. 初始化缓存实例
	cache := &LRUCacheImpl{
		opts:     defaultOpts,
		capacity: capacity,
		cache:    make(map[string]*LRUNode),
		head:     &LRUNode{},
		tail:     &LRUNode{},
		stopChan: make(chan struct{}),
	}
	cache.head.next = cache.tail
	cache.tail.prev = cache.head

	// 4. 启动定时清理（如果配置了间隔）
	if cache.opts.cleanInterval > 0 {
		cache.cleanTicker = time.NewTicker(cache.opts.cleanInterval)
		go cache.startPeriodicClean()
	}

	return cache
}

func (l *LRUCacheImpl) Get(key string) (interface{}, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	node, ok := l.cache[key]

	if !ok {
		return nil, false
	}

	// 检查过期（先判断是否有过期时间，再判断是否过期）
	if !node.expire.IsZero() && time.Now().After(node.expire) {
		l.removeNode(node)
		return nil, false
	}

	// 移动到头部（最近使用）
	l.moveToHead(node)

	return node.value, true
}

func (l *LRUCacheImpl) Put(key string, value interface{}) {
	// default ttl is 0, means never expire
	l.PutWithTTL(key, value, time.Duration(0))
}
func (l *LRUCacheImpl) PutWithTTL(key string, value interface{}, ttl time.Duration) {

	// set expire time
	var expire time.Time

	if ttl > 0 {
		expire = time.Now().Add(ttl)
	} else if l.opts.defaultTTL > 0 {
		expire = time.Now().Add(l.opts.defaultTTL)
	} else {
		expire = time.Time{}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if node, ok := l.cache[key]; ok {
		node.value = value
		node.expire = expire
		l.moveToHead(node)
		return
	}

	newNode := &LRUNode{
		key:    key,
		value:  value,
		expire: expire,
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

func (l *LRUCacheImpl) removeNode(node *LRUNode) {
	// remove node from current position
	node.prev.next = node.next
	node.next.prev = node.prev

	// clear node ptr
	node.next = nil
	node.prev = nil

	delete(l.cache, node.key)
}

// 定时清理过期节点的协程
func (l *LRUCacheImpl) startPeriodicClean() {
	for {
		select {
		case <-l.cleanTicker.C:
			l.cleanExpiredNodes() // 批量清理所有过期节点
		case <-l.stopChan:
			l.cleanTicker.Stop()
			return
		}
	}
}

// 批量清理所有过期节点（加锁操作，O(n)但低频执行）
func (l *LRUCacheImpl) cleanExpiredNodes() {
	l.mu.Lock()
	defer l.mu.Unlock()

	current := l.head.next
	for current != l.tail {
		next := current.next

		// 检查是否过期
		if !current.expire.IsZero() && time.Now().After(current.expire) {
			l.removeNode(current)
		}

		current = next
	}
}

// 新增关闭方法，优雅停止定时清理
func (l *LRUCacheImpl) Close() {
	close(l.stopChan)
}

// 新增缓存长度方法
func (l *LRUCacheImpl) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.cache)
}

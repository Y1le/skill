package ttl

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestLRUCache_Options_Default test default options
func TestLRUCache_Options_Default(t *testing.T) {
	cache := NewLRUCache(100)
	impl := cache.(*LRUCacheImpl)

	// 验证默认清理间隔（关闭）
	if impl.opts.cleanInterval != 0 {
		t.Errorf("Default clean interval should be 0, actual=%v", impl.opts.cleanInterval)
	}

	// 验证默认TTL（永不过期）
	cache.Put("key1", "val1")
	val, ok := cache.Get("key1")
	if !ok || val != "val1" {
		t.Errorf("Default TTL Put should never expire, actual: val=%v, ok=%v", val, ok)
	}
}

// TestLRUCache_Options_DefaultTTL test WithDefaultTTL option
func TestLRUCache_Options_DefaultTTL(t *testing.T) {
	// 配置默认TTL为1秒
	cache := NewLRUCache(100, WithDefaultTTL(1*time.Second))
	defer cache.Close()
	// Put方法自动使用默认TTL
	cache.Put("key1", "val1")

	// 立即读取：存在
	val, ok := cache.Get("key1")
	if !ok || val != "val1" {
		t.Errorf("Put after immediate read should exist, actual: val=%v, ok=%v", val, ok)
	}

	// 等待TTL过期
	time.Sleep(1100 * time.Millisecond)

	// 过期后读取：不存在
	val, ok = cache.Get("key1")
	if ok {
		t.Errorf("Default TTL expires, key should not exist, actual: val=%v", val)
	}

	// 验证PutWithTTL可覆盖默认TTL
	cache.PutWithTTL("key2", "val2", 5*time.Second)
	time.Sleep(1100 * time.Millisecond)
	val, ok = cache.Get("key2")
	if !ok || val != "val2" {
		t.Errorf("PutWithTTL overrides default TTL, should exist, actual: val=%v, ok=%v", val, ok)
	}
}

// TestLRUCache_Options_CleanInterval test WithCleanInterval option
func TestLRUCache_Options_CleanInterval(t *testing.T) {
	// 配置清理间隔500ms
	cache := NewLRUCache(10,
		WithCleanInterval(500*time.Millisecond),
	)
	impl := cache.(*LRUCacheImpl)
	defer impl.Close() // 测试后关闭

	// 写入3个过期节点（TTL 100ms）
	cache.PutWithTTL("exp1", "val1", 100*time.Millisecond)
	cache.PutWithTTL("exp2", "val2", 100*time.Millisecond)
	cache.PutWithTTL("exp3", "val3", 100*time.Millisecond)

	// 等待定时清理触发（>500ms）
	time.Sleep(600 * time.Millisecond)

	// 验证所有过期节点被清理
	if val, ok := cache.Get("exp1"); ok {
		t.Errorf("exp1 should be cleaned by schedule, actual: val=%v", val)
	}
	if val, ok := cache.Get("exp2"); ok {
		t.Errorf("exp2 should be cleaned by schedule, actual: val=%v", val)
	}

	// 验证永不过期节点不受影响
	cache.Put("noexp", "val4")
	if val, ok := cache.Get("noexp"); !ok || val != "val4" {
		t.Errorf("Never-expiring node should exist, actual: val=%v, ok=%v", val, ok)
	}
}

// ---------------------- 核心功能兼容性测试 ----------------------
// TestLRUCache_Concurrent_ReadWrite 并发读写测试（验证读写锁安全）
func TestLRUCache_Concurrent_ReadWrite(t *testing.T) {
	const (
		numWriters = 10
		numReaders = 50
		numOps     = 1000
	)
	// 配置缓存容量为 numOps * numWriters
	cache := NewLRUCache(numOps*numWriters, WithDefaultTTL(10*time.Second))
	_ = cache.(*LRUCacheImpl)
	defer cache.Close()

	var wg sync.WaitGroup

	// 写协程
	wg.Add(numWriters)
	for i := 0; i < numWriters; i++ {
		go func(writerID int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				key := fmt.Sprintf("key_%d_%d", writerID, j)
				value := fmt.Sprintf("value_%d_%d", writerID, j)

				cache.Put(key, value)
				val, ok := cache.Get(key)
				if !ok || val != value {
					t.Errorf("Write after read failed: key=%s, expected=%s, actual=%v", key, value, val)
				}
			}
		}(i)
	}

	// 读协程
	wg.Add(numReaders)
	for i := 0; i < numReaders; i++ {
		go func(readerID int) {
			defer wg.Done()
			for j := 0; j < numOps; j++ {
				writerID := j % numWriters
				key := fmt.Sprintf("key_%d_%d", writerID, j%numOps)
				_, _ = cache.Get(key)
			}
		}(i)
	}

	wg.Wait()
	// 最终验证
	testKey := "key_0_900"
	val, ok := cache.Get(testKey)
	if !ok || val != "value_0_900" {
		t.Errorf("Final verification failed: key=%s, expected=value_0_900, actual=%v", testKey, val)
	}
}

// TestLRUCache_Close 测试优雅关闭
func TestLRUCache_Close(t *testing.T) {
	// 配置定时清理
	cache := NewLRUCache(0, WithCleanInterval(100*time.Millisecond))
	impl := cache.(*LRUCacheImpl)

	// 验证清理协程正在运行
	if impl.cleanTicker == nil {
		t.Error("Clean Ticker should be initialized when WithCleanInterval is set")
	}

	// 关闭缓存
	impl.Close()
	assert.Equal(t, <-impl.stopChan, struct{}{})

	// 验证关闭后仍可正常读写（核心功能不受影响）
	cache.Put("key1", "val1")
	val, ok := cache.Get("key1")
	if !ok || val != "val1" {
		t.Errorf("After closing, reading and writing should be normal and practical: val=%v, ok=%v", val, ok)
	}
}

package lru

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestLRUCache(t *testing.T) {
	cache := NewLRUCache(2)

	// basic test
	cache.Put("a", 1)
	cache.Put("b", 2)

	val, ok := cache.Get("a")
	assert.Equal(t, 1, val)
	assert.True(t, ok)

	// capacity test
	cache.Put("c", 3)
	_, ok = cache.Get("b")
	assert.False(t, ok)

	// update test
	cache.Put("a", 10)
	val, ok = cache.Get("a")
	assert.Equal(t, 10, val)

	// concurrent test
	const (
		capacity   = 1000
		numWorkers = 20
		numOps     = 5000
	)

	cache = NewLRUCache(capacity)

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	expected := make(map[string]int)
	var mu sync.Mutex

	start := time.Now()

	for workerID := 0; workerID < numWorkers; workerID++ {
		go func(id int) {
			defer wg.Done()
			rng := id

			for i := 0; i < numOps; i++ {
				rng = (rng*1664525 + 1013904223) % 214748364
				key := fmt.Sprintf("key-%d", rng%500)
				value := id*1000 + i

				// random 70% Get, 30% Put
				if rng%10 < 7 {
					// Get
					if v, ok := cache.Get(key); ok {
						assert.Equal(t, int64(value), v.(int64))
					}
				} else {
					// Put
					cache.Put(key, value)

					// record expected value
					mu.Lock()
					expected[key] = value
					mu.Unlock()
				}
			}
		}(workerID)
	}

	wg.Wait()
	duration := time.Since(start)
	t.Logf("Concurrent test finished in %v with %d workers, %d ops each", duration, numWorkers, numOps)

	// verify final cache state
	mu.Lock()
	defer mu.Unlock()

	for key, expectedVal := range expected {
		if got, ok := cache.Get(key); ok {
			if got != expectedVal {
				t.Errorf("Key %s: expected %d, got %d", key, expectedVal, got)
			}
		} else {
			t.Logf("Key %s was evicted (capacity=%d)", key, capacity)
		}
	}
}

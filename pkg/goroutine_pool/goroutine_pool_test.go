package gpool

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestGoroutinePoolBasicSubmitAndExecute(t *testing.T) {
	assert := assert.New(t)

	pool := NewGoroutinePool(
		WithMinWorkers(2),
		WithMaxWorkers(10),
		WithQueueSize(100),
	)
	defer pool.Stop()

	var (
		wg      sync.WaitGroup
		counter int32
		taskNum = 100
	)

	wg.Add(taskNum)
	for i := 0; i < taskNum; i++ {
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			atomic.AddInt32(&counter, 1)
			return nil
		}, WithPriority(PriorityNormal))

		err := pool.Submit(task)
		assert.NoError(err, "提交任务%d失败:%v", i, err)
	}

	// 等待所有任务完成
	wg.Wait()

	// 关键:原子读取counter，避免并发问题
	assert.Equal(int32(taskNum), atomic.LoadInt32(&counter),
		"任务执行数不符，期望%d，实际%d", taskNum, atomic.LoadInt32(&counter))

	// 验证等待任务数为0
	stats := pool.Stats()
	assert.Equal(int32(0), stats["waiting_tasks"], "任务完成后等待数应为0，实际%d", stats["waiting_tasks"])
}

func TestGoroutinePoolDynamicScale(t *testing.T) {
	assert := assert.New(t)

	pool := NewGoroutinePool(
		WithMinWorkers(2),
		WithMaxWorkers(10),
		WithQueueSize(1000),
	)
	defer pool.Stop()

	var wg sync.WaitGroup
	taskNum := 100
	wg.Add(taskNum)

	// 批量提交任务，触发扩容
	for i := 0; i < taskNum; i++ {
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			time.Sleep(50 * time.Millisecond) // 让任务执行稍久，触发扩容
			return nil
		})
		err := pool.Submit(task)
		assert.NoError(err, "提交扩容任务%d失败:%v", i, err)
	}

	// 等待扩容触发（扩缩容检查间隔500ms）
	time.Sleep(600 * time.Millisecond)

	// 验证扩容:活跃协程数超过最小数
	stats := pool.Stats()
	minWorkers := stats["min_workers"]
	activeWorkersAfterScale := stats["active_workers"]
	assert.Greater(activeWorkersAfterScale, minWorkers,
		"扩容未触发:活跃协程数%d ≤ 最小协程数%d", activeWorkersAfterScale, minWorkers)

	// 等待任务完成，验证缩容
	wg.Wait()
	time.Sleep(1 * time.Second) // 等待缩容检查
	stats = pool.Stats()
	t.Logf("stats:%v", stats)
	activeWorkersAfterShrink := stats["active_workers"]
	maxAllowedWorkers := minWorkers + 1 // 缩容允许±1的误差
	assert.LessOrEqual(activeWorkersAfterShrink, maxAllowedWorkers,
		"缩容未触发:活跃协程数%d > 最小协程数+1(%d)", activeWorkersAfterShrink, maxAllowedWorkers)
}

func TestGoroutinePoolGracefulStop(t *testing.T) {
	assert := assert.New(t)

	pool := NewGoroutinePool(
		WithMinWorkers(2),
		WithMaxWorkers(10),
		WithQueueSize(100),
	)
	defer pool.Stop()

	var (
		wg      sync.WaitGroup
		counter int32
		taskNum = 20
	)

	wg.Add(taskNum)
	for i := 0; i < taskNum; i++ {
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			atomic.AddInt32(&counter, 1)
			time.Sleep(10 * time.Millisecond) // 模拟任务执行
			return nil
		})

		err := pool.Submit(task)
		assert.NoError(err, "提交优雅关闭任务%d失败", i)
	}

	// 异步调用优雅关闭
	go func() {
		pool.Stop()
	}()

	// 等待所有任务完成
	wg.Wait()

	//  验证所有任务执行完成（原子计数匹配）
	assert.Equal(int32(taskNum), atomic.LoadInt32(&counter),
		"优雅关闭未执行完所有任务，期望%d，实际%d", taskNum, atomic.LoadInt32(&counter))

	// 验证关闭后无法提交新任务
	task := NewTask(func(ctx context.Context) error { return nil })
	err := pool.Submit(task)
	assert.Error(err, "关闭后提交新任务应返回错误")
	assert.Equal("pool is closed, cannot submit new task", err.Error(),
		"关闭后提交新任务的错误信息不匹配，期望:%s，实际:%v",
		"pool is closed, cannot submit new task", err)
}
func TestGoroutinePoolForceStop(t *testing.T) {
	assert := assert.New(t)

	pool := NewGoroutinePool(
		WithMinWorkers(2),
		WithMaxWorkers(10),
		WithQueueSize(100),
	)

	var (
		wg      sync.WaitGroup
		counter int32
		taskNum = 50
	)

	wg.Add(taskNum)
	for i := 0; i < taskNum; i++ {
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			select {
			case <-ctx.Done():
			case <-time.After(200 * time.Millisecond):
				return nil
			}
			return nil
		})

		err := pool.Submit(task)
		assert.NoError(err, "提交强制关闭任务%d失败:%v", i, err)
	}

	pool.StopNow()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 正常完成，验证强制关闭效果
		assert.Less(atomic.LoadInt32(&counter), int32(taskNum),
			"强制关闭应终止任务，实际执行了%d个（总%d个）", atomic.LoadInt32(&counter), taskNum)
	case <-time.After(2 * time.Second): // 2秒超时足够验证强制关闭
		t.Fatal("测试超时：强制关闭未终止任务，wg等待超时")
	}
}

func TestGoroutinePoolTaskPriority(t *testing.T) {
	assert := assert.New(t)

	pool := NewGoroutinePool(
		WithMinWorkers(2),
		WithMaxWorkers(5),
		WithQueueSize(100),
	)
	defer pool.Stop()

	var (
		highCounter int32
		lowCounter  int32
		wg          sync.WaitGroup
	)

	// 先提交大量低优先级任务
	lowTaskNum := 50
	wg.Add(lowTaskNum)
	for i := 0; i < lowTaskNum; i++ {
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond) // 让低优先级任务排队
			atomic.AddInt32(&lowCounter, 1)
			return nil
		}, WithPriority(PriorityLow))
		err := pool.Submit(task)
		assert.NoError(err, "提交低优先级任务%d失败:%v", i, err)
	}

	// 再提交少量高优先级任务
	highTaskNum := 10
	wg.Add(highTaskNum)
	for i := 0; i < highTaskNum; i++ {
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			atomic.AddInt32(&highCounter, 1)
			return nil
		}, WithPriority(PriorityHigh))
		err := pool.Submit(task)
		assert.NoError(err, "提交高优先级任务%d失败:%v", i, err)
	}

	// 等待高优先级任务先完成
	time.Sleep(50 * time.Millisecond)
	// 验证高优先级任务已全部执行，低优先级任务未完成
	assert.Equal(int32(highTaskNum), atomic.LoadInt32(&highCounter),
		"高优先级任务未全部执行，期望%d，实际%d", highTaskNum, atomic.LoadInt32(&highCounter))
	assert.NotEqual(int32(lowTaskNum), atomic.LoadInt32(&lowCounter),
		"低优先级任务不应全部执行，实际执行了%d个", atomic.LoadInt32(&lowCounter))

	// 等待所有任务完成
	wg.Wait()
}

func TestGoroutinePoolTaskTimeout(t *testing.T) {
	assert := assert.New(t)

	pool := NewGoroutinePool(
		WithMinWorkers(2),
		WithMaxWorkers(10),
		WithQueueSize(100),
	)
	defer pool.Stop()

	var (
		timeoutCounter int32
		wg             sync.WaitGroup
	)

	taskNum := 10
	wg.Add(taskNum)
	for i := 0; i < taskNum; i++ {
		// 改造任务函数：接收ctx，监听超时信号
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			select {
			case <-ctx.Done():
				atomic.AddInt32(&timeoutCounter, 1)
				return fmt.Errorf("任务%d超时: %w", i, ctx.Err())
			case <-time.After(200 * time.Millisecond):
				return nil
			}
		}, WithTimeout(100*time.Millisecond))

		err := pool.Submit(task)
		assert.NoError(err, "提交超时任务%d失败:%v", i, err)

	}

	// 等待所有任务完成
	wg.Wait()

	// 验证所有任务都超时
	assert.Equal(int32(taskNum), atomic.LoadInt32(&timeoutCounter),
		"超时任务数不符，期望%d，实际%d", taskNum, atomic.LoadInt32(&timeoutCounter))
}

func TestGoroutinePoolQueueFull(t *testing.T) {
	assert := assert.New(t)

	// 队列容量仅10
	pool := NewGoroutinePool(
		WithMinWorkers(2),
		WithMaxWorkers(2),
		WithQueueSize(10),
	)
	defer pool.Stop()

	var wg sync.WaitGroup
	// 提交20个任务，超出队列容量
	for i := 0; i < 20; i++ {
		task := NewTask(func(ctx context.Context) error {
			defer wg.Done()
			time.Sleep(1 * time.Second)
			return nil
		}, WithPriority(PriorityNormal))

		err := pool.Submit(task)
		if i < 10 {
			assert.NoError(err, "队列未满时提交任务%d失败:%v", i, err)
			wg.Add(1)
		} else {
			assert.Error(err, "队列满时提交任务%d应失败", i)
			assert.Equal("normal queue is full", err.Error(),
				"任务%d队列满错误信息不匹配，期望:%s，实际:%v",
				i, "normal queue is full", err)
		}
	}

	wg.Wait()
}

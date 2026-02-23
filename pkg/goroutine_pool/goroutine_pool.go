package gpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type GoroutinePool struct {
	// configuration parameters
	minWorkers    int32
	maxWorkers    int32
	queueSize     int32
	scaleInterval time.Duration
	idleTimeout   time.Duration

	// worker pools
	highQueue   chan *Task
	normalQueue chan *Task
	lowQueue    chan *Task

	// control signal
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	workerChan chan struct{}

	// statistical information
	activeWorkers int32
	activeTasks   int32
	waitingTasks  int32

	// 优雅关机
	submitClosed int32          // 原子标记：是否禁止提交新任务（0=允许，1=禁止）
	taskWg       sync.WaitGroup // 等待所有已提交任务执行完成
}

type Option func(*GoroutinePool)

// 调整NewGoroutinePool中的队列初始化逻辑（在opts执行后）
func NewGoroutinePool(opts ...Option) *GoroutinePool {
	p := &GoroutinePool{
		minWorkers:    10,
		maxWorkers:    100,
		queueSize:     1000,
		scaleInterval: 50 * time.Millisecond,
		idleTimeout:   1 * time.Second,
	}

	// 先执行配置项，再初始化队列
	for _, opt := range opts {
		opt(p)
	}

	// 初始化通道（基于最终配置的queueSize/maxWorkers）
	p.workerChan = make(chan struct{}, p.maxWorkers)
	p.highQueue = make(chan *Task, p.queueSize)
	p.normalQueue = make(chan *Task, p.queueSize)
	p.lowQueue = make(chan *Task, p.queueSize)

	p.ctx, p.cancel = context.WithCancel(context.Background())

	for i := int32(0); i < p.minWorkers; i++ {
		p.startWorker()
	}

	go p.scaleWorker()

	return p
}

func WithMinWorkers(n int32) Option {
	return func(p *GoroutinePool) {
		p.minWorkers = n
	}
}

func WithMaxWorkers(n int32) Option {
	return func(p *GoroutinePool) {
		if n >= p.minWorkers { // 确保最大数≥最小数
			p.maxWorkers = n
		}
	}
}

func WithQueueSize(n int32) Option {
	return func(p *GoroutinePool) {
		if n > 0 && n <= 100000 { // 限制最大队列容量，避免内存溢出
			p.queueSize = n
		}
	}
}
func (p *GoroutinePool) startWorker() {
	if p.maxWorkers < p.minWorkers {
		panic("maxWorkers must be greater than or equal to minWorkers")
	}
	if atomic.LoadInt32(&p.activeWorkers) >= p.maxWorkers {
		return
	}
	p.wg.Add(1)
	atomic.AddInt32(&p.activeWorkers, 1)
	p.workerChan <- struct{}{}

	go func() {
		defer func() {
			<-p.workerChan
			atomic.AddInt32(&p.activeWorkers, -1)
			p.wg.Done()
			if r := recover(); r != nil {
				fmt.Printf("worker panic: %v\n", r)
			}
		}()

		// 初始化空闲定时器（核心：只初始化一次，避免重复创建）
		idleTimer := time.NewTimer(p.idleTimeout)
		defer idleTimer.Stop()

		for {
			// 重置空闲定时器（关键：先停止，避免漏读已触发的C通道）
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(p.idleTimeout)

			var task *Task
			// 扁平化优先级消费逻辑（彻底移除嵌套select）
			select {
			// 池关闭，立即退出
			case <-p.ctx.Done():
				return
			// 空闲超时，且协程数>最小数 → 退出（缩容）
			case <-idleTimer.C:
				if atomic.LoadInt32(&p.activeWorkers) > p.minWorkers {
					return
				}
				continue
			// 按优先级消费任务（高→中→低）
			case task = <-p.highQueue:
			case task = <-p.normalQueue:
			case task = <-p.lowQueue:
			}

			// 处理队列关闭后的nil任务
			if task == nil {
				continue
			}

			// 更新统计信息
			atomic.AddInt32(&p.activeTasks, 1)
			atomic.AddInt32(&p.waitingTasks, -1)
			_ = task.fn(task.ctx)
			task.Release()

			// 任务执行完成，更新统计
			atomic.AddInt32(&p.activeTasks, -1)
			p.taskWg.Done()
		}
	}()
}
func (p *GoroutinePool) scaleWorker() {
	ticker := time.NewTicker(p.scaleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			waiting := atomic.LoadInt32(&p.waitingTasks)
			activeWorkers := atomic.LoadInt32(&p.activeWorkers)

			// 扩容逻辑：等待任务多，且未到最大协程数
			if waiting > activeWorkers*2 && activeWorkers < p.maxWorkers {
				newWorkers := int32(float64(activeWorkers) * 0.1)
				if newWorkers < 1 {
					newWorkers = 1
				}
				if activeWorkers+newWorkers > p.maxWorkers {
					newWorkers = p.maxWorkers - activeWorkers
				}
				for i := int32(0); i < newWorkers; i++ {
					p.startWorker()
				}
			}
		}
	}
}
func (p *GoroutinePool) Submit(task *Task) error {
	const (
		ErrPoolClosed      = "pool is closed, cannot submit new task"
		ErrHighQueueFull   = "high queue is full"
		ErrNormalQueueFull = "normal queue is full"
		ErrLowQueueFull    = "low queue is full"
		ErrInvalidPriority = "invalid priority"
		ErrInvalidTask     = "invalid task: fn is nil"
	)

	if atomic.LoadInt32(&p.submitClosed) == 1 {
		return errors.New(ErrPoolClosed)
	}

	if task == nil || task.fn == nil {
		return errors.New(ErrInvalidTask)
	}

	if task.timeout > 0 {
		task.ctx, task.cancel = context.WithTimeout(p.ctx, task.timeout)
	} else {
		task.ctx, task.cancel = context.WithCancel(p.ctx)
	}
	p.taskWg.Add(1)
	select {
	case <-p.ctx.Done():
		p.taskWg.Done()
		return errors.New(ErrPoolClosed)
	default:
		atomic.AddInt32(&p.waitingTasks, 1)
		switch task.priority {
		case PriorityHigh:
			select {
			case p.highQueue <- task:
				return nil
			default:
				atomic.AddInt32(&p.waitingTasks, -1)
				p.taskWg.Done()
				return errors.New(ErrHighQueueFull)
			}
		case PriorityNormal:
			select {
			case p.normalQueue <- task:
				return nil
			default:
				atomic.AddInt32(&p.waitingTasks, -1)
				p.taskWg.Done()
				return errors.New(ErrNormalQueueFull)
			}
		case PriorityLow:
			select {
			case p.lowQueue <- task:
				return nil
			default:
				atomic.AddInt32(&p.waitingTasks, -1)
				p.taskWg.Done()
				return errors.New(ErrLowQueueFull)
			}
		default:
			atomic.AddInt32(&p.waitingTasks, -1)
			p.taskWg.Done()
			return errors.New(ErrInvalidPriority)
		}
	}
}

// Stop 优雅关闭：禁止新任务提交 + 等待所有已提交任务执行完成 + 关闭协程池
func (p *GoroutinePool) Stop() {
	// 1. 原子标记：禁止提交新任务（仅执行一次）
	if !atomic.CompareAndSwapInt32(&p.submitClosed, 0, 1) {
		return // 已调用过关闭，直接返回
	}

	// 2. 等待所有已提交任务执行完成
	p.taskWg.Wait()

	// 3. 取消池上下文，通知工作协程退出
	p.cancel()

	// 4. 关闭任务队列（防止工作协程阻塞在队列读取）
	close(p.highQueue)
	close(p.normalQueue)
	close(p.lowQueue)

	// 5. 等待所有工作协程正常退出
	p.wg.Wait()
}

// StopNow 强制关闭：禁止新任务提交 + 立即终止所有任务 + 丢弃等待任务 + 关闭协程池
func (p *GoroutinePool) StopNow() {
	//1. 禁止提交新任务
	if !atomic.CompareAndSwapInt32(&p.submitClosed, 0, 1) {
		return
	}

	// 2. 立即取消池上下文，终止正在执行的任务
	p.cancel()

	cleanQueue := func(queue chan *Task) {
		for {
			select {
			case task := <-queue:
				task.Release()
				p.taskWg.Done()
				atomic.AddInt32(&p.waitingTasks, -1)
			default:
				return
			}
		}
	}
	cleanQueue(p.highQueue)
	cleanQueue(p.normalQueue)
	cleanQueue(p.lowQueue)

	// 4. 关闭队列
	close(p.highQueue)
	close(p.normalQueue)
	close(p.lowQueue)

	// 5. 等待工作协程退出
	p.wg.Wait()

	// 6. 重置taskWg
	p.taskWg = sync.WaitGroup{}
}
func (p *GoroutinePool) Stats() map[string]int32 {
	return map[string]int32{
		"active_workers": atomic.LoadInt32(&p.activeWorkers),
		"active_tasks":   atomic.LoadInt32(&p.activeTasks),
		"waiting_tasks":  atomic.LoadInt32(&p.waitingTasks),
		"min_workers":    p.minWorkers,
		"max_workers":    p.maxWorkers,
	}
}

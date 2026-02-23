package gpool

import (
	"context"
	"sync"
	"time"
)

type Priority int

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
)

// Task define the task structure
type Task struct {
	fn       func(ctx context.Context) error
	priority Priority
	timeout  time.Duration
	ctx      context.Context
	cancel   context.CancelFunc
}

// task object pool
var taskPool = sync.Pool{
	New: func() interface{} {
		return &Task{}
	},
}

func NewTask(fn func(ctx context.Context) error, opts ...func(*Task)) *Task {
	t := taskPool.Get().(*Task)
	t.fn = fn
	t.priority = PriorityNormal
	t.timeout = 0
	t.ctx = nil
	t.cancel = nil

	for _, opt := range opts {
		opt(t)
	}
	return t
}

func WithPriority(p Priority) func(*Task) {
	return func(t *Task) {
		t.priority = p
	}
}

func WithTimeout(d time.Duration) func(*Task) {
	return func(t *Task) {
		t.timeout = d
	}
}

func (t *Task) Release() {
	if t.cancel != nil {
		t.cancel()
	}

	t.fn = nil
	t.priority = PriorityNormal
	t.timeout = 0
	t.ctx = nil
	taskPool.Put(t)
}

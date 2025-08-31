package netlimit

import (
	"net"
	"sync"
	"time"
)

type taskQueue struct {
	tasks []int64
	start time.Time
	timer *time.Timer
	mu    sync.Mutex
}

type Limiter struct {
	localAddr net.Addr
	bandwidth int64
	latencies map[string]time.Duration
	taskMap   map[net.Conn]*taskQueue
	mu        sync.Mutex
}

func NewLimiter(localAddr net.Addr, bandwidth int64, latencies map[string]time.Duration) *Limiter {
	return &Limiter{
		localAddr: localAddr,
		bandwidth: bandwidth,
		latencies: latencies,
		taskMap:   make(map[net.Conn]*taskQueue),
	}
}

func (l *Limiter) NewDialer(timeout time.Duration) *Dialer {
	return &Dialer{
		Dialer: net.Dialer{
			Timeout: timeout,
		},
		limiter: l,
	}
}

func (l *Limiter) Listen(network, address string) (net.Listener, error) {
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	return &Listener{
		Listener: listener,
		limiter:  l,
	}, nil
}

func (l *Limiter) updateTasks(prevTaskNum int) {
	curTime := time.Now()
	for _, queue := range l.taskMap {
		if queue.timer != nil && prevTaskNum > 0 {
			queue.tasks[0] -= l.bandwidth * curTime.Sub(queue.start).Microseconds() / 1000000 / int64(prevTaskNum)
		}
		queue.start = curTime
		delay := time.Second * time.Duration(queue.tasks[0]*int64(len(l.taskMap))) / time.Duration(l.bandwidth)
		if queue.timer != nil {
			queue.timer.Reset(delay)
		} else {
			queue.timer = time.NewTimer(delay)
		}
	}
}

func (l *Limiter) Wait(conn net.Conn, n int64) {
	l.mu.Lock()
	prevTaskNum := len(l.taskMap)
	queue, ok := l.taskMap[conn]
	if !ok {
		queue = &taskQueue{
			tasks: make([]int64, 0),
		}
		l.taskMap[conn] = queue
	}
	queue.tasks = append(queue.tasks, n)
	l.mu.Unlock()

	queue.mu.Lock()
	defer queue.mu.Unlock()

	l.mu.Lock()
	l.updateTasks(prevTaskNum)
	l.mu.Unlock()

	<-queue.timer.C

	l.mu.Lock()
	queue.tasks = queue.tasks[1:]
	if len(queue.tasks) == 0 {
		delete(l.taskMap, conn)
		l.updateTasks(len(l.taskMap) + 1)
	}
	l.mu.Unlock()
}

package server

import (
	"container/heap"
	"context"
	"encoding/json"
	"sync"
	"time"
)

const defaultEventChanLength = 32

type eventType int

const (
	evTypeShardUpdate eventType = iota
	evTypeShardDel
	evTypeContainerUpdate
	evTypeContainerDel
)

type loadEvent struct {
	Service     string    `json:"service"`
	Type        eventType `json:"type"`
	EnqueueTime int64     `json:"enqueueTime"`
	Load        string    `json:"load"`
}

func (i *loadEvent) String() string {
	b, _ := json.Marshal(i)
	return string(b)
}

type eventQueue struct {
	goroutineStopper

	// 延迟队列: 不能立即处理的先放这里，启动单独的goroutine把event根据时间拿出来，再放到异步队列中
	pq PriorityQueue

	mu        sync.Mutex
	evChan    map[string]chan *loadEvent // 区分service给chan，每个worker给一个goroutine
	curEvList map[string]struct{}        // 防止同一service在queue中有重复任务
}

func newEventQueue(ctx context.Context) *eventQueue {
	eq := eventQueue{}

	eq.ctx, eq.cancel = context.WithCancel(ctx)
	eq.evChan = make(map[string]chan *loadEvent)

	heap.Init(&eq.pq)

	eq.wg.Add(1)
	go eq.pqLoop()

	return &eq
}

func (eq *eventQueue) Close() {
	eq.cancel()
	eq.wg.Wait()
}

func (eq *eventQueue) push(item *Item, checkDup bool) {
	eq.mu.Lock()
	defer eq.mu.Unlock()

	var ev loadEvent
	if err := json.Unmarshal([]byte(item.Value), &ev); err != nil {
		Logger.Printf("[eq] err: %v", err)
		return
	}

	if checkDup {
		if _, ok := eq.curEvList[ev.Service]; ok {
			Logger.Printf("[eq] service %s already exist in queue", ev.Service)
			eq.mu.Unlock()
			return
		}
		eq.curEvList[ev.Service] = struct{}{}
	}

	ch, ok := eq.evChan[ev.Service]
	if !ok {
		ch = make(chan *loadEvent, defaultEventChanLength)
		eq.evChan[ev.Service] = ch

		Logger.Printf("[eq] evLoop started for service %s", ev.Service)
		eq.wg.Add(1)
		go eq.evLoop(ev.Service, ch)
	}

	switch ev.Type {
	case evTypeShardUpdate, evTypeContainerUpdate:
		ch <- &ev
	case evTypeShardDel, evTypeContainerDel:
		if time.Now().Unix() >= item.Priority {
			ch <- &ev
			return
		}
		Logger.Printf("[eq] item enqueue %s", item)
		heap.Push(&eq.pq, item)
	}
}

func (eq *eventQueue) pqLoop() {
	defer eq.wg.Done()

	ticker := time.Tick(1 * time.Second)
	for {
		select {
		case <-eq.ctx.Done():
			Logger.Println("[eq] pqLoop exit")
			return
		case <-ticker:
		popASAP:
			v := heap.Pop(&eq.pq)
			if v == nil {
				continue
			}
			item := v.(*Item)

			if time.Now().Unix() < item.Priority {
				// TODO 重复入队的代价在heap场景比较大，需要优化掉
				heap.Push(&eq.pq, item)
				continue
			}
			eq.push(item, false)

			// 存在需要处理的事件，立即pop，减小延迟
			goto popASAP
		}
	}
}

func (eq *eventQueue) evLoop(service string, ch chan *loadEvent) {
	defer eq.wg.Done()

	// worker只启动一个，用于计算，算法本身可以利用多核能力
	for {
		var ev *loadEvent
		select {
		case <-eq.ctx.Done():
			Logger.Printf("[eq] evLoop for service [%s] exit", service)
			return
		case ev = <-ch:
		}

		Logger.Printf("[eq] got ev %s", ev)

		// TODO 同一service需要保证只有一个goroutine在计算，否则没有意义
		switch ev.Type {
		case evTypeShardUpdate:
			// TODO 解析load，确定shard的load超出阈值，触发shard move
		case evTypeShardDel:
			// TODO 检查shard是否都处于有container的状态
		case evTypeContainerUpdate:
			// TODO
		case evTypeContainerDel:
			// TODO
		}
	}
}
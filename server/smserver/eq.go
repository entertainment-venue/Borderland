// Copyright 2021 The entertainment-venue Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package smserver

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/entertainment-venue/sm/pkg/apputil"
	"github.com/entertainment-venue/sm/pkg/etcdutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

const defaultEventChanLength = 32

type eventType int

const (
	tShardChanged eventType = iota + 1
	tShardLoadChanged
	tContainerChanged
	tContainerLoadChanged
	tContainerInit
)

type mvEvent struct {
	Service     string    `json:"service"`
	Type        eventType `json:"type"`
	EnqueueTime int64     `json:"enqueueTime"`
	Value       string    `json:"value"`
}

func (i *mvEvent) String() string {
	b, _ := json.Marshal(i)
	return string(b)
}

type eventQueue struct {
	parent  *smContainer
	stopper *apputil.GoroutineStopper

	// 延迟队列: 不能立即处理的先放这里，启动单独的goroutine把event根据时间拿出来，再放到异步队列中
	pq PriorityQueue

	mu            sync.Mutex
	serviceEvChan map[string]chan *mvEvent // 区分service给chan，每个worker给一个goroutine
	serviceBlk    map[string]struct{}      // 防止同一service在queue中有重复任务

	lg *zap.Logger
}

func newEventQueue(_ context.Context, lg *zap.Logger, sc *smContainer) *eventQueue {
	eq := eventQueue{
		parent:        sc,
		serviceEvChan: make(map[string]chan *mvEvent),
		serviceBlk:    make(map[string]struct{}),
		stopper:       &apputil.GoroutineStopper{},
		lg:            lg,
	}

	heap.Init(&eq.pq)

	eq.stopper.Wrap(
		func(ctx context.Context) {
			apputil.TickerLoop(
				ctx,
				lg,
				1*time.Second,
				fmt.Sprintf("tryPopAndPush loop exit, service %s", eq.parent.Service()),
				func(ctx context.Context) error {
					eq.tryPopAndPush()
					return nil
				},
			)
		})
	return &eq
}

func (eq *eventQueue) Close() {
	if eq.stopper != nil {
		eq.stopper.Close()
	}
	eq.lg.Info("eq closed", zap.String("service", eq.parent.Service()))
}

func (eq *eventQueue) push(item *Item, checkDup bool) {
	eq.mu.Lock()
	defer eq.mu.Unlock()

	var ev mvEvent
	if err := json.Unmarshal([]byte(item.Value), &ev); err != nil {
		eq.lg.Error("Unmarshal err", zap.String("raw", item.Value))
		return
	}

	if checkDup {
		if _, ok := eq.serviceBlk[ev.Service]; ok {
			eq.lg.Error("service already exist", zap.String("service", ev.Service))
			return
		}
		eq.serviceBlk[ev.Service] = struct{}{}
	}

	ch := eq.serviceEvChan[ev.Service]
	if ch == nil {
		ch = make(chan *mvEvent, defaultEventChanLength)
		eq.serviceEvChan[ev.Service] = ch

		// 区分service启动evLoop，目前直接将任务设置到etcd节点中，因为所有任务都是不断重入的，不担心错过，如果当前正在处理某个任务，就直接放弃
		eq.stopper.Wrap(
			func(ctx context.Context) {
				eq.evLoop(ctx, ev.Service, ch)
			})

		eq.lg.Info("evLoop started", zap.String("service", ev.Service))
	}

	switch ev.Type {
	case tShardChanged, tContainerChanged, tContainerInit:
		ch <- &ev
	case tShardLoadChanged, tContainerLoadChanged:
		if time.Now().Unix() >= item.Priority {
			ch <- &ev
			return
		}

		eq.lg.Info("item enqueue", zap.String("item", item.String()))
		heap.Push(&eq.pq, item)
	default:
		eq.lg.Panic(fmt.Sprintf("unknown ev type %d", ev.Type))
	}
}

func (eq *eventQueue) tryPopAndPush() {
popASAP:
	v := heap.Pop(&eq.pq)
	if v == nil {
		return
	}
	item := v.(*Item)

	if time.Now().Unix() < item.Priority {
		// TODO 重复入队的代价在heap场景比较大，需要优化掉
		heap.Push(&eq.pq, item)
		return
	}
	eq.push(item, false)

	// 存在需要处理的事件，立即pop，减小延迟
	goto popASAP
}

func (eq *eventQueue) evLoop(ctx context.Context, service string, ch chan *mvEvent) {
	// worker只启动一个，用于计算，算法本身可以利用多核能力
	for {
		var ev *mvEvent
		select {
		case <-ctx.Done():
			eq.lg.Info("evLoop exit", zap.String("service", service))
			return
		case ev = <-ch:
		}

		eq.lg.Info("ev received", zap.Reflect("ev", ev))

		key := apputil.EtcdPathAppShardTask(eq.parent.service)
		if _, err := eq.parent.Client.CompareAndSwap(ctx, key, "", ev.Value, clientv3.NoLease); err != nil {
			if err == etcdutil.ErrEtcdValueNotMatch {
				eq.lg.Warn(
					"can not add task, value conflict",
					zap.String("key", key),
					zap.String("value", ev.Value),
					zap.Error(err),
				)
			} else {
				eq.lg.Warn("failed to put task",
					zap.String("key", key),
					zap.String("value", ev.Value),
					zap.Error(err),
				)
			}
		}
		// 清理掉service的站位，允许该service的下一个event进来
		eq.mu.Lock()
		delete(eq.serviceBlk, ev.Service)
		eq.mu.Unlock()
	}
}

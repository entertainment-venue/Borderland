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

package apputil

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/entertainment-venue/sm/pkg/etcdutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

var (
	ttLogger, _ = zap.NewProduction()
)

func Test_tickerLoop(t *testing.T) {
	var (
		wg          sync.WaitGroup
		ctx, cancel = context.WithCancel(context.Background())
	)

	wg.Add(1)
	go TickerLoop(
		ctx,
		ttLogger,
		time.Second,
		"test loop exit",
		func(ctx context.Context) error {
			fmt.Println("test fn " + time.Now().String())
			return nil
		},
	)

	go func() {
		for {
			select {
			case <-time.After(3 * time.Second):
				cancel()
			}
		}
	}()

	wg.Wait()
	fmt.Println("TestTickerLoop exit")
}

func Test_WatchLoop(t *testing.T) {
	client, err := etcdutil.NewEtcdClient([]string{"127.0.0.1:2379"}, ttLogger)
	if err != nil {
		t.Errorf("err: %v", err)
		t.SkipNow()
	}

	resp, err := client.GetKV(context.TODO(), "foo", nil)
	if err != nil {
		t.Error(err)
		t.SkipNow()
	}
	fmt.Println(resp.Header.GetRevision())

	WatchLoop(
		context.TODO(),
		ttLogger,
		client.Client,
		"foo",
		resp.Header.GetRevision()+1,
		func(ctx context.Context, ev *clientv3.Event) error {
			fmt.Println(ev.Type, ev.Kv.CreateRevision, ev.Kv.ModRevision)
			return nil
		},
	)
}

func Test_WatchLoop_close(t *testing.T) {
	var (
		wg          sync.WaitGroup
		ctx, cancel = context.WithCancel(context.Background())
	)

	client, err := etcdutil.NewEtcdClient([]string{"127.0.0.1:2379"}, ttLogger)
	if err != nil {
		t.Errorf("err: %v", err)
		t.SkipNow()
	}

	wg.Add(1)
	go WatchLoop(
		ctx,
		ttLogger,
		client.Client,
		"foo",
		0,
		func(ctx context.Context, ev *clientv3.Event) error {
			fmt.Println(ev.Type)
			return nil
		},
	)

	go func() {
		for {
			select {
			case <-time.After(5 * time.Second):
				cancel()
			}
		}
	}()

	wg.Wait()
	fmt.Println("TestWatchLoop exit")
}

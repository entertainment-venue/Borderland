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

package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/entertainment-venue/sm/pkg/apputil"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type shardServer struct {
	cr *serverContainer

	lg *zap.Logger
}

type addSpecRequest struct {
	// 目前app的spec更多承担的是管理职能，shard配置的一个起点，先只配置上service，可以唯一标记一个app
	Service string `json:"service"`

	CreateTime int64 `json:"createTime"`
}

func (s *addSpecRequest) String() string {
	b, _ := json.Marshal(s)
	return string(b)
}

func (ss *shardServer) GinAddSpec(c *gin.Context) {
	var req addSpecRequest
	if err := c.ShouldBind(&req); err != nil {
		ss.lg.Error("ShouldBind err", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ss.lg.Info("receive request", zap.String("request", req.String()))

	//  写入app spec和app task节点在一个tx
	var (
		nodes  []string
		values []string
	)
	nodes = append(nodes, ss.cr.ew.nodeAppSpec(req.Service))
	nodes = append(nodes, ss.cr.ew.nodeAppTask(req.Service))
	values = append(values, req.String())
	values = append(values, "")
	if err := ss.cr.Client.CreateAndGet(context.Background(), nodes, values, clientv3.NoLease); err != nil {
		ss.lg.Error("CreateAndGet err",
			zap.Error(err),
			zap.Strings("nodes", nodes),
			zap.Strings("values", values),
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}

type addShardRequest struct {
	ShardId string `json:"shardId"`

	// 为哪个业务app增加shard
	Service string `json:"service"`

	// 业务app自己定义task内容
	Task string `json:"task"`
}

func (r *addShardRequest) String() string {
	b, _ := json.Marshal(r)
	return string(b)
}

func (ss *shardServer) GinAddShard(c *gin.Context) {
	var req addShardRequest
	if err := c.ShouldBind(&req); err != nil {
		ss.lg.Error("ShouldBind err", zap.Error(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ss.lg.Info("receive request", zap.String("request", req.String()))

	spec := apputil.ShardSpec{
		Service:    req.Service,
		Task:       req.Task,
		UpdateTime: time.Now().Unix(),
	}

	// 区分更新和添加
	// 如果是添加，等待负责该app的shard做探测即可
	// 如果是更新，shard是不允许更新的，这种更新的相当于shard工作内容的调整
	var (
		nodes  = []string{apputil.EtcdPathAppShardId(req.Service, req.ShardId)}
		values = []string{spec.String()}
	)
	if err := ss.cr.Client.CreateAndGet(context.Background(), nodes, values, clientv3.NoLease); err != nil {
		ss.lg.Error("CreateAndGet err",
			zap.Error(err),
			zap.Strings("nodes", nodes),
			zap.Strings("values", values),
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{})
}

/*
type appDelShardRequest struct {
	ShardId string `json:"shardId"`
	Service string `json:"service"`
}

func (g *shardServer) GinAppDelShard(c *gin.Context) {
	var req appDelShardRequest
	if err := c.ShouldBind(&req); err != nil {
		Logger.Printf("err: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	Logger.Printf("req: %v", req)

	resp, err := g.cr.ew.get(context.Background(), g.cr.ew.nodeAppShardId(req.Service, req.ShardId), nil)
	if err != nil {
		Logger.Printf("err: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if resp.Count == 0 {
		err = errors.Errorf("Failed to get serverShard %s content in service %s", req.ShardId, req.Service)
		Logger.Printf("err: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var spec shardSpec
	if err := json.Unmarshal(resp.Kvs[0].Value, &spec); err != nil {
		Logger.Printf("err: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !spec.Deleted {
		spec.Deleted = true

		if err := g.cr.ew.update(context.Background(), g.cr.ew.nodeAppShardId(req.Service, req.ShardId), spec.String()); err != nil {
			Logger.Printf("err: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{})
}
*/

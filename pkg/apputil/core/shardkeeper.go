package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/entertainment-venue/sm/pkg/apputil/storage"
	"github.com/entertainment-venue/sm/pkg/commonutil"
	"github.com/entertainment-venue/sm/pkg/etcdutil"
	"github.com/entertainment-venue/sm/pkg/logutil"
	"github.com/pkg/errors"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

type ShardPrimitives interface {
	Add(id string, spec *storage.ShardSpec) error
	Drop(id string) error
}

const (
	// rebalanceTrigger shardKeeper.rbTrigger 使用
	rebalanceTrigger = "rebalanceTrigger"

	// defaultSyncInterval boltdb同步到app的周期
	defaultSyncInterval = 300 * time.Millisecond
)

type Assignment struct {
	// Drops v1版本只存放要干掉哪些，add仍旧由smserver在guard阶段下发
	Drops []string `json:"drops"`
}

type ShardLease struct {
	storage.Lease

	// GuardLeaseID 不是clientv3.NoLease ，代表是bridge阶段，且要求本地shard的lease属性是该值
	GuardLeaseID clientv3.LeaseID `json:"guardLeaseID"`

	// BridgeLeaseID 在guard阶段，要求本地的bridge是这个值
	BridgeLeaseID clientv3.LeaseID `json:"bridgeLeaseID"`

	// Assignment 包含本轮需要drop掉的shard
	Assignment *Assignment `json:"assignment"`
}

func (sl *ShardLease) String() string {
	b, _ := json.Marshal(sl)
	return string(b)
}

// ShardKeeper 参考raft中log replication节点的实现机制，记录日志到boltdb，开goroutine异步下发指令给调用方
type ShardKeeper struct {
	stopper *commonutil.GoroutineStopper

	// storage 持久存储
	storage storage.Storage

	// rbTrigger rb事件log，按顺序单goroutine处理lease节点的event
	rbTrigger commonutil.Trigger

	// initialized 第一次sync，需要无差别下发shard
	initialized bool

	// startRev 记录lease节点的rev，用于开启watch goroutine
	startRev int64
	// bridgeLease acquireBridgeLease 赋值，当前bridge lease，rb阶段只需要拿到第一次通过etcd下发的lease，没有lease续约的动作
	bridgeLease *storage.Lease
	// guardLease acquireGuardLease 赋值，当前guard lease，成功时才能赋值，直到下次rb
	guardLease *storage.Lease

	containerOpts *ShardKeeperOptions
}

// ShardKeeperOptions copy from containerOptions
type ShardKeeperOptions struct {
	Service          string
	ContainerId      string
	DropExpiredShard bool
	Client           etcdutil.EtcdWrapper
	ShardDir         string
	AppShardImpl     ShardPrimitives
}

func NewShardKeeper(opts *ShardKeeperOptions, st storage.Storage) (*ShardKeeper, error) {
	sk := ShardKeeper{
		containerOpts: opts,
		storage:       st,

		stopper:     &commonutil.GoroutineStopper{},
		bridgeLease: storage.NoLease,
		guardLease:  storage.NoLease,
	}

	sk.rbTrigger, _ = commonutil.NewTrigger(commonutil.WithWorkerSize(1))
	sk.rbTrigger.Register(rebalanceTrigger, sk.handleRbEvent)

	// 标记本地shard的Disp为false，等待参与rb，或者通过guard lease对比直接参与
	if err := sk.storage.Reset(); err != nil {
		logutil.Error(
			"Reset error",
			zap.String("service", sk.containerOpts.Service),
			zap.Error(err),
		)
		return nil, err
	}

	leasePfx := etcdutil.LeasePath(sk.containerOpts.Service)
	gresp, err := sk.containerOpts.Client.Get(context.Background(), leasePfx, clientv3.WithPrefix())
	if err != nil {
		return nil, errors.Wrap(err, "")
	}
	if gresp.Count == 0 {
		// 没有lease/guard节点，当前service没有被正确初始化
		logutil.Error(
			"guard lease not exist",
			zap.String("leasePfx", leasePfx),
			zap.Error(commonutil.ErrNotExist),
		)
		return nil, errors.Wrap(commonutil.ErrNotExist, "")
	}
	if gresp.Count == 1 {
		// 存在历史revision被compact的场景，所以可能watch不到最后一个event，这里通过get，防止miss event
		var lease ShardLease
		if err := json.Unmarshal(gresp.Kvs[0].Value, &lease); err != nil {
			return nil, errors.Wrap(err, "")
		}
		if !sk.containerOpts.DropExpiredShard {
			// 默认client不开启，降低client对lease的敏感度：
			// 1 server长时间不刷新lease
			// 2 网络问题或etcd异常会导致lease失效
			// 以上场景，会导致shard停止服务，不开启的情况下，会使用旧的guard lease继续运行
			sk.guardLease = &lease.Lease

			logutil.Info(
				"ignore guard lease expire status",
				zap.String("service", sk.containerOpts.Service),
				zap.Int64("guard-lease", int64(lease.ID)),
			)
		} else {
			// 判断lease的合法性，expire后续会废弃掉，统一通过etcd做lease合法性校验
			clientv3Lease := clientv3.NewLease(sk.containerOpts.Client.GetClient().Client)
			ctx, cancel := context.WithTimeout(context.TODO(), etcdutil.DefaultRequestTimeout)
			res, err := clientv3Lease.TimeToLive(ctx, lease.ID)
			cancel()
			if err != nil {
				logutil.Error(
					"guard lease fetch error",
					zap.String("service", sk.containerOpts.Service),
					zap.Int64("guard-lease", int64(lease.ID)),
					zap.Error(err),
				)
			} else {
				if res.TTL <= 0 {
					logutil.Warn(
						"guard lease expired, shards will be dropped",
						zap.String("service", sk.containerOpts.Service),
						zap.Int64("guard-lease", int64(lease.ID)),
					)
				} else {
					sk.guardLease = &lease.Lease
					logutil.Info(
						"guard lease not expired",
						zap.String("service", sk.containerOpts.Service),
						zap.Int64("guard-lease", int64(lease.ID)),
					)
				}
			}
		}
	}
	sk.startRev = gresp.Header.Revision + 1

	// 启动同步goroutine，对shard做move动作
	sk.stopper.Wrap(func(ctx context.Context) {
		commonutil.SequenceTickerLoop(
			ctx,
			commonutil.LogErrFunc,
			defaultSyncInterval,
			fmt.Sprintf("sync exit %s", sk.containerOpts.Service),
			func(ctx context.Context) error {
				return sk.sync()
			},
		)
	})

	return &sk, nil
}

// WatchLease 监听lease节点，及时参与到rb中
func (sk *ShardKeeper) WatchLease() {
	leasePfx := etcdutil.LeasePath(sk.containerOpts.Service)
	sk.stopper.Wrap(
		func(ctx context.Context) {
			etcdutil.WatchLoop(
				ctx,
				sk.containerOpts.Client,
				leasePfx,
				sk.startRev,
				func(ctx context.Context, ev *clientv3.Event) error {
					return sk.rbTrigger.Put(&commonutil.TriggerEvent{Key: rebalanceTrigger, Value: ev})
				},
			)
		},
	)
}

func (sk *ShardKeeper) handleRbEvent(_ string, value interface{}) error {
	ev, ok := value.(*clientv3.Event)
	if !ok {
		return errors.New("type error")
	}
	key := string(ev.Kv.Key)

	lease, err := sk.parseShardLease(ev)
	if err != nil {
		logutil.Error(
			"parseShardLease error",
			zap.Error(err),
		)
		return err
	}
	logutil.Info(
		"receive rb event",
		zap.String("key", key),
		zap.Reflect("lease", lease),
		zap.Int32("type", int32(ev.Type)),
	)

	switch key {
	case etcdutil.LeaseBridgePath(sk.containerOpts.Service):
		if err := sk.acquireBridgeLease(ev, lease); err != nil {
			logutil.Error(
				"acquireBridgeLease error",
				zap.String("key", key),
				zap.Reflect("lease", lease),
				zap.Error(err),
			)
			return nil
		}
	case etcdutil.LeaseGuardPath(sk.containerOpts.Service):
		if err := sk.acquireGuardLease(ev, lease); err != nil {
			logutil.Error(
				"acquireGuardLease error",
				zap.String("key", key),
				zap.Reflect("lease", lease),
				zap.Error(err),
			)
			return nil
		}
	default:
		if !strings.HasPrefix(key, etcdutil.LeaseSessionDir(sk.containerOpts.Service)) {
			return errors.Errorf("unexpected key [%s]", key)
		}
		return sk.handleSessionKeyEvent(ev)
	}
	return nil
}

func (sk *ShardKeeper) parseShardLease(ev *clientv3.Event) (*ShardLease, error) {
	var value []byte
	if ev.Type == mvccpb.DELETE {
		value = ev.PrevKv.Value
	} else {
		value = ev.Kv.Value
	}
	var lease ShardLease
	if err := json.Unmarshal(value, &lease); err != nil {
		return nil, errors.Wrap(err, "")
	}
	return &lease, nil
}

func (sk *ShardKeeper) handleSessionKeyEvent(ev *clientv3.Event) error {
	switch ev.Type {
	case mvccpb.PUT:
		k := ev.Kv.Key
		v := ev.Kv.Value
		if ev.IsCreate() {
			logutil.Info(
				"lease session receive create event, ignore",
				zap.String("service", sk.containerOpts.Service),
				zap.ByteString("key", k),
				zap.ByteString("value", v),
			)
			return nil
		}
		if ev.IsModify() {
			logutil.Info(
				"lease session receive modify event, ignore",
				zap.String("service", sk.containerOpts.Service),
				zap.ByteString("key", ev.Kv.Key),
				zap.ByteString("value", ev.Kv.Value),
			)
			return nil
		}
	case mvccpb.DELETE:
		k := ev.PrevKv.Key
		v := ev.PrevKv.Value
		var lease storage.Lease
		if err := json.Unmarshal(v, &lease); err != nil {
			panic(fmt.Sprintf("key [%s] receive delete event, Unmarshal error [%s] with value [%s]", string(k), err.Error(), string(v)))
		}
		if err := sk.storage.DropByLease(false, lease.ID); err != nil {
			return err
		}
	default:
		panic(fmt.Sprintf("unexpected event type [%s] for lease [%s] at [%s]", ev.Type, string(ev.Kv.Value), string(ev.Kv.Key)))
	}
	return nil
}

func (sk *ShardKeeper) acquireBridgeLease(ev *clientv3.Event, lease *ShardLease) error {
	key := string(ev.Kv.Key)

	// bridge不存在修改场景
	if ev.IsModify() {
		err := errors.Errorf("unexpected modify event, key %s", string(ev.Kv.Key))
		return errors.Wrap(err, "")
	}

	if ev.Type == mvccpb.DELETE {
		if err := sk.storage.DropByLease(false, lease.ID); err != nil {
			return err
		}
		logutil.Info(
			"drop bridge lease completed",
			zap.String("pfx", key),
			zap.Int64("lease", int64(lease.ID)),
		)
		return nil
	}

	// reset bridge lease，清除 shardKeeper 当前的临时变量，方便开启新的rb
	sk.bridgeLease = storage.NoLease

	if err := sk.storage.Drop(lease.Assignment.Drops); err != nil {
		return err
	}

	if err := sk.storage.MigrateLease(lease.GuardLeaseID, lease.ID); err != nil {
		return err
	}

	sk.bridgeLease = &lease.Lease
	logutil.Info(
		"bridge: create success",
		zap.String("key", key),
		zap.Reflect("bridge-lease", lease),
	)
	return nil
}

func (sk *ShardKeeper) acquireGuardLease(ev *clientv3.Event, lease *ShardLease) error {
	// guard处理创建场景，等待下一个event，smserver保证rb是由modify触发
	if ev.IsCreate() {
		return errors.Errorf(
			"guard node should be created before shardkeeper started, key [%s]",
			string(ev.Kv.Key),
		)
	}

	key := string(ev.Kv.Key)

	// 非renew场景，肯定是在rb中，所以当前的bridgeLease需要存在值
	if sk.bridgeLease.EqualTo(storage.NoLease) {
		return errors.Errorf(
			"bridge lease is zero, can not participating rb, key [%s]",
			key,
		)
	}

	// 兼容现存的rb提供的lease信息
	if lease.BridgeLeaseID > 0 && sk.bridgeLease.ID != lease.BridgeLeaseID {
		return errors.Errorf(
			"bridge lease not match, key [%s] expect [%d] actual [%d]",
			string(ev.Kv.Key),
			lease.BridgeLeaseID,
			sk.bridgeLease.ID,
		)
	}

	defer func() {
		// 清理bridge，不管逻辑是否出错
		sk.bridgeLease = storage.NoLease
	}()

	// 预先设定guardLease，boltdb的shard逐个过度到guardLease下
	sk.guardLease = &lease.Lease

	// 每个shard的lease存在下面3种状态：
	// 1 shard的lease和guard lease相等，shard分配有效，什么都不用做
	// 2 shard拿着bridge lease，可以直接使用guard lease做更新，下次hb会带上给smserver
	// 3 shard没有bridge lease，shard分配无效，删除，应该只在节点挂掉一段时间后，才可能出现
	if err := sk.storage.MigrateLease(sk.bridgeLease.ID, lease.ID); err != nil {
		return err
	}

	if err := sk.storage.DropByLease(true, lease.ID); err != nil {
		return err
	}

	logutil.Info(
		"guard lease update success",
		zap.String("key", key),
		zap.Reflect("guardLease", sk.guardLease),
	)

	// 存储和lease的关联节点
	sessionPath := etcdutil.LeaseSessionPath(sk.containerOpts.Service, sk.containerOpts.ContainerId)
	leaseIDStr := strconv.FormatInt(int64(sk.guardLease.ID), 10)
	if _, err := sk.containerOpts.Client.Put(context.TODO(), sessionPath, sk.guardLease.String(), clientv3.WithLease(sk.guardLease.ID)); err != nil {
		logutil.Error(
			"Put error",
			zap.String("session-path", sessionPath),
			zap.String("guard-lease-id", leaseIDStr),
			zap.Error(err),
		)
	}

	return nil
}

func (sk *ShardKeeper) Add(id string, spec *storage.ShardSpec) error {
	// 提前判断添加shard场景下的细节，让storage内部逻辑尽量明确
	if !spec.Lease.EqualTo(sk.guardLease) {
		logutil.Warn(
			"shard guard lease not equal with guard lease",
			zap.String("service", sk.containerOpts.Service),
			zap.String("shard-id", id),
			zap.Int64("local-guard-lease", int64(sk.guardLease.ID)),
			zap.Int64("shard-guard-lease", int64(spec.Lease.ID)),
		)
		return errors.New("lease mismatch")
	}
	return sk.storage.Add(spec)
}

func (sk *ShardKeeper) Drop(id string) error {
	return sk.storage.Drop([]string{id})
}

// sync 没有关注lease，boltdb中存在的就需要提交给app
func (sk *ShardKeeper) sync() error {
	var (
		dropShardIDs   []string
		updateDbValues = make(map[string]*storage.ShardKeeperDbValue)
	)

	dropFn := func(dv *storage.ShardKeeperDbValue) error {
		err := sk.containerOpts.AppShardImpl.Drop(dv.Spec.Id)
		if err == nil || err == commonutil.ErrNotExist {
			// 清理掉shard
			dropShardIDs = append(dropShardIDs, dv.Spec.Id)
			return nil
		}
		logutil.Error(
			"drop shard failed",
			zap.String("service", sk.containerOpts.Service),
			zap.String("shardId", dv.Spec.Id),
			zap.Error(err),
		)
		return err
	}

	addFn := func(dv *storage.ShardKeeperDbValue) error {
		err := sk.containerOpts.AppShardImpl.Add(dv.Spec.Id, dv.Spec)
		if err == nil || err == commonutil.ErrExist {
			// 下发成功后更新boltdb
			dv.Disp = true
			updateDbValues[dv.Spec.Id] = dv
			return nil
		}
		logutil.Error(
			"add shard failed",
			zap.String("service", sk.containerOpts.Service),
			zap.String("shardId", dv.Spec.Id),
			zap.Error(err),
		)
		return err
	}

	sk.storage.ForEach(func(shardID string, dv *storage.ShardKeeperDbValue) error {
		// shard的lease一定和guardLease是相等的才可以下发
		/*
			这种要求shardkeeper下发shard的情况，有两个通道：
			1. 从http add请求
			2. watch lease，发现需要drop（不会走到问题逻辑）
			1这种情况，sm在guardlease的更新和http请求下发之间停10s，等待client同步，然后下发，如果10s这个问题client都没同步到最新的guardlease，drop即可
		*/
		if !dv.Spec.Lease.EqualTo(sk.guardLease) && !dv.Spec.Lease.EqualTo(sk.bridgeLease) {
			logutil.Warn(
				"unexpected lease, will be dropped",
				zap.Reflect("dv", dv),
				zap.Reflect("guardLease", sk.guardLease),
			)
			return dropFn(dv)
		}

		if dv.Disp && sk.initialized {
			return nil
		}

		if dv.Drop {
			logutil.Info(
				"drop shard from app",
				zap.String("service", sk.containerOpts.Service),
				zap.Reflect("shard", dv),
			)
			return dropFn(dv)
		}

		logutil.Info(
			"add shard to app",
			zap.String("service", sk.containerOpts.Service),
			zap.Reflect("shard", dv),
		)
		return addFn(dv)
	})

	for _, shardID := range dropShardIDs {
		if err := sk.storage.Remove(shardID); err != nil {
			return err
		}
	}

	for shardID, dv := range updateDbValues {
		dv.Disp = true
		if err := sk.storage.Put(shardID, dv); err != nil {
			return err
		}
	}

	// 整体sync一遍，才进入运行时根据Disp属性选择同步状态
	if !sk.initialized {
		sk.initialized = true
	}
	return nil
}

func (sk *ShardKeeper) Close() {
	if sk.stopper != nil {
		sk.stopper.Close()
	}

	if sk.rbTrigger != nil {
		sk.rbTrigger.Close()
	}

	if sk.storage != nil {
		sk.storage.Close()
	}

	logutil.Info(
		"active closed",
		zap.String("service", sk.containerOpts.Service),
	)
}

func (sk *ShardKeeper) Storage() storage.Storage {
	return sk.storage
}

# Flash-Sale 改进计划

> 目标：在现有秒杀系统基础上做一组「能写进简历、经得起面试深挖」的改进。
> 原则：**每个亮点 = 一个真实解决的问题**，能讲完整「问题现象 → 排查过程 → 方案权衡 → 量化结果」。
> 创建日期：2026-09-29

## 改进总览

| # | 改进 | 维度 | 优先级 | 预估工作量 |
|---|------|------|--------|-----------|
| 1 | 消息可靠投递 + 消费端重试/DLQ + 对账 | 可靠性 | ⭐⭐⭐ | 1 天 |
| 2 | AMQP channel 池化 + pprof 性能优化 | 性能 | ⭐⭐⭐ | 1~2 天 |
| 3 | 超时释放购买资格 + 库存来源合理化 | 业务正确性 | ⭐⭐⭐ | 半天 |
| 4 | 消费者限流（prefetch + worker 池） | 并发控制 | ⭐⭐ | 半天 |
| 5 | 入口限流 + 售罄本地标记 | 高并发优化 | ⭐⭐ | 1 天 |
| 6 | 优雅停机 | 工程化 | ⭐ | 半天 |
| 7 | Prometheus 可观测性 | 工程化 | ⭐ | 半天 |

**建议组合：做 1 + 2 + 3，再从 4/5 中选一个。**
四个点覆盖「可靠性 / 性能 / 业务正确性 / 并发控制」四个维度，简历上正好凑成一组有层次的 bullet。别贪多——4 个能讲透的改进 > 8 个说不清的。

---

## 第一梯队：修复硬伤（不做会被问倒，做了反而是亮点）

### 改进 1：消息可靠投递 + 消费端重试兜底 + 定时对账 ⭐

**现状问题**

- `internal/service/workflow/reservation_workflow.go:33-46`：先发即时消息、再发延时消息。若第二条发送失败，订单永远卡在 `RESERVED`，**库存永久泄漏**。
- `internal/service/workflow/payment_workflow.go:78`：失败时 `Nack(false, true)` 无限 requeue，一条坏消息会打爆消费者。
- 消息可能丢失的环节均无防护：生产者 → MQ（网络抖动）、MQ → 消费者（消费者宕机）、消费中（处理失败）。

**改进方案**

1. 生产者开启 publisher confirm，发送失败时补偿重发或回滚 Redis 订单。
2. 消费端记录重试次数（消息头 `x-retry-count`），超过 3 次 `Nack(false, false)` 进入死信队列。
3. 新增对账任务（goroutine + ticker）：定期扫描 Redis 中长时间停留的 `RESERVED` 订单，与 DB 比对后补偿处理。

**涉及文件**

- `internal/mq/producer.go`、`internal/mq/rabbitmq.go`（confirm 机制、DLQ 声明）
- `internal/service/workflow/payment_workflow.go`、`order_workflow.go`（重试计数）
- 新增 `internal/service/workflow/reconcile_workflow.go`（对账任务）

**简历写法**

> 设计可靠消息投递机制（publisher confirm + 消费幂等 + 死信兜底 + 定时对账），消除订单状态滞留与库存泄漏场景。

**面试追问准备**

- 消息丢失有哪几个环节？各自怎么防？
- publisher confirm 和 RabbitMQ 事务的区别？为什么 confirm 性能好？
- 对账任务扫 Redis 还是扫 DB？扫描粒度怎么定？
- at-least-once + 幂等 vs 事务消息，各自适用场景？

**验收标准**

- [ ] 杀掉 MQ 容器重启后，期间产生的订单不丢失、状态最终一致
- [ ] 注入一条必失败的消息，验证重试 3 次后进入 DLQ，消费者不 hang
- [ ] 对账任务能发现人工构造的滞留订单并补偿

---

### 改进 2：AMQP channel 池化 + pprof 性能优化 ⭐ 最好的「性能故事」

**现状问题**

- `internal/service/workflow/reservation_workflow.go:28`：每次订票都 `mq.NewChannel`——一次网络往返，且 channel 从不关闭（**泄露**）。
- 高并发链路里这是肉眼可见的开销，也是 QPS 瓶颈之一。

**改进方案**

1. 接入 `net/http/pprof`，用 `go test -bench` 或现有并发测试压出**优化前基线**（QPS、p99 延迟、火焰图）。
2. channel 池化：`sync.Pool` 或带缓冲 channel 实现的对象池，用后即还；注意 amqp channel **不是并发安全的**，池化天然规避共享问题。
3. 再次压测，对比火焰图与 QPS，记录数据。

**涉及文件**

- `internal/mq/rabbitmq.go`（新增 channel 池）
- `internal/service/workflow/reservation_workflow.go`（改为从池中取/还）
- `cmd/flash-sale/main.go`（挂载 pprof 路由）

**简历写法**

> 通过 pprof 火焰图定位到每请求创建 AMQP channel 的性能瓶颈，池化改造后 QPS 从 X 提升至 Y（+Z%），p99 延迟从 A ms 降至 B ms。

**面试追问准备**

- channel 为什么贵？（每次创建是网络往返 + 服务端分配资源）
- 连接复用但 channel 为什么不能无界共享？（channel 非并发安全，且服务端有 channelMax 上限）
- pprof 怎么用？火焰图怎么读？还排查过什么类型的瓶颈？
- `sync.Pool` 的特性？（GC 会清空、无界时的权衡）

**验收标准**

- [ ] 有优化前后的压测数据对比（写入 `test_result/`）
- [ ] 火焰图中 channel 创建开销消失或占比显著下降
- [ ] 并发测试三个场景仍全部通过

> ⚠️ 注意：**先做这项拿到基线数据**，再改其他东西，否则基线测不准。

---

### 改进 3：超时释放购买资格 + 库存来源合理化

**现状问题**

- `internal/cache/constants.go` 的 `markTicketAsTimeoutScript`：回滚库存但不删除 `user:{id}:showtime:{id}:ordered` 标记 → 用户超时后**永远无法再买该场次**。
- `internal/app/app.go:78`：每个场次硬编码 100 张票，而非读取 DB 中的真实座位数。

**改进方案**

1. 超时 Lua 脚本中补 `DEL userOrderedKey`（需要把 user_id 一并传入或从 reservation hash 中读取）。
2. 启动时从 showtime 表/座位表读取真实库存初始化 Redis。

**涉及文件**

- `internal/cache/constants.go`（改 `markTicketAsTimeoutScript`）
- `internal/cache/redis.go`（`MarkTicketAsTimeout` 传参）
- `internal/app/app.go`（库存初始化来源）

**简历写法**

> 修复订单超时取消后用户无法重新购买的业务缺陷，保证库存与用户状态的原子回滚。

**面试追问准备**

- 超时 Lua 与支付 Lua 并发执行会怎样？（Redis 单线程串行 + 状态前置校验，先到者赢）
- 为什么回滚库存和释放资格必须在同一个 Lua 里？（非原子会出现「资格已释放但库存未回滚」的中间态）

**验收标准**

- [ ] 订单超时后同一用户可重新购买该场次 —— 代码已实现（commit `3cba8dd`），待部署实测
- [ ] 超时释放库存与释放资格原子完成（并发压测验证）—— 代码已实现（同一 Lua 原子执行），待部署实测

---

## 第二梯队：制造深度（选一个做透）

### 改进 4：消费者限流（prefetch + 固定 worker 池）

**现状问题**

`internal/service/workflow/payment_workflow.go:49`：每条消息 `go func()` 无上限起 goroutine，mock 支付 sleep 最长 1s，洪峰时 goroutine 爆炸，可能打爆 Redis 连接池。

**改进方案**

1. `ch.Qos(prefetchCount, 0, false)` 限制未确认消息数。
2. 固定大小 worker 池（goroutine + 有缓冲 channel 信号量）消费消息。

**面试追问准备**

- backpressure 怎么从 Redis 传导到 MQ？（prefetch 满 → MQ 停止推送 → 消息堆积在队列）
- prefetch 设多大？依据什么？（处理耗时 × 期望并发 / 单条处理时间，需压测定）

---

### 改进 5：入口限流 + 售罄本地标记

**改进方案**

1. 入口加令牌桶限流（`golang.org/x/time/rate`），超限直接 429，保护 Redis。
2. 某场次售罄后在本地内存（`sync.Map`）打标记，后续请求直接拦截不打 Redis；用 Redis pub/sub 广播「库存回滚」事件使各实例本地标记失效。

**面试追问准备**

- 本地标记与 Redis 不一致的窗口期怎么办？（短暂多打几次 Redis 是无害的——Lua 判单兜底；标记只用于优化，不用于正确性）
- 多实例部署时本地缓存怎么同步？（pub/sub 广播，或接受秒级延迟）
- 令牌桶 vs 漏桶？为什么入口选令牌桶？（允许突发）

---

## 第三梯队：工程化加分（锦上添花）

### 改进 6：优雅停机

- `http.Server.Shutdown(ctx)` 处理在途请求
- 停止 MQ 消费（`ch.Cancel`）并等待在途消息处理完
- `signal.NotifyContext` 监听 SIGTERM/SIGINT

### 改进 7：Prometheus 可观测性

- 指标：请求 QPS/延迟直方图、预订成功率、各队列积压量、Redis 操作耗时
- `prometheus/client_golang` + `/metrics` 端点
- 简历关键词：「可观测性」「监控告警」

---

## 实施顺序建议

```text
Day 1:  改进 2（前半）—— 接入 pprof，压测拿基线数据 ⚠️ 必须先做
Day 2:  改进 2（后半）—— channel 池化，对比压测，记录数据
Day 3:  改进 3 —— 超时释放资格 + 库存来源（工作量最小）
Day 4:  改进 1 —— 可靠投递 + DLQ + 对账
Day 5:  改进 4 或 5 —— 二选一做透
```

## 进度追踪

- [x] 改进 1：消息可靠投递 + DLQ + 对账 —— **✅ 完成（2026-10-02）**
  - [x] 第一层：publisher confirm + 失败原子回滚 ✅ 2026-10-02
  - [x] 第二层：幂等消费 + 有限重试（retry 队列 TTL 退避）+ parking-lot 死信 ✅ 2026-10-02
  - [x] 第三层：定时对账任务（SCAN+pipeline，PAID 补写 / 超龄 RESERVED 超时取消；reservation hash 新增 created_at 字段）✅ 2026-10-02
  - ⏳ 待实测验证：docker 环境故障注入（停 MQ 看回滚、停 Redis 看停车场、构造滞留订单看对账补偿）
  - 附带完成：修复 `order_service` 事务 bug（Create 错误未检查 + 未走 WithTx）；修复 reservation workflow 的 channel 泄露；`QueuePurge` 改为队列重建（解决队列参数变更）
- [~] 改进 2：channel 池化 + pprof 优化 —— **代码完成，待压测对比填数据**
  - [x] pprof 端点接入（独立 6060 端口，commit `848e35f` = 基线）
  - [x] ChannelPool 有界复用（commit `b3902d8` = 优化）
  - [ ] 实测：基线 vs 池化的 QPS/p99/火焰图对比（操作手册见 INTERVIEW_NOTES.md 改进 2 节），数据填回简历 Bullet
- [x] 改进 3：超时释放购买资格 + 库存合理化 —— **✅ 完成（2026-10-02）**
  - [x] 超时 Lua 原子释放：状态流转 + 库存回滚 + DEL 已购标记（user_id 从订单 hash 读，调用方零改动）
  - [x] Showtime 新增 TotalTickets 字段，启动时从 DB 读真实库存初始化 Redis（替代硬编码 100）
  - [ ] 待实测验证：订单超时后同一用户重购成功；并发压测下释放原子性
- [ ] 改进 4：消费者限流
- [ ] 改进 5：入口限流 + 售罄本地标记
- [ ] 改进 6：优雅停机
- [ ] 改进 7：Prometheus 监控
- [ ] 更新 README（架构图 + 测试结果）
- [~] 更新简历 bullet，逐条准备面试话术 —— 改进 1（三层）+ 改进 2 + 改进 3 的话术已写入 `INTERVIEW_NOTES.md`；改进 2 简历数据待压测填入

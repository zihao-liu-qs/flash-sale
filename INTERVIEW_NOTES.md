# 面试讲解手册：Flash-Sale 改进记录

> 用途：面试前复习。每个改进按「电梯陈述 → 完整故事 → 设计决策 → 预判追问」组织。
> 原则：能讲出「问题现象 → 排查过程 → 方案权衡 → 量化结果」的完整闭环。

---

# 改进 1：消息可靠投递（第一层：Publisher Confirm + 失败回滚）

## 简历 Bullet

> 定位秒杀系统消息链路的三处订单丢失风险（双写裂缝、发送无确认、失败无回滚），引入 publisher confirm 与 Lua 原子回滚机制，使下单失败场景库存零泄漏、用户可安全重试。

## 30 秒电梯陈述

"我的秒杀系统用 Redis Lua 判单、RabbitMQ 异步处理支付和入库。压测后我 review 消息链路时发现一个问题：**`Publish` 返回成功不代表 broker 真的收到了消息**，而且 Redis 扣库存和发 MQ 消息是两个系统，中间任何一步失败都会导致库存永久泄漏、用户钱票两空。我做了第一层改造：发送端引入 publisher confirm，失败时用 Lua 脚本原子回滚——删订单、还库存、释放购买资格，三步一个原子操作。这样任何消息发送失败，用户都能安全重试。"

## 完整故事线

### 1. 问题怎么发现的

压测通过后我回头梳理一条订单的完整生命周期，逐段检查「消息在哪里可能丢」。发现了三处缺陷：

**缺陷 A：双写裂缝。** 原代码的执行序列是：

```
Redis Lua 成功（票已扣、订单已建） → 建 channel → 发即时消息 → 发延时消息
```

如果发消息失败，直接 `return err`，用户收到 500「请重试」——但 Redis 里票已经扣了、用户已被标记「已购买」。用户按提示重试，却收到「您已购买过该场次」。**票扣了、单没成、还不让再买**。

**缺陷 B：发送无确认。** RabbitMQ 客户端的 `Publish` 返回 nil 只代表「写进了本地 TCP 缓冲区」，不代表 broker 收到。broker 宕机、网络闪断都会静默丢消息。

**缺陷 C：发送顺序埋下竞态。** 原来先发即时消息（触发支付）、再发延时消息（15 分钟超时兜底）。如果即时成功、延时失败，订单永远卡在 RESERVED 状态，没有超时消息来取消它，库存永久泄漏。

### 2. 方案：confirm + 原子回滚

新流程：

```
Redis Lua 成功
  → 发延时消息（confirm，阻塞等 broker 回执）
  → 发即时消息（confirm）
  → 任何一步失败 → cancelReservationScript 原子回滚：
       DEL 订单 hash + INCR 库存 + DEL 用户已购标记
  → 返回 500，用户重试可成功（资格已释放）
```

### 3. 结果

- 消息发送的任何失败场景：库存零泄漏、用户资格被释放、重试即可成功
- 「confirm 后进程崩溃」的场景也能自愈：延时消息 15 分钟后触发 MarkTimeout 回滚库存

---

## 三个关键设计决策（面试官最可能深挖的地方）

### 决策 1：为什么先睡后发——调换两条消息的发送顺序

**决定**：先延时消息、后即时消息（与原实现相反）。

**推理**：即时消息才是「触发订单流转」的那条——它一到消费者，mock 支付几百毫秒内就把订单改成 PAID 了。只要让即时消息**最后**发，那么任何一步失败时，订单都还没有被任何消费者看到过，此时回滚 Redis 永远安全。

**反证**：如果按原顺序，「即时已发、延时失败」时回滚会删掉一个正在支付甚至已支付的订单——为了解决旧问题引入新竞态。

> 💡 这个决策体现的通用原则：**对跨系统操作排序时，让「触发下游副作用」的操作最后执行，前面的失败才能无损回滚。**

### 决策 2：回滚为什么必须幂等

`cancelReservationScript` 第一步是 `EXISTS` 检查：订单不存在就直接返回，**不执行 INCR 库存**。

**为什么**：回滚逻辑本身可能失败重试（比如回滚时 Redis 抖动）。如果没有存在性检查，重试一次库存就多还一张——防超卖的系统反而制造了「超发」。

> 💡 通用原则：**每一个写操作都要问一句——它被执行两次会怎样？** 这个思路贯穿后续所有改进。

### 决策 3：残留消息为什么靠「幂等忽略」而不是「撤回」

场景：延时消息已 confirm、即时消息失败 → 回滚删了订单，但延时消息 15 分钟后**还是会到达**消费者。

**方案**：`markTicketAsTimeoutScript` 对「订单不存在」返回特殊码 -3，消费端视为「已处理」而非失败。

**为什么不撤回消息**：RabbitMQ 不支持撤回已入队的消息；而且延时消息在 15 分钟内不可见、无法精确定位。**让消费端幂等，是 at-least-once 投递模型下唯一的正解**——这也是第二层的核心思想，这里先做了第一次实践。

---

## 预判追问 Q&A

**Q：publisher confirm 和 RabbitMQ 事务（tx）有什么区别？**

A：都能保证消息到达 broker。事务是同步阻塞的，每发一条都要等 broker 提交事务，吞吐差一个数量级；confirm 是异步批量回执，broker 落盘后回调确认，性能好得多。我的场景用 DeferredConfirm 逐条等待，是因为下单请求本身需要同步知道结果来决定是否回滚。

**Q：同步等 confirm，会不会拖累 QPS？**

A：会有一点——每条消息多等一个 broker 回执（毫秒级）。但两点缓解：一是这里用 confirm 换来的是「失败可回滚」的正确性，下单路径值得；二是如果未来要优化，可以改异步 confirm + 本地消息表（outbox），把等待移出请求路径——这是我知道但还没做的权衡。

**Q：回滚失败怎么办？（比如回滚时 Redis 挂了）**

A：订单会滞留 RESERVED。有两层兜底：已发出的延时消息 15 分钟后触发 MarkTimeout 自动回滚库存；另外我规划了对账任务（第三层）定期扫描滞留订单补偿。这正是「兜底思维」——任何单点机制都可能失效，要靠分层防御。

**Q：broker 收到了消息，但 confirm 回执丢了，会怎样？**

A：（诚实回答）生产者误判失败 → 回滚删订单 → 但消息实际已入队 → 消费者支付时发现订单不存在。当前版本这条消息会 requeue——**这正是我做第二层（有限重试 + 死信队列 + 全面幂等消费）的直接动机**。我在第一层 review 时发现了这个边界，说明分层防御的必要性。

**Q：为什么不用本地消息表 / 事务消息一步到位？**

A：本地消息表（outbox）是更强的方案：把「订单 + 待发消息」放在同一个存储里原子写入，再由 relay 投递。但我的判单在 Redis 不在关系库，没有「同事务写消息表」的载体，改造成本大。confirm + 回滚是在现有架构上性价比最高的方案；如果订单主存储在 MySQL，我会选 outbox。**方案选择要匹配架构现状。**

**Q：回滚脚本为什么用 Lua 而不是应用层三次调用？**

A：DEL 订单、INCR 库存、DEL 已购标记必须原子——否则并发下可能出现「资格已释放但库存未还」的中间态，另一个用户此时判单会读到不一致的数据。Redis 单线程执行 Lua，天然是最轻量的原子方案，不需要分布式锁。

---

## 诚实边界（面试时主动讲，加分）

第一层只解决了「生产者 → broker」这一段。以下问题**仍然存在**，是第二层的内容：

1. 消费失败 `Nack(requeue=true)` 无限重试，坏消息空转打满 CPU
2. 支付成功后发「入库消息」失败只打了日志，订单 PAID 但不落库
3. 消费端缺乏全面幂等（上面 Q&A 第 4 条的场景）

> 面试话术："我把消息可靠性拆成三层防御：预防（confirm+回滚）、遏制（有限重试+DLQ）、兜底（对账）。第一层做完后我自己 review 出了剩余边界，这正是第二层的设计输入。"

---

## 关键代码位置（面试白板参考）

| 内容 | 位置 |
|---|---|
| 回滚 Lua 脚本 | `internal/cache/constants.go` — `cancelReservationScript` |
| confirm 发送封装 | `internal/mq/producer.go` — `publishWithConfirm` |
| confirm channel 创建 | `internal/mq/rabbitmq.go` — `NewConfirmingChannel` |
| Reserve 主流程（先睡后发） | `internal/service/workflow/reservation_workflow.go` — `Reserve` |
| 超时幂等忽略（-3） | `internal/cache/constants.go` — `markTicketAsTimeoutScript` |

---

# 改进 1（第二层）：幂等消费 + 有限重试 + 死信兜底

## 简历 Bullet

> 重构 MQ 消费链路为「处理成功才 Ack」模式，基于状态机实现零额外存储的幂等消费；设计「重试队列 TTL 退避 + x-death 计数 + 停车场队列」的有限重试拓扑，消除坏消息无限 requeue 空转与支付成功但订单不落库的数据丢失。

## 30 秒电梯陈述

"第一层做完后我 review 出了剩余的两个风险：一是消费失败 `Nack(requeue=true)` 会让坏消息以每秒数千次空转；二是更严重——支付成功后发入库消息失败只打了行日志，而支付消息已经 Ack 了，订单会是 PAID 但永远不进数据库。第二层我把消费流程重排为「MarkPaid → 发入库消息(confirm) → 全部成功才 Ack」，这要求 MarkPaid 幂等——我没有引入任何额外存储，而是让状态机自己判断：已是 PAID 说明上次死在发消息，直接补发。重试也不是无脑 requeue，而是走「重试队列 TTL 10 秒退避 + broker 自动维护的 x-death 计数，3 次失败进停车场队列人工处理」。"

## 完整故事线

### 1. 问题怎么发现的

第一层 review 时自问「confirm 回执丢失会怎样」，顺着这个思路把消费端三段代码全部过了一遍，发现三个问题：

**问题 A（最严重）：支付成功但订单不落库。** 原代码顺序是「Ack 支付消息 → 发入库消息」，发送失败只 `log.Printf`。消息已 Ack 没有任何重试机会，**订单 PAID 但 PostgreSQL 里永远没有它**——Redis 与 DB 永久不一致。

**问题 B：坏消息热循环。** 三处消费者失败都是 `Nack(false, true)` 立即 requeue。DB 宕机 5 分钟，一条消息就空转 5 分钟，CPU 打满、日志爆炸。

**问题 C：消费端无幂等。** 任何对「不存在订单」的 MarkPaid 都返回错误 → requeue → 死循环（第一层的回滚让这个场景真实存在了）。

**附带发现**：review `order_service.go` 时发现入库代码 `s.Repo.Create(...)` **返回值没检查、且没走 `WithTx(tx)`**——入库失败被吞掉返回成功，消息被 Ack，订单丢失。一个隐藏的深度 bug。

### 2. 方案

```
消费支付消息
  → MarkPaid（幂等化：返回 PaidNew / PaidAlready / PaidNotFound）
      ├─ PaidNotFound（订单已回滚）→ 直接 Ack 忽略
      └─ PaidNew / PaidAlready → 发入库消息（confirm + 串行化发送）
  → 全部成功才 Ack
  → 任一步失败 → Nack 进重试拓扑：
      main 队列 ──Nack──► retry 交换机 ──► retry 队列（TTL 10s）──过期──► 回旋 main 队列
      x-death 计数 ≥ 3 ──► 应用层投递到停车场队列 + Ack（人工处理）
```

### 3. 结果

- 坏消息最多重试 3 次、每次间隔 10 秒，之后进停车场——消费者永不空转
- 支付消息在「入库消息确认送达」前不会被 Ack，PAID 但不落库的场景被消灭
- 重试安全：MarkPaid 幂等、入库幂等（订单号 = DB 主键，查重 + 冲突兜底）

---

## 关键设计决策

### 决策 1：流程重排引出的「幂等连锁反应」

把「发入库消息」移到 Ack 之前，会产生一个连锁问题：**重试时 MarkPaid 必然失败**——上次已经把它改成 PAID 了，`status ~= "RESERVED"` 校验拒绝。所以幂等不是可选项，是重排的必要前提。

**解法**：不引入额外存储（比如「已发送消息记录表」），而是让 Lua 区分三种语义：

```
1  = 新支付成功        → 发入库消息
0  = 已是 PAID（幂等）→ 上次死在发消息，补发
-3 = 订单不存在       → 已回滚的残留消息，忽略
-2 = TIMEOUT 等       → 真失败，走重试
```

> 💡 通用原则：**at-least-once 投递下，幂等是消费者的义务而非可选项；最好的幂等判断是利用业务状态机本身，而不是另建一套去重存储。**

### 决策 2：为什么重试用 broker 拓扑而不是应用层 sleep 重试

| | 应用层 sleep 重试 | retry 队列 + TTL（本方案） |
|---|---|---|
| 失败期间 | 当前消息**阻塞消费循环**，后面的消息排队等 | 消息离开 main 队列，**不阻塞**其他消息 |
| 计次状态 | 应用内存，重启丢失 | broker 的 `x-death` header **自动维护** |
| 退避策略 | 自己写 | TTL 天然就是退避 |

`x-death` 是 RabbitMQ 自动写入的 header，记录消息每次死信化的 queue/reason/count——读它就知道重试了几次，零额外状态。

**为什么 parking lot 是应用层 publish 而不是纯拓扑**：一个队列只能配一个 DLX，「再试一次」和「进停车场」两条路无法靠拓扑分流，所以消费端读 x-death 计数自己判断——< 3 就 Nack 走 DLX 重试，≥ 3 就主动投递到停车场再 Ack。

### 决策 3：publish 串行化

支付消费者是「每条消息一个 goroutine」，并发 publish 共享 channel 有两个问题：amqp channel **不是并发安全的**；confirm 模式下 DeferredConfirm 按 delivery tag 匹配回执，并发发布容易错乱。解法：workflow 持有一个 confirm channel + `sync.Mutex`，发送全部串行。代价极小——mock 支付的 sleep 在锁外，锁内只有毫秒级的发送。

### 附带修复：order_service 事务 bug

`CreateOrderFromReservation` 原来：① `s.Repo.Create(...)` 返回值没检查——入库失败被吞，消息 Ack，订单丢失；② 用的是 `s.Repo`（绑定 `s.DB`）而非 `WithTx(tx)`——事务根本没包住写入。修复后：GetByID 区分 `ErrRecordNotFound` 与真实 DB 错误、Create 走 tx 且错误上抛。**这个 bug 不改，第二层的「入库失败重试」就是摆设。**

---

## 预判追问 Q&A

**Q：x-death 具体长什么样？count 怎么累计？**

A：消息每次死信化，broker 往 header 的 `x-death` 数组写入 `{queue, reason, count, time, exchange, routing-keys}`，按 (queue, reason) 去重累加 count。我的消息会留两条记录：main 队列的 `rejected`（Nack 产生）和 retry 队列的 `expired`（TTL 产生）。取最大 count 作为重试次数。

**Q：重试会不会导致消息乱序？会有问题吗？**

A：会乱序，但本场景没有顺序依赖：每条消息操作不同的 reservationID，互不相关。同一订单的支付消息和超时消息被 15 分钟 TTL 天然隔开；万一支付重试撞上超时到达，Lua 状态机前置校验保证先到者赢——**顺序性焦虑最终是靠状态机而不是队列顺序来解决的**。

**Q：retry TTL 为什么是 10 秒？**

A：权衡故障恢复时间和处理延迟。太短（1s）退避失去意义，DB 抖动的 5 分钟里还是重试几十次；太长（1min）正常抖动恢复后用户感知延迟大。10s × 3 次 = 最坏 30 秒进停车场，对 demo 和多数生产场景都合理。真实系统会配指数退避（多个 TTL 递增的 retry 队列）。

**Q：停车场队列里的消息怎么处理？**

A：目前人工检查（demo 里用管理台）。生产做法是挂告警（队列深度 > 0 就报警）+ 补偿任务或人工后台重放。关键是坏消息**不会丢也不会空转**——它安静地躺在那里等人处理。

**Q：听说过 quorum queue 的 delivery-limit 吗？**

A：（加分项）RabbitMQ 3.8+ 的 quorum queue 原生支持 `x-delivery-limit`，超过投递次数自动死信，不需要应用层读 x-death。我没用是因为 classic queue + x-death 方案兼容性更广，而且这个机制自己实现一遍理解更深——面试里我能讲清它每一步在干什么。

**Q：消费者多实例部署会怎样？**

A：天然负载均衡——RabbitMQ 轮询分发消息给各实例，幂等设计保证竞争安全。这也是为什么幂等必须做：多实例下重复投递概率更高。

---

## 关键代码位置

| 内容 | 位置 |
|---|---|
| MarkPaid 幂等 Lua（1/0/-2/-3） | `internal/cache/constants.go` — `markTicketAsPaidScript` |
| 重试拓扑声明 | `internal/mq/rabbitmq.go` — `SetupRetryableQueue` |
| x-death 计数 + 有限重试 | `internal/mq/consumer.go` — `DeadLetterCount` / `NackWithRetry` |
| 支付消费重排（核心） | `internal/service/workflow/payment_workflow.go` — `handlePaymentMessage` |
| 事务 bug 修复 | `internal/service/domain/order_service.go` — `CreateOrderFromReservation` |

---

# 改进 1（第三层）：定时对账——分层防御的兜底

## 简历 Bullet

> 设计独立于 MQ 的定时对账任务作为最终兜底：SCAN + pipeline 扫描 Redis 订单状态，对「已支付未落库」补写入库、对「超龄 RESERVED」直接超时取消，使任何单点机制失效后系统仍能自愈到最终一致。

## 30 秒电梯陈述

"前两层覆盖了可预见的失败路径，但分布式系统总有缝隙——confirm 后进程崩溃恰好延时消息也丢了、broker 数据损坏、甚至我自己没料到的 bug。所以第三层是对账：一个完全不走 MQ 的定时任务（它要兜底的场景可能正是 MQ 坏了），每分钟 SCAN Redis 里的订单：PAID 的直接补写 PostgreSQL（幂等），RESERVED 超过 16 分钟的直接超时取消回滚库存。为了让对账能判断『滞留多久』，我在订单 hash 里加了 created_at 字段——取的是 Redis 服务器时钟，避免应用机器时钟不一致。"

## 设计决策

### 决策 1：为什么对账刻意绕过 MQ

对账补偿可以直接调 domain service，也可以补发 MQ 消息。选前者：**它要兜底的场景可能正是 MQ 本身故障**——如果补偿还要经过 MQ，就是「用坏掉的工具修坏掉的系统」。这也决定了对账必须能访问 service 层，所以它在 workflow 包、手持 domain service 引用。

### 决策 2：created_at 取 Redis 服务器时钟

Lua 里 `redis.call("TIME")`，不是应用层传入。多实例部署时各应用机器时钟可能有偏差（NTP 漂移），而所有判单都过同一个 Redis——**时钟也取同一个源，比较才有意义**。

### 决策 3：扫描必须不伤线上

- **SCAN 不用 KEYS**：KEYS 全库扫描阻塞 Redis 单线程，线上大忌；SCAN 游标分批
- **pipeline 读 hash**：一批 100 个 key 的 HGETALL 合并成一次往返，7000 订单 = 70 次 RTT 而非 7000 次
- **单订单失败不中断**：补偿失败 log 后继续，下轮再试

### 决策 4：16 分钟 = 15 分钟支付窗口 + 1 分钟宽限

正常路径下延时消息 15 分钟整到达并 MarkTimeout。对账阈值多留 1 分钟，不跟正常路径抢；即使真的撞上，Lua 状态机前置校验保证**先到者赢、后到者被拒**——对账与正常路径的并发安全性是状态机给的，不靠时序运气。

### 决策 5：后台任务 panic 必须 recover

对账 goroutine 里一次 panic 会 crash 整个进程——兜底机制自己成了单点故障。`reconcileOnce` defer recover，log 后下轮继续。

### 决策 6：旧数据兼容——没有 created_at 的订单视为「超龄」

对账上线前创建的订单没有 created_at 字段，解析为零值 → age 无穷大 → RESERVED 立即被超时清理。对老数据这恰恰是期望行为：**它们本来早就该超时了**。零迁移成本。

## 预判追问 Q&A

**Q：PAID 订单每分钟都补写一次，不浪费吗？**

A：会多一次 DB 查重（CreateOrderFromReservation 先 GetByID，存在即 no-op）。万级订单每分钟几百次主键查询，PostgreSQL 毫无压力。进一步优化是给订单加「已入库」标记（入库成功后回写 Redis）或上布隆过滤器——我知道这个方向，但为 demo 引入额外状态不值，**简单性也是设计目标**。

**Q：对账任务本身挂了怎么办？谁监督监督者？**

A：这是递归问题，答案只能是「到监控告警为止」：对账应上报心跳指标（Prometheus 的 last_run_timestamp），超过 2 个周期没跑就告警。demo 里它和主进程同生共死（goroutine），生产上独立 cron 部署 + 心跳监控。

**Q：多实例部署，多个对账任务会重复补偿吗？**

A：会，但无害——补偿操作全部幂等（CreateOrderFromReservation 查重、MarkTimeout 状态机校验）。如果要有且只有一个对账实例，加 Redis 分布式锁选主即可；但**幂等设计让选主变成优化而非必需**。

**Q：为什么不用 MQ 的延迟重试机制替代对账？**

A：职责不同。MQ 重试解决「这条消息没处理好」；对账解决「消息压根不在 MQ 里了」（进程崩溃+消息丢失、broker 故障、bug）。**消息级机制无法兜底消息本身的丢失，必须有数据级的校验**——这也是为什么对账扫的是 Redis 数据而不是 MQ 队列。

---

## 关键代码位置

| 内容 | 位置 |
|---|---|
| 对账主逻辑 | `internal/service/workflow/reconcile_workflow.go` |
| SCAN + pipeline 扫描 | `internal/cache/redis.go` — `ScanReservations` |
| created_at 写入（Redis TIME） | `internal/cache/constants.go` — `reserveTicketScript` |
| 装配与启停 | `internal/app/app.go` — `Init` / `Close` |

## 三层防御全景（改进 1 总结话术）

"我把消息可靠性拆成三层：**预防**（confirm + 原子回滚，让失败可感知、可撤销）、**遏制**（幂等消费 + 有限重试 + 死信，让失败不扩散）、**兜底**（独立对账，让任何漏网之鱼最终自愈）。每层都有自己的失败模式，所以不能只靠一层；而每一层的设计都倒逼出了下一层的需求——confirm 的回执丢失暴露了消费端幂等的缺失，幂等重试又暴露了无限 requeue，最后对账兜住所有『我不知道我不知道』的部分。"



---

# 改进 2：AMQP channel 池化 + pprof 性能分析

## 简历 Bullet（数据待实测后填入）

> 通过 pprof CPU profile 定位订票热路径上「每请求创建 AMQP channel」的网络往返开销，设计有界复用的 channel 池（借用独占、归还复用、坏通道丢弃），稳态 channel 创建次数从每请求一次降至池大小量级，QPS 从 ___ 提升至 ___（+__%），p99 延迟从 ___ms 降至 ___ms。

## 30 秒电梯陈述

"改进 1 引入 confirm 后，每条订票请求要新建一个 AMQP channel 并等 broker 回执。我接入 pprof 采集压测时的 CPU profile，发现 channel.open 的网络往返在热路径上占比可观。但 channel 不能直接共享——它不是并发安全的，confirm 模式下回执还要按 delivery tag 匹配。所以我做了一个池：借用独占、用完归还、坏通道自动丢弃替换。稳态下 channel 创建次数从每请求一次降到池大小（16）。"

## 关键设计决策

### 决策 1：为什么是池化而不是共享单 channel

- amqp091-go 的 Channel **文档明确非并发安全**
- confirm 模式下 `DeferredConfirm` 按 delivery tag 匹配回执，并发 publish 需要额外串行化
- 池化让「独占借用」成为天然约束：编译器不管，但结构上就没法共享

对比方案「单 channel + mutex」（payment workflow 用的就是这个）：热路径上互斥锁会成为新瓶颈，把网络开销换成锁竞争开销。**池化是用内存换无锁并发**。

### 决策 2：有界复用、无界借用（与 sql.DB 同款语义）

- 池空 → **新建**（请求不阻塞，峰值不退化可用性）
- 归还时池满 → **关闭**（池不膨胀，空闲资源有上限）
- 取到已关闭的 channel → 丢弃重建（broker 重启后自愈）

稳态下 channel 创建次数 ≈ 池大小，突发流量退化为旧的每请求创建模式——**优化只在需要时生效，永不成为单点**。

### 决策 3：payment workflow 为什么不用池

消费端 publish 量低（支付速率 ≈ 消费速率，且有 mock sleep 100ms~1s 天然限速），单 channel + mutex 串行化已经足够。**优化要打在测量出的瓶颈上，不是均匀撒胡椒面**——这句话本身就是面试答案。

### 决策 4：方法论——基线 commit 与优化 commit 分离

pprof 接入（`848e35f`）和池化（`b3902d8`）是两个独立 commit。压测对比时两版代码的唯一差异就是池化——**控制变量**，数据才有说服力。

## 预判追问 Q&A

**Q：创建 channel 到底贵在哪？**

A：channel.open 是一次到 broker 的同步 RPC（请求-响应往返），跨容器/跨机至少零点几到几毫秒，7000 并发就是 7000 次往返；而复用后是一次 channel 缓冲区读取，纳秒级。TCP 连接（Connection）更贵（握手+认证），所以 AMQP 设计成「连接多路复用 channel」——但 channel 也不是免费的。

**Q：为什么不用 sync.Pool？**

A：sync.Pool 的对象随时可能被 GC 清空，适合「创建便宜、GC 友好」的场景；channel 是有状态的外部资源（持有 broker 侧状态），GC 清掉等于泄露——必须显式 Close。带缓冲 channel 实现的池语义可控：容量明确、满则关闭、空则新建。

**Q：池大小 16 怎么定的？**

A：经验起点 + 压测校准。考虑因素：单 channel publish+confirm 吞吐（毫秒级）、目标 QPS、broker channelMax 上限（默认 2047/连接）。16 个 channel × 每毫秒级操作 ≫ 当前 QPS 需求，且远不及 broker 上限。压测后可以调整。

**Q：还采集了哪些 profile？**

A：CPU（找计算热点）、goroutine（看堆积——改进前能看到大量 channel 创建/关闭相关调用栈）、block/mutex（需要显式开启 rate）。火焰图看「自顶向下的调用树 + 自底向上的热点函数」两个视角。

## 实测操作手册

```bash
# ── 基线（pprof-only commit）──
git checkout 848e35f
docker compose up -d --build
# 终端1：启动 30s CPU 采样
go tool pprof -http=:8081 http://localhost:6060/debug/pprof/profile?seconds=30
# 终端2：采样期间压测
go test -v ./test/concurrent_test.go   # 记录场景一 QPS: ___

# ── 优化后（池化 commit）──
git checkout main   # 或 b3902d8
docker compose up -d --build
# 同样流程：采样 + 压测，记录 QPS: ___

# 对比火焰图：channel.open / amqp091 相关帧应显著缩小或消失
# goroutine 对比：http://localhost:6060/debug/pprof/goroutine?debug=1
```

**注意**：压测必须同机同负载对比；云服务器测试要分别记录网络条件。把数据填回上面的简历 Bullet。

## 关键代码位置

| 内容 | 位置 |
|---|---|
| channel 池实现 | `internal/mq/channel_pool.go` |
| 热路径接入 | `internal/service/workflow/reservation_workflow.go` — `Reserve` |
| pprof 挂载 | `cmd/flash-sale/main.go`（独立 6060 端口） |
| 基线 commit `848e35f`；优化 commit `b3902d8` | `git log --oneline` |
---

# 改进 3：超时释放购买资格 + 库存来源合理化

## 简历 Bullet

> 修复订单超时取消后用户无法重新购买的业务缺陷：将超时路径的「状态流转、库存回滚、购买资格释放」合并为一次 Lua 原子操作；并把 Redis 库存初始化从硬编码改为读取 DB 中场次的真实票数，让库存成为可运营数据。

## 30 秒电梯陈述

"改进 1 的回滚脚本给了我一条通用原则：**任何订单生命周期终结时，库存和购买资格必须一起释放**。拿这条原则去 review 超时路径，发现它违反了——markTicketAsTimeout 只回滚库存、不删已购标记，用户 15 分钟没支付，票回了池子，但本人永远买不了这个场次，票烂在手里。修复就是把 DEL 已购标记并入同一个超时 Lua，user_id 直接从订单 hash 里读，两个调用方（延时消息消费者、对账任务）零改动。顺手解决了另一个 review 发现：库存硬编码 100——给 Showtime 加了 TotalTickets 字段，启动时从 DB 读真实库存初始化 Redis。"

## 完整故事线

### 1. 问题怎么发现的

不是线上事故，是**原则推演**。改进 1 的 cancelReservationScript 做三件事：删订单、还库存、释放资格。复盘时把「订单终结」的所有路径摆在一起对比：

| 终结路径 | 状态流转 | 库存回滚 | 资格释放 |
|---|---|---|---|
| 消息发送失败回滚（cancel） | 删订单 | ✅ | ✅ |
| 支付超时（timeout）—— 改进前 | TIMEOUT | ✅ | ❌ **漏了** |

同一条原则在两条路径上执行不一致——这就是 bug 的藏身之处。

**业务影响**：用户超时未支付 → 订单 TIMEOUT、库存 +1 → 但 `user:{id}:showtime:{id}:ordered` 标记还在 → 用户重新下单被「您已购买过该场次」拒绝。**库存回了、用户买不了，票烂在池子里。**

**顺带发现**：`app.go` Init() 里每个场次硬编码 100 张票——库存是魔法数字，不是真实数据。

### 2. 方案

1. 超时 Lua 从订单 hash 读 `user_id`，把「HSET 状态 TIMEOUT + INCR 库存 + DEL 已购标记」合并为一次原子执行。
2. `Showtime` 模型加 `TotalTickets` 字段作为库存的 DB 真相，启动时 `Init()` 从 DB 读真实票数初始化 Redis。

### 3. 结果

- 超时用户可以重新购买该场次（验收：订单超时 → 同一用户重购成功）
- 库存与资格的释放原子完成，无中间态
- 库存配置数据化：每个场次的票数是 DB 里可运营的数据

---

## 关键设计决策

### 决策 1：为什么释放资格必须和状态流转在同一个 Lua 里

拆成应用层两次调用，并发下会暴露「库存已回滚、资格未释放」的中间态；放进同一个 Lua 还有两个更深的好处：

- **次序安全**：DEL 已购标记与旧订单的状态流转原子完成。用户能重购的前提是标记已删，而新订单 SET 标记永远发生在此之后——**不存在 DEL 误删新订单标记的竞态**，时序由原子性保证，不靠运气。
- 这正是改进 1「回滚必须原子」原则的第二次应用。一条原则，两处落地——面试时主动点出这个呼应。

### 决策 2：user_id 从 hash 里读，而不是让调用方传

对比 cancelReservationScript：它让调用方传 user_id（KEYS[3]），因为回滚时调用方本来就握着完整上下文。

超时场景不同：延时消息里只有 reservationID，两个调用方（payment workflow 的超时消费者、reconcile 对账任务）都只有这一个字段。让 Lua 自己从订单 hash 读 user_id，**`MarkTicketAsTimeout` 的签名不变，两个调用点无感知**——改动面最小。hash 缺 user_id 属异常数据，脚本防御性跳过 DEL，主流程不受影响。

### 决策 3：库存真相放 DB，Redis 只做「可重建的缓存」

- 库存是运营数据（每场不同、可能调整），DB 是它和订单数据同居的地方；Redis 重启即重建：`Redis库存 = f(DB)`。
- demo 启动清库清缓存（main.go DropTable + Redis FlushDB），所以剩余 = 总票数。**诚实边界**：如果部署契约改为「不清库重启」，正确语义是「总票数 − 已售订单数」（orders 表 count by showtime）——我知道这个语义，没实现是因为当前契约下它是恒减 0 的死代码；而且不清库重启还有更大的坑（已购标记丢失导致重复购买），不是一处能补的。

---

## 预判追问 Q&A

**Q：超时释放资格，会不会出现用户同时持有两张票？**

A：不会。状态机保证同一时刻最多一个活跃订单：旧订单必须先终结（RESERVED→TIMEOUT，先到者赢、只流转一次）资格才释放，此后才能建新订单。不存在「资格已释放但旧订单还活着」的窗口。

**Q：DEL 会不会删掉用户新订单写下的已购标记？**

A：不会。DEL 只发生在旧订单状态流转成功的那一次 Lua 里（重复触发被 -2 拒绝）。严格时序：旧订单终结 + DEL → 重购时 GET ordered = nil → 新订单 SET ordered。DEL 不可能落在新 SET 之后。

**Q：延时消息消费者和对账任务同时触发 MarkTimeout 呢？**

A：Lua 原子执行 + 状态前置校验，先到者赢：一个返回 1（DEL 执行一次），另一个拿到 -2。这和改进 1 第三层「对账与正常路径撞车」是同一个答案——**并发安全性是状态机给的**。

**Q：为什么不做成「资格标记设 TTL 自动过期」？**

A：想过，放弃了。给 ordered key 设 15 分钟 TTL 看似省事，但支付完成后再超时取消（未来扩展退款路径）时标记已自然过期，语义就乱了；而且 TTL 是「时间到了就放行」，和订单真实状态脱钩——状态机才是唯一真相，标记的生死应该由状态流转显式控制，而不是时钟。

**Q：TotalTickets 后续怎么演进成真实选座？**

A：真实影院系统按座售卖：座位表（showtime_id, seat_no）+ 已售座位唯一约束防重，Redis 侧换成位图或座位集合。TotalTickets 是迁移锚点——换成 `count(座位表)` 即可，Init 逻辑不变。计数器模型是秒杀场景的合理简化：不问座位、只要资格，热点才集中在单个 key 上，Lua 单 key 原子操作才够用。

---

## 关键代码位置

| 内容 | 位置 |
|---|---|
| 超时原子释放（核心改动） | `internal/cache/constants.go` — `markTicketAsTimeoutScript` |
| 库存 DB 真相 | `internal/model/model.go` — `Showtime.TotalTickets` |
| 启动库存初始化 | `internal/app/app.go` — `Init` |
| 对照：cancel 的同款释放语义 | `internal/cache/constants.go` — `cancelReservationScript` |

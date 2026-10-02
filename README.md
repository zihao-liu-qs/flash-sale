# Flash-Sale

这是一个使用go实现的并发抢票系统，能够正确、快速处理高并发量下的订票请求，避免超卖和重复购买，并能够保证Redis与数据库中订单的一致性

## 使用的技术

- Web框架 Gin
- 数据库 Postgresql Gorm 
- 缓存 Redis lua script
- 消息队列 rabbitMQ
- 容器  Docker 并用docker compose多容器管理

## 系统架构

```mermaid
flowchart LR
    U["客户端<br/>7000+ 并发请求"] -->|"POST /reserve"| H["Gin Handler"]

    subgraph GoSvc["Go 服务"]
        H --> RW["ReservationWorkflow"]
        PW["PaymentWorkflow<br/>（模拟支付）"]
        OW["OrderWorkflow"]
    end

    RW -->|"Lua 原子脚本<br/>幂等检查+扣库存+生成订单ID"| R[("Redis<br/>下单判断唯一数据源")]
    RW -->|"① 即时消息"| Q1["支付队列"]
    RW -->|"② 延时消息"| Q2["延时队列<br/>TTL 15min"]

    subgraph MQ["RabbitMQ"]
        Q1 --> PW
        Q2 -->|"过期→死信"| X["DLX 死信交换机"]
        X --> Q3["超时队列"]
        Q3 --> PW
        PW --> Q4["订单入库队列"]
        Q4 --> OW
    end

    PW -->|"MarkPaid（Lua）<br/>RESERVED→PAID"| R
    PW -->|"MarkTimeout（Lua）<br/>RESERVED→TIMEOUT<br/>INCR 回滚库存"| R
    OW -->|"读取 reservation"| R
    OW -->|"GORM 事务<br/>主键查重幂等"| PG[("PostgreSQL<br/>订单落盘")]
```

### 订单状态机

```mermaid
stateDiagram-v2
    [*] --> RESERVED: Lua 脚本原子创建
    RESERVED --> PAID: 支付成功（Lua 校验状态）
    RESERVED --> TIMEOUT: 15 分钟超时（Lua 校验状态）
    PAID --> [*]: 异步写入 PostgreSQL
    TIMEOUT --> [*]: INCR 回滚库存
```

两个 Lua 脚本均先校验状态为 `RESERVED` 才允许流转，保证「支付成功」与「超时取消」并发到达时只有先到者生效，状态单向流转不可逆。

## 项目设计

### 优先响应用户的订票请求

通过使用RabbitMQ在 用户订票、支付功能、写入订单到数据库 这些service之间传递消息，使各部分可以异步运行

这使得 可以直接读取Redis中的信息来决定是否允许用户的订票请求，而无须访问数据库，因此不会被数据库阻塞，加快了处理速度

Service包含两个包 domain和workflow，在domain中是不使用RabbitMQ的各种基本服务，wokflow包中则是有rabbitMQ的服务

### 保证数据的正确性和一致性

无论在Redis还是在Postgresql中，都使用原子操作来保证逻辑上的正确性。也避免了在redis中读到还有余票，但写入时已经没有余票 而引发的超订问题

Redis用lua脚本， Postgresql使用GORM提供的Transaction

### 用户订票机制

用户请求订某一张票 -> 在redis中查询还有余票且用户没有订过这场电影->通过MQ发送给payment service两条信息，一条是模拟用户支付行为，另一条经过一个15mins的延时队列，15分钟后如果用户还没有支付成功，这条消息会取消用户的订单并返还库存 -> 支付成功后通过MQ发信息给order数据库服务，写入订单到数据库

### 容器化

使用Docker包装项目，用docker compose编排容器，将Go, Postgresql, Redis, RabbitMQ 分装到多个容器中

### Layers:

model - repositorty - domain service - workflow service - app - handler

## 架构分析

### 核心思路：Redis 前置拦截 + MQ 异步解耦 + DB 最终落盘

系统整体为三段式结构：用户请求只访问 Redis（Lua 原子判单），支付与入库全部异步化，依靠状态机与多级幂等保证不超卖、不重复购买、不丢单。

关键路径上唯一的操作是一个 Redis Lua 脚本，全程不访问数据库，一次原子执行完成 6 个动作：

1. 检查用户是否已订该场次
2. 检查余票
3. `DECR` 扣库存
4. `INCR` 生成订单号
5. `HSET` 写入 RESERVED 状态订单
6. `SET` 用户已订标记

随后发送两条 MQ 消息（即时支付消息 + 15 分钟延时消息）并立即返回响应。

### 分层职责

| 层 | 位置 | 职责 |
|---|---|---|
| model | internal/model/ | User / Movie / Showtime / Order 四张表 |
| repository | internal/repository/ | GORM 数据访问，接口化（OrderRepo interface + WithTx） |
| domain service | internal/service/domain/ | 纯业务逻辑，不感知 MQ（直接调 Redis/DB） |
| workflow service | internal/service/workflow/ | 组合 domain 服务 + MQ 收发，承载异步编排 |
| app | internal/app/ | 依赖注入，启动时初始化库存与队列 |
| handler | internal/handler/ | HTTP 入口，错误码映射 |

domain 与 workflow 的拆分让核心一致性逻辑（Lua、事务）可以脱离 MQ 单独理解和测试。

### 核心设计决策

#### 1. Lua 脚本将判单收敛为一个原子操作

`reserveTicketScript` 一次性完成「查已订 → 查余票 → 扣库存 → 生成订单号 → 写订单 → 标记已订」。Lua 在 Redis 单线程上原子执行，7000 并发打到同一个 key 上也是串行判单——不需要分布式锁，也不需要数据库行锁。这是本机 QPS 能达到 2 万的原因：热路径上没有任何数据库访问。

#### 2. 状态机单向流转，解决「支付 vs 超时」竞态

`markTicketAsPaidScript` 与 `markTicketAsTimeoutScript` 都以 `status == "RESERVED"` 为前置条件。当支付成功与超时取消并发到达时（秒杀系统的经典竞态），只有先到者能翻转状态，后到者被拒绝。状态不可逆，因此不会出现「库存已回滚但订单却是已支付」的不一致局面。

#### 3. TTL + DLX 延时队列实现定时取消

利用 RabbitMQ 原生 `x-message-ttl`（15 分钟）与死信交换机实现延迟任务：消息过期后自动路由到超时队列触发取消，无需自己实现定时轮询扫描器。

#### 4. 多级幂等：Redis 订单号直接作为 DB 主键

`Order.ID` 关闭自增（`autoIncrement:false`），直接使用 Redis `INCR reservation:id:seq` 生成的 ID：

- 落库前 `GetByID` 查重，重复消费直接返回成功
- 即使查重失效，主键冲突也会让事务失败兜底

MQ 是 at-least-once 投递，该设计使重复消息无害化。

### 一致性策略

| 一致性维度 | 机制 |
|---|---|
| 不超卖 | Lua 原子 DECR，判单不碰 DB |
| 不重复购买 | `user:{id}:showtime:{id}:ordered` 键（Lua 内检查+设置） |
| 支付/超时竞态 | 状态机前置校验，先到者赢 |
| Redis ↔ DB 最终一致 | 订单号同源（Redis seq = DB 主键）+ 消费幂等 |
| 超时释放库存 | TIMEOUT 与 INCR 库存在 Lua 中原子绑定 |

### 已知权衡与局限

1. **启动即清场**：启动时 `DropTable` + `FlushDB` + `QueuePurge`，服务重启即全量数据丢失。作为学习/演示项目可以接受，但意味着崩溃时已支付未落库的订单会丢失。
2. **超时后不能重新购买**：超时脚本只回滚库存，不删除用户已订标记，用户超时未支付后将无法再次购买该场次（真实业务通常允许重新购买）。
3. **消费者并发无上限**：支付消费者对每个消息启动一个新 goroutine，没有 prefetch 限流与固定 worker 池，洪峰时 goroutine 会大量堆积。
4. **失败重试无上限**：`Nack` requeue 没有重试次数限制与死信兜底，持续失败的消息会无限重投。
5. **库存硬编码**：启动时每个场次固定写入 100 张票，而非读取数据库中的真实座位数。

这些局限也是从 demo 走向生产系统时下一步要解决的问题。

## 文件树

```shell
  .
  ├── cmd/
  │   └── flash-sale/
  │       └── main.go                        # 程序入口，初始化并启动服务
  │
  ├── config/
  │   └── config.go                          # 配置加载
  │
  ├── internal/
  │   ├── app/
  │   │   └── app.go                         # 依赖注入，服务初始化
  │   │
  │   ├── cache/
  │   │   ├── constants.go
  │   │   └── redis.go                       # Redis操作，Lua脚本保证原子性
  │   │
  │   ├── handler/
  │   │   └── handler.go                     # HTTP请求处理
  │   │
  │   ├── model/
  │   │   └── model.go                       # 数据模型：User、Movie、Showtime、Order
  │   │
  │   ├── mq/
  │   │   ├── constants.go
  │   │   ├── producer.go
  │   │   └── rabbitmq.go                    # RabbitMQ队列管理，15分钟延时队列
  │   │
  │   ├── repository/                        # 数据访问层
  │   │   ├── movie_repo.go
  │   │   ├── order_repo.go
  │   │   ├── showtime_repo.go
  │   │   └── user_repo.go
  │   │
  │   ├── service/
  │   │   ├── domain/                        # 基础业务服务
  │   │   │   ├── movie_service.go
  │   │   │   ├── order_service.go
  │   │   │   ├── payment_service.go
  │   │   │   ├── reservation_service.go
  │   │   │   └── showtime_service.go
  │   │   │
  │   │   ├── workflow/                      # 异步工作流（使用MQ）
  │   │   │   ├── order_workflow.go
  │   │   │   ├── payment_workflow.go
  │   │   │   └── reservation_workflow.go
  │   │   │
  │   │   └── errors.go
  │   │
  │   └── util/
  │       └── env.go
  │
  ├── test/
  │   └── concurrent_test.go                 # 并发测试：超卖、幂等性、多场次
  │
  ├── test_result/                           # 测试结果和截图
  │
  ├── docker-compose.yml                      # 容器编排
  ├── Dockerfile
  ├── env.example
  ├── go.mod
  └── README.md
```

## 并发测试

./test/concurrent_test.go中设计了三个测试场景：

- 场景一  **7000个用户同时抢一场电影的100张票  检测性能以及是否超卖（是否会将一张票卖给多个用户）**
- 场景二  一个用户多次买同一张票  检测是否会让一个一个用户同时买到同一张票
- 场景三  1000个用户抢3场电影，每场50张票  测试在复杂情境下是否会出现错误

### *分别在本地和云主机进行了两次测试*

### 在华为云服务器部署的测试结果

在华为云服务器部署，本地进行请求测试

云主机配置：

![配置](./test_result/云主机配置图.png)

测试结果：

场景一：7000用户同时抢100张票

7000个不同id的用户同时请求抢同一张票，显示有效避免超卖，等待3秒使数据库能够全部写入后，检测到数据库订单量正确

QPS为 4486.01，性能较高（但由于远程部署增加了网络延迟，qps显著低于本机部署（20573次））

~~~shell
go test -v ./test/concurrent_test.go
=== RUN   TestConcurrent_OversellPrevention
    concurrent_test.go:104: ✅ 测试数据初始化完成: 7000个用户, 1个场次, 每场100张票
    concurrent_test.go:250:
        🎯 场景1: 极限抢票测试
    concurrent_test.go:251: 票数: 100, 并发用户: 7000
    concurrent_test.go:215:
        ============================================================
    concurrent_test.go:216: 📊 场景1: 超卖测试 - 测试结果
    concurrent_test.go:217: ============================================================
    concurrent_test.go:218: ✅ 成功预订: 100
    concurrent_test.go:219: 🔴 已售罄: 6900
    concurrent_test.go:220: 🔁 重复预订: 0
    concurrent_test.go:221: ❌ 其他错误: 0
    concurrent_test.go:222: 📈 总请求数: 7000
    concurrent_test.go:223: ⏱️  总耗时: 1.560405667s
    concurrent_test.go:224: ⚡ 平均响应时间: 849.666919ms
    concurrent_test.go:225: 🚀 QPS: 4486.01
    concurrent_test.go:226: ============================================================
    concurrent_test.go:263: ✅ 超卖检测通过！
订票已完成，等待3秒保证数据库写入完成
    concurrent_test.go:236: ✅ 数据库验证通过: 100 条订单
--- PASS: TestConcurrent_OversellPrevention (6.31s)
=== RUN   TestConcurrent_IdempotencyCheck
    concurrent_test.go:104: ✅ 测试数据初始化完成: 10个用户, 1个场次, 每场10张票
    concurrent_test.go:288:
~~~

#### 测试场景二：同一用户幂等性测试

防止同一用户重复订票

~~~shell
 🎯 场景2: 同一用户幂等性测试
    concurrent_test.go:289: 用户1 发起 20 个并发请求
    concurrent_test.go:215:
        ============================================================
    concurrent_test.go:216: 📊 场景2: 幂等性测试 - 测试结果
    concurrent_test.go:217: ============================================================
    concurrent_test.go:218: ✅ 成功预订: 1
    concurrent_test.go:219: 🔴 已售罄: 0
    concurrent_test.go:220: 🔁 重复预订: 19
    concurrent_test.go:221: ❌ 其他错误: 0
    concurrent_test.go:222: 📈 总请求数: 20
    concurrent_test.go:223: ⏱️  总耗时: 49.736333ms
    concurrent_test.go:224: ⚡ 平均响应时间: 41.748837ms
    concurrent_test.go:225: 🚀 QPS: 402.12
    concurrent_test.go:226: ============================================================
    concurrent_test.go:301: ✅ 幂等性检测通过！
订票已完成，等待3秒保证数据库写入完成
    concurrent_test.go:236: ✅ 数据库验证通过: 1 条订单
--- PASS: TestConcurrent_IdempotencyCheck (4.36s)
=== RUN   TestConcurrent_MultipleShowtimes
    concurrent_test.go:104: ✅ 测试数据初始化完成: 3000个用户, 3个场次, 每场50张票
    concurrent_test.go:325:
~~~

#### 测试场景三: 多场次混合测试

```shell
🎯 场景3: 多场次混合测试
    concurrent_test.go:326: 3个场次, 每场50张票, 总并发: 3000
订票已完成，等待3秒保证数据库写入完成
    concurrent_test.go:215:
        ============================================================
    concurrent_test.go:216: 📊 场景3-场次1 - 测试结果
    concurrent_test.go:217: ============================================================
    concurrent_test.go:218: ✅ 成功预订: 50
    concurrent_test.go:219: 🔴 已售罄: 950
    concurrent_test.go:220: 🔁 重复预订: 0
    concurrent_test.go:221: ❌ 其他错误: 0
    concurrent_test.go:222: 📈 总请求数: 1000
    concurrent_test.go:223: ⏱️  总耗时: 606.372625ms
    concurrent_test.go:224: ⚡ 平均响应时间: 288.404285ms
    concurrent_test.go:225: 🚀 QPS: 1649.15
    concurrent_test.go:226: ============================================================
    concurrent_test.go:236: ✅ 数据库验证通过: 50 条订单
    concurrent_test.go:215:
        ============================================================
    concurrent_test.go:216: 📊 场景3-场次2 - 测试结果
    concurrent_test.go:217: ============================================================
    concurrent_test.go:218: ✅ 成功预订: 50
    concurrent_test.go:219: 🔴 已售罄: 950
    concurrent_test.go:220: 🔁 重复预订: 0
    concurrent_test.go:221: ❌ 其他错误: 0
    concurrent_test.go:222: 📈 总请求数: 1000
    concurrent_test.go:223: ⏱️  总耗时: 591.847625ms
    concurrent_test.go:224: ⚡ 平均响应时间: 406.865622ms
    concurrent_test.go:225: 🚀 QPS: 1689.62
    concurrent_test.go:226: ============================================================
    concurrent_test.go:236: ✅ 数据库验证通过: 50 条订单
    concurrent_test.go:215:
        ============================================================
    concurrent_test.go:216: 📊 场景3-场次3 - 测试结果
    concurrent_test.go:217: ============================================================
    concurrent_test.go:218: ✅ 成功预订: 50
    concurrent_test.go:219: 🔴 已售罄: 950
    concurrent_test.go:220: 🔁 重复预订: 0
    concurrent_test.go:221: ❌ 其他错误: 0
    concurrent_test.go:222: 📈 总请求数: 1000
    concurrent_test.go:223: ⏱️  总耗时: 542.95925ms
    concurrent_test.go:224: ⚡ 平均响应时间: 183.597606ms
    concurrent_test.go:225: 🚀 QPS: 1841.76
    concurrent_test.go:226: ============================================================
    concurrent_test.go:236: ✅ 数据库验证通过: 50 条订单
    concurrent_test.go:363:
        📊 多场次总结: 总成功预订 150 笔
--- PASS: TestConcurrent_MultipleShowtimes (5.57s)
PASS
ok  	command-line-arguments	17.009s
```

云服务器运行容器：

![](./test_result/云服务器运行容器1.png)![](./test_result/云服务器运行容器2.png)

本地测试

![](./test_result/本地访问云服务器进行测试.png)

测试结果详见 https://github.com/zihao-liu-qs/flash-sale/test_result/test.log

### 在本地部署的测试结果：

#### 测试场景一：7000用户同时抢100张票

7000个不同id的用户同时请求抢同一张票，显示有效避免超卖，等待3秒使数据库能够全部写入后，检测到数据库订单量正确

每秒能够处理的请求数（QPS）为20573.43, 性能较高

```shell
go test -v ./test/concurrent_test.go
=== RUN   TestConcurrent_OversellPrevention
    concurrent_test.go:92: ✅ 测试数据初始化完成: 7000个用户, 1个场次, 每场100 张票
    concurrent_test.go:238:
        🎯 场景1: 极限抢票测试
    concurrent_test.go:239: 票数: 100, 并发用户: 7000
    concurrent_test.go:203:
        ============================================================
    concurrent_test.go:204: 📊 场景1: 超卖测试 - 测试结果
    concurrent_test.go:205: ============================================================
    concurrent_test.go:206: ✅ 成功预订: 100
    concurrent_test.go:207: 🔴 已售罄: 6900
    concurrent_test.go:208: 🔁 重复预订: 0
    concurrent_test.go:209: ❌ 其他错误: 0
    concurrent_test.go:210: 📈 总请求数: 7000
    concurrent_test.go:211: ⏱️  总耗时: 340.244709ms
    concurrent_test.go:212: ⚡ 平均响应时间: 225.429043ms
    concurrent_test.go:213: 🚀 QPS: 20573.43
    concurrent_test.go:214: ============================================================
    concurrent_test.go:251: ✅ 超卖检测通过！
订票已完成，等待3秒保证数据库写入完成
    concurrent_test.go:224: ✅ 数据库验证通过: 100 条订单
--- PASS: TestConcurrent_OversellPrevention (4.18s)

```

如果设置并发请求量>10000，由于我的电脑性能限制，会出现   concurrent_test.go:170: ❌ 请求错误 [用户18890]: Post "http://127.0.0.1:4000/reserve": dial tcp 127.0.0.1:4000: socket: too many open files 错误，这应该是因为电脑资源耗尽导致的，因此我无法进行并发请求量更大的测试

#### 测试场景二：同一用户幂等性测试

防止同一用户重复订票

```shell
    concurrent_test.go:204: 📊 场景2: 幂等性测试 - 测试结果
    concurrent_test.go:205: ============================================================
    concurrent_test.go:206: ✅ 成功预订: 1
    concurrent_test.go:207: 🔴 已售罄: 0
    concurrent_test.go:208: 🔁 重复预订: 19
    concurrent_test.go:209: ❌ 其他错误: 0
    concurrent_test.go:210: 📈 总请求数: 20
    concurrent_test.go:211: ⏱️  总耗时: 1.928875ms
    concurrent_test.go:212: ⚡ 平均响应时间: 1.023789ms
    concurrent_test.go:213: 🚀 QPS: 10368.74
    concurrent_test.go:214: ============================================================
    concurrent_test.go:289: ✅ 幂等性检测通过！
订票已完成，等待3秒保证数据库写入完成
    concurrent_test.go:224: ✅ 数据库验证通过: 1 条订单
--- PASS: TestConcurrent_IdempotencyCheck (3.06s)
```

#### 测试场景三: 多场次混合测试

```shell
=== RUN   TestConcurrent_MultipleShowtimes
    concurrent_test.go:92: ✅ 测试数据初始化完成: 1000个用户, 3个场次, 每场50张票
    concurrent_test.go:313:
        🎯 场景3: 多场次混合测试
    concurrent_test.go:314: 3个场次, 每场50张票, 总并发: 3000
订票已完成，等待3秒保证数据库写入完成
    concurrent_test.go:203:
        ============================================================
    concurrent_test.go:204: 📊 场景3-场次1 - 测试结果
    concurrent_test.go:205: ============================================================
    concurrent_test.go:206: ✅ 成功预订: 50
    concurrent_test.go:207: 🔴 已售罄: 950
    concurrent_test.go:208: 🔁 重复预订: 0
    concurrent_test.go:209: ❌ 其他错误: 0
    concurrent_test.go:210: 📈 总请求数: 1000
    concurrent_test.go:211: ⏱️  总耗时: 89.633416ms
    concurrent_test.go:212: ⚡ 平均响应时间: 47.960918ms
    concurrent_test.go:213: 🚀 QPS: 11156.55
    concurrent_test.go:214: ============================================================
    concurrent_test.go:224: ✅ 数据库验证通过: 50 条订单
    concurrent_test.go:203:
        ============================================================
    concurrent_test.go:204: 📊 场景3-场次2 - 测试结果
    concurrent_test.go:205: ============================================================
    concurrent_test.go:206: ✅ 成功预订: 50
    concurrent_test.go:207: 🔴 已售罄: 950
    concurrent_test.go:208: 🔁 重复预订: 0
    concurrent_test.go:209: ❌ 其他错误: 0
    concurrent_test.go:210: 📈 总请求数: 1000
    concurrent_test.go:211: ⏱️  总耗时: 89.616709ms
    concurrent_test.go:212: ⚡ 平均响应时间: 48.939644ms
    concurrent_test.go:213: 🚀 QPS: 11158.63
    concurrent_test.go:214: ============================================================
    concurrent_test.go:224: ✅ 数据库验证通过: 50 条订单
    concurrent_test.go:203:
        ============================================================
    concurrent_test.go:204: 📊 场景3-场次3 - 测试结果
    concurrent_test.go:205: ============================================================
    concurrent_test.go:206: ✅ 成功预订: 50
    concurrent_test.go:207: 🔴 已售罄: 950
    concurrent_test.go:208: 🔁 重复预订: 0
    concurrent_test.go:209: ❌ 其他错误: 0
    concurrent_test.go:210: 📈 总请求数: 1000
    concurrent_test.go:211: ⏱️  总耗时: 89.828333ms
    concurrent_test.go:212: ⚡ 平均响应时间: 43.836538ms
    concurrent_test.go:213: 🚀 QPS: 11132.35
    concurrent_test.go:214: ============================================================
    concurrent_test.go:224: ✅ 数据库验证通过: 50 条订单
    concurrent_test.go:351:
        📊 多场次总结: 总成功预订 150 笔
--- PASS: TestConcurrent_MultipleShowtimes (3.48s)
```

## 为什么有这个项目

我在构建 github.com/qs-lzh/movie-reservation 项目时，认为可以尝试拓展项目使之能够处理高并发，但考虑到代码量较大，所以将项目的一部份后端简化并分离出来，单独写成这个项目。

# Flash-Sale 架构图解

> 一个用 Go 实现的并发抢票系统：**Redis Lua 原子判单**（热路径零 DB 访问）→ **RabbitMQ 异步**支付/超时/入库 → **PostgreSQL 最终落盘**，外层再包一层**三层可靠投递防御**。

技术栈：Gin · PostgreSQL + GORM · Redis + Lua · RabbitMQ · Docker Compose

---

## 1. 系统总览 —— 三段式架构

一句话：用户请求只访问 Redis，支付与入库全部异步化，依靠状态机与多级幂等保证不超卖、不重复、不丢单。

```mermaid
flowchart LR
    U["客户端 · 7000+ 并发"] -->|"POST /reserve"| H["Gin Handler"]

    subgraph GO["Go 服务"]
        H --> RW["ReservationWorkflow"]
        PW["PaymentWorkflow"]
        OW["OrderWorkflow"]
        RC["ReconcileWorkflow"]
    end

    RW -->|"Lua 原子判单"| REDIS[("Redis · 唯一判单源")]

    subgraph MQ["RabbitMQ"]
        QPAY["支付队列"]
        QDELAY["延时队列 · TTL 15min"]
        QTO["超时队列"]
        QORD["入库队列"]
    end

    RW -->|"即时支付消息"| QPAY
    RW -->|"延时消息"| QDELAY
    QDELAY -->|"过期 → 死信"| QTO
    QPAY --> PW
    QTO --> PW
    PW -->|"MarkPaid"| REDIS
    PW -->|"入库消息"| QORD
    QORD --> OW
    OW -->|"GORM 事务"| PG[("PostgreSQL")]
    RC -.->|"每分钟对账"| REDIS
    RC -.->|"补写落库"| PG
```

**面试要点**：热路径唯一操作是一个 Redis Lua 脚本，全程零数据库访问；MQ 同时投递「即时支付」与「15 分钟超时」两条消息。

---

## 2. 订单状态机 —— 单向流转，先到者赢

`markTicketAsPaid` 与 `markTicketAsTimeout` 都以 `status == RESERVED` 为前置条件，支付成功与超时取消并发到达时，只有先到者生效。

```mermaid
stateDiagram-v2
    [*] --> RESERVED : Lua 原子创建
    RESERVED --> PAID : 支付成功 · MarkPaid
    RESERVED --> TIMEOUT : 15min 超时 · MarkTimeout
    PAID --> [*] : 异步写 PostgreSQL
    TIMEOUT --> [*] : 回滚库存 · 释放购买资格
```

**面试要点**：状态单向流转不可逆 → 不会出现「库存已回滚、订单却已支付」；这是秒杀系统经典竞态的答案。

---

## 3. Lua 原子判单 —— 为什么不超卖、为什么快

一个脚本在 Redis 单线程上原子完成 6 步，7000 并发打到同一个 key 也是串行判单——无需分布式锁、无需 DB 行锁。

```mermaid
flowchart TB
    subgraph REQ["7000 并发请求"]
        U1["用户 A"]
        U2["用户 B"]
        U3["用户 C …"]
    end

    subgraph LUA["Redis 单线程 · 一个 Lua 脚本原子执行 6 步"]
        direction TB
        S1["① 查已订 · GET ordered"]
        S2["② 查余票 · GET remain"]
        S3["③ 扣库存 · DECR remain"]
        S4["④ 生成订单号 · INCR seq"]
        S5["⑤ 写订单 · HSET reservation"]
        S6["⑥ 标记已订 · SET ordered"]
        S1 --> S2 --> S3 --> S4 --> S5 --> S6
    end

    REQ --> LUA
    S6 -->|"返回 订单号 / 售罄 / 已订"| RES["立即响应 · 零 DB 访问"]
```

**面试要点**：6 步一个原子操作；热路径零 DB 访问是本机 QPS 2 万的原因。

---

## 4. 三层可靠投递 —— 项目核心亮点

每一层有自己的失败模式，所以不能只靠一层；每一层的设计又倒逼出下一层。

```mermaid
flowchart TB
    subgraph L1["第一层 · 预防 —— 让失败可感知、可撤销"]
        C1["消息发送失败"] --> M1["confirm + Lua 原子回滚"]
    end
    subgraph L2["第二层 · 遏制 —— 让失败不扩散"]
        C2["消费失败 · 重复投递"] --> M2["幂等消费 + 有限重试 + 停车场"]
    end
    subgraph L3["第三层 · 兜底 —— 让漏网之鱼自愈"]
        C3["消息彻底丢失 · MQ 故障"] --> M3["定时对账 · 不走 MQ"]
    end
    L1 -->|"防不住"| L2 -->|"防不住"| L3
```

**面试要点**：预防（confirm + 回滚）/ 遏制（幂等 + 有限重试 + 停车场）/ 兜底（对账）——讲清各防什么、为什么逐层递进。

---

## 5. RabbitMQ 重试拓扑 —— 有限重试

坏消息不无限 requeue：走 retry 队列 TTL 退避，`x-death` 计数满 3 次进停车场。

```mermaid
flowchart LR
    MAIN["主队列 main"] -->|"Nack · 未满 3 次"| RX["Retry 交换机"]
    RX --> RQ["Retry 队列 · TTL 10s"]
    RQ -->|"过期 → 死信"| MAIN
    MAIN -->|"x-death 已满 3 次"| PARK["停车场队列 · 人工处理"]
```

**面试要点**：TTL 天然就是退避；`x-death` 是 broker 自动维护的计次（零额外状态）；该拓扑对支付/超时/入库三条队列都生效。

---

## 6. 分层架构 —— domain 与 workflow 的拆分

domain 不感知 MQ（纯业务、可单测），workflow 组合 domain + MQ 做异步编排。

```mermaid
flowchart TB
    H["handler"] --> W["workflow · 编排 MQ"]
    W --> D["domain · 纯业务"]
    D --> R["repository"]
    R --> M["model"]

    A["app · 依赖注入"] -.->|装配| W
    A -.->|装配| D

    W -->|MQ 消息| MQ[("RabbitMQ")]
    D -->|Lua 原子| CACHE[("Redis")]
    R -->|GORM 事务| DB[("PostgreSQL")]
```

**面试要点**：拆分让核心一致性逻辑（Lua、事务）脱离 MQ 单独理解与测试；实线 = 调用，虚线 = 依赖注入装配。

---

## 7. 部署与压测数据

```mermaid
flowchart TB
    subgraph COMPOSE["docker compose"]
        FS["flash-sale · Go 服务 · :4000 / :6060"]
        PG[("postgres:13 · :5432")]
        RD[("redis:6.2 · :6379")]
        MQ[("rabbitmq:3.9 · :5672 / :15672")]
    end
    FS --- PG
    FS --- RD
    FS --- MQ
    CL["客户端 / 压测"] -->|"HTTP :4000"| FS
```

### 压测结果（三个场景）

| 场景 | 本地 QPS | 云主机 QPS | 结果 |
|---|---|---|---|
| ① 7000 用户抢 100 票 | 20573 | 4486 | 成功 100 / 售罄 6900 / 超卖 0 |
| ② 单用户 20 次并发抢同一票 | 10369 | 402 | 成功 1 / 重复拒绝 19 |
| ③ 3000 用户抢 3 场 × 50 票 | ~11156 | ~1690 | 每场 50 / 超卖 0 |

> 云主机 QPS 显著低于本地，是因为远程部署引入了网络往返延迟。

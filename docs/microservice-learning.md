# 微服务实战：从零构建功能齐全的微服务项目

> 基于 flash-sale 项目的真实代码和历史演进讲解，QPS 10000+ 秒杀系统。

---

## 目录

- [阶段零：单体 → 微服务拆分](#阶段零单体--微服务拆分)
- [阶段一：基础设施补全](#阶段一基础设施补全)
  - [第一讲：共享库 pkg/](#第一讲共享库-pkg--消除代码重复)
  - [第二讲：Consul 服务注册与发现](#第二讲consul-服务注册与发现)
  - [第三讲：Consul KV 配置中心](#第三讲consul-kv-配置中心)
  - [第四讲：Kong API 网关](#第四讲kong-api-网关)
- [阶段二：可观测性](#阶段二可观测性)
  - [第一讲：结构化日志](#第一讲结构化日志--从-logprintf-到-zap)
  - [第二讲：健康检查 & 优雅关闭](#第二讲健康检查--优雅关闭)
  - [第三讲：Prometheus 指标监控](#第三讲prometheus-指标监控)
  - [第四讲：分布式链路追踪](#第四讲分布式链路追踪--opentelemetry--jaeger)
- [阶段三：弹性与容错](#阶段三弹性与容错)
  - [第一讲：熔断器](#第一讲熔断器-circuit-breaker)
  - [第二讲：MQ 重试退避](#第二讲mq-重试退避)
  - [第三讲：令牌桶限流](#第三讲令牌桶限流)
- [阶段四：分布式事务](#阶段四分布式事务)
  - [第一讲：Saga 模式](#第一讲saga-模式)
  - [第二讲：Outbox 模式](#第二讲outbox-模式)
- [阶段五：安全](#阶段五安全)
  - [第一讲：JWT 认证 + User 服务](#第一讲jwt-认证--user-服务)
  - [第二讲：Swagger API 文档](#第二讲swagger-api-文档)
- [阶段六：Kubernetes 部署 & CI/CD](#阶段六kubernetes-部署--cicd)
  - [第一讲：Kubernetes 部署](#第一讲kubernetes-部署)
  - [第二讲：GitHub Actions CI/CD](#第二讲github-actions-cicd)
- [全阶段总结](#全阶段总结)

---

## 阶段零：单体 → 微服务拆分

### 讲的是什么

项目最初是一个单体应用（monolith），所有逻辑在一个进程里。第一个改动 `dce8f97` 把它拆分成了 3 个独立的微服务。

### 微服务的核心思想是什么

**一个服务只做一件事**。单体是"一个程序包揽所有"，微服务是"每个程序管一个领域"。

拿抢票业务举例——流程分三步：

| 步骤 | 领域 | 拆成的服务 |
|------|------|-----------|
| ① 用户抢票，扣库存 | 预订（Reservation） | `services/reservation/` |
| ② 支付处理 | 支付（Payment） | `services/payment/` |
| ③ 生成订单 | 订单（Order） | `services/order/` |

### 三个服务各自的职责

**reservation —— 这个服务是唯一暴露 HTTP 端口的**：

```go
// services/reservation/cmd/main.go
func main() {
    // 1. 连 Redis（存票仓）
    c, _ := cache.NewRedisCache(cfg.CacheURL)
    // 2. 连 RabbitMQ（发消息）
    mqConn, _ := mq.NewMQConn(cfg.MQURL)
    // 3. 起 HTTP server
    r := gin.New()
    r.POST("/reserve", svc.HandleReserve)  // 唯一的业务端点
    r.Run(cfg.Addr)
}
```

**payment 和 order —— 没有 HTTP 端口，只监听消息队列**：

```go
// services/payment/cmd/main.go
func main() {
    // 只有 MQ 消费者，没有 HTTP server
    svc := service.New(c, mqConn)
    svc.Start()  // 启动消费者，阻塞监听

    quit := make(chan os.Signal, 1)
    signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
    <-quit  // 阻塞等待退出信号
}
```

### 服务间怎么通信

**没有 HTTP 调用，没有 gRPC，只有 RabbitMQ 消息队列。** 这是一个纯粹的事件驱动架构。

消息流是单向的，通过 3 个队列串联：

```
POST /reserve  →  Reservation
                      │
                      │  ① reservation.payment.pay.immediate
                      ▼
                   Payment
                      │
                      │  ③ payment.order.create.immediate
                      ▼
                    Order
```

还有一条超时路径——15 分钟不支付就释放票：

```
Reservation  →  ② reservation.payment.timeout.delay
                      │ (15 分钟 TTL 后自动路由)
                      ▼
               reservation.payment.timeout.immediate  →  Payment（释放票）
```

这是用 RabbitMQ 的 **Dead Letter Exchange** 模式实现的，原理是：延时队列设置 `x-message-ttl`（消息存活 15 分钟），过期后自动转发到目标队列。看代码：

```go
// pkg/mq/mq.go
func setupDelayQueue(ch *amqp.Channel, ...) error {
    delayArgs := amqp.Table{
        "x-message-ttl":             int32(15 * 60 * 1000),  // 15分钟
        "x-dead-letter-exchange":    exchangeName,            // 过期后转到哪个 exchange
        "x-dead-letter-routing-key": routingKey,              // 转到哪个队列
    }
    ch.QueueDeclare(delayQ, true, false, false, false, delayArgs)
}
```

### 数据库怎么拆的

**每个服务有自己的数据库**——这是微服务的关键原则：服务之间不能共享数据库。

| 服务 | 数据库 | 表 |
|------|--------|---|
| Reservation | `reservation_db` | `showtime`（后来加了 `reservations`、`outbox`） |
| Payment | **没有数据库**，只用 Redis | - |
| Order | `order_db` | `orders` |

### 架构图

```
┌─────────────────────────────────────────────────────┐
│                   Docker / K8s                       │
│                                                      │
│  ┌──────────┐     ┌──────────┐     ┌──────────┐     │
│  │Reservation│────►│ Payment  │────►│  Order   │     │
│  │  HTTP:4000│ MQ  │  MQ only │ MQ  │  MQ only │     │
│  │  Gin      │     │          │     │          │     │
│  └────┬─────┘     └────┬─────┘     └────┬─────┘     │
│       │                │               │            │
│  ┌────▼─────┐     ┌────▼─────┐    ┌────▼─────┐     │
│  │reserve_db│     │  Redis   │    │ order_db │     │
│  │PostgreSQL│     │ (票仓+   │    │PostgreSQL│     │
│  │          │     │  预订状态)│    │          │     │
│  └──────────┘     └──────────┘    └──────────┘     │
│                                                      │
│  ┌──────────┐     ┌──────────┐                      │
│  │ RabbitMQ │     │  Redis   │                      │
│  │ 3个队列  │     │  票仓    │                      │
│  └──────────┘     └──────────┘                      │
└─────────────────────────────────────────────────────┘
```

### 要点总结

1. **服务拆分按领域边界**：预订、支付、订单各自独立
2. **事件驱动异步通信**：MQ 解耦，发送方不关心谁消费
3. **数据库按服务拆分**：每个服务只能访问自己的 DB
4. **Redis 做共享状态**：票仓库存和预订状态存在 Redis，多服务可读（注意：这是简化做法，严格微服务应该每个服务有自己的缓存）

---

## 阶段一：基础设施补全

阶段一做了 4 件事：

1. **共享库 pkg/** — 消除 3 个服务间大量重复代码
2. **Consul 服务注册与发现** — 启动时自动注册，有健康检查
3. **Consul KV 配置中心** — 集中配置，可从 Consul 覆盖 .env
4. **Kong API 网关** — 统一入口，外部只需知道 Kong 的地址

---

### 第一讲：共享库 pkg/ — 消除代码重复

#### 问题：拆分微服务后产生了大量重复代码

拆成 3 个服务后，每个服务都是独立的 Go module，有自己的 `go.mod`。这导致一些通用逻辑被复制了三份。

**重复最严重的是 `loadEnv()` 函数**。打开三个服务的 config，一模一样：

```go
// 3 个服务里的 config.go，loadEnv 完全一样
func loadEnv() error {
    dir, err := os.Getwd()         // 从当前目录开始
    for {
        envPath := filepath.Join(dir, ".env")
        if _, err := os.Stat(envPath); err == nil {
            return godotenv.Load(envPath)  // 找到 .env 就加载
        }
        parent := filepath.Dir(dir)
        if parent == dir { break }  // 到文件系统根目录还没找到就停
        dir = parent
    }
    return nil
}
```

这段逻辑的意思是：**从当前目录逐级向上找 `.env` 文件**。因为服务可能从不同目录启动，这样总能找到项目根目录的 `.env`。

**MQ 队列常量也重复了**。`"reservation.payment.pay.immediate"` 这个字符串在 reservation（生产者）和 payment（消费者）里各有一份——如果一方改了、另一方没改，MQ 就断开了。

#### 解决方案：公共模块 pkg/

在项目根目录创建 `pkg/` 目录，作为所有服务共享的 Go module：

```
flash-sale/
├── pkg/           ← 新增的公共库
│   ├── go.mod     ← 独立 module: github.com/qs-lzh/flash-sale/pkg
│   ├── mq/mq.go        ← MQ 队列名、消息类型、连接工具
│   ├── config/config.go ← loadEnv() 公共配置加载
│   ├── cache/redis.go   ← Redis 连接、key 常量、状态类型
│   ├── logger/          ← zap 结构化日志
│   ├── health/          ← 健康检查 HTTP server
│   ├── metrics/         ← Prometheus 指标
│   ├── tracing/         ← OpenTelemetry 链路追踪
│   ├── auth/            ← JWT 认证
│   ├── discovery/       ← Consul 服务注册
│   └── resilience/      ← 熔断器
├── services/
│   ├── reservation/go.mod
│   ├── payment/go.mod
│   ├── order/go.mod
│   └── user/go.mod
└── go.work         ← Go workspace，关键！
```

#### `go.work` 是粘合剂

```go
// go.work
go 1.24.3

use (
    ./pkg             // ← 把 pkg 加入 workspace
    ./services/order
    ./services/payment
    ./services/reservation
    ./services/user
    ./test
)
```

Go workspace（`go.work`）让多个独立 module 可以**互相引用而不需要发布到远程仓库**。`services/reservation/go.mod` 里并没有 `require github.com/qs-lzh/flash-sale/pkg`，但 `go work sync` 会自动处理模块解析。

**没有 workspace 的话**，每次改 pkg/ 的代码，你需要 `git tag pkg/v0.0.2`、push 到 GitHub、然后其他服务 `go get` 拉取。workspace 让所有本地 module 实时联动。

#### 各服务怎么引用 pkg

以 reservation 为例，让它用 `pkg/config` 的 `LoadEnv`：

```go
// services/reservation/internal/config/config.go
import (
    "os"
    "github.com/qs-lzh/flash-sale/pkg/config"  // 引用公共库
)

func LoadConfig() (*Config, error) {
    if err := config.LoadEnv(); err != nil {  // 只保留公共的 loadEnv 逻辑
        return nil, err
    }
    return &Config{
        DatabaseDSN: os.Getenv("DATABASE_DSN"),
        Addr:        os.Getenv("ADDR"),
        // ... 这些字段每个服务不同，留在各自的 Config 里
    }, nil
}
```

MQ 常量也一样，以 payment 的 consumer 为例：

```go
// services/payment/internal/mq/consumer.go
import pkgmq "github.com/qs-lzh/flash-sale/pkg/mq"

// 全部从 pkg 里"再导出"，保证所有服务用同一套常量
const (
    ReservationToPaymentImmediateQueue = pkgmq.ReservationToPaymentImmediateQueue
    PaymentToOrderImmediateQueue       = pkgmq.PaymentToOrderImmediateQueue
    SagaStateQueue                     = pkgmq.SagaStateQueue
)

var NackWithRetry = pkgmq.NackWithRetry  // 函数也可以重新导出
```

这样 `pkg/mq/mq.go` 是**唯一的真相来源**，队列名改了编译就会报错，不会出现运行时才发现 MQ 断连的问题。

---

### 第二讲：Consul 服务注册与发现

#### 这是什么

Consul 是一个**服务注册中心**。每个微服务启动时向 Consul 报到（"我是 reservation，在 192.168.1.5:4000 上运行"），其他服务可以从 Consul 查到它在哪。

类比：就像公司前台。新员工入职登记工位，有人找他就去前台查。

#### 为什么需要它

现在各服务通过硬编码的地址通信：

```
CACHE_URL="redis:6379"           # 写死的
RABBIT_MQ_URL="amqp://guest:guest@rabbitmq:5672/"  # 写死的
```

这在 Docker Compose 里勉强可用（容器名做 DNS），但到了 K8s 里 Pod IP 是动态的，IP 会变。如果以后加 gRPC 同步调用，服务需要知道对方在哪，总不能写死在代码里。

#### 代码怎么做的

**核心在 `pkg/discovery/consul.go`**。三个要点：

**① 注册** — 服务启动时调用：

```go
// pkg/discovery/consul.go
func Register(client *consulapi.Client, name string, port int) (*ServiceRegistration, error) {
    addr := getLocalIP()  // 自动获取本机 IP

    // 附带健康检查：Consul 每 10s 请求 /health
    check := &consulapi.AgentServiceCheck{
        HTTP:      fmt.Sprintf("http://%s:%d/health", addr, port),
        Interval:  "10s",
        DeregisterCriticalServiceAfter: "30s",  // 30s 不健康就自动摘除
    }

    svc := &consulapi.AgentServiceRegistration{
        ID:   fmt.Sprintf("%s-%s-%d", name, addr, port),
        Name: name,
        Address: addr,
        Port: port,
        Check: check,
    }
    client.Agent().ServiceRegister(svc)
    // 日志: "Registered in Consul: reservation at 192.168.1.5:4000"
}
```

**② 健康检查** — 这个依赖阶段二加的 `/health` 端点（`pkg/health`），Consul 定时 GET `/health`，超 3 次失败就自动摘除。

**③ 注销** — 服务关闭时：

```go
func (r *ServiceRegistration) Deregister() {
    r.client.Agent().ServiceDeregister(r.ID)
}
```

**④ 各服务接入** — 以 reservation 为例：

```go
// services/reservation/cmd/main.go
consulClient, _ := discovery.NewClient(os.Getenv("CONSUL_ADDR"))
var consulReg *discovery.ServiceRegistration
if consulClient != nil {
    consulReg, _ = discovery.Register(consulClient, "reservation", discovery.ParsePort(cfg.Addr))
    defer consulReg.Deregister()  // 程序退出时自动注销
}
```

注意 `if consulClient != nil` — 如果没设 `CONSUL_ADDR` 环境变量，Consul 集成就跳过，不影响开发环境。

#### 注册生命周期：IP 变了怎么办

一个关键问题：**注册时填的健康检查地址包含了 IP，IP 是会变的，Consul 怎么知道新地址？**

答案是：**每轮启动→注册用的都是当时的 IP，不是 Consul 去找服务，而是服务主动找 Consul。**

```
服务 → Consul（注册）:  POST http://consul:8500/v1/agent/service/register
                            ↑ Consul 地址固定，配置在 env 里

Consul → 服务（健康检查）:  GET http://<服务IP>:<端口>/health
                                 ↑ 这个地址是服务注册时自己报给 Consul 的
```

以 K8s 场景为例：

```
时刻 T0: Pod 启动在 Node-A，IP = 10.0.0.5
    │
    ▼
  服务调用 Consul API 注册:
    { name: "reservation", address: "10.0.0.5", port: 4000,
      health: "http://10.0.0.5:4000/health" }
           ↑ 本次启动的 IP

时刻 T1~T9: Consul 每 10s 去 GET http://10.0.0.5:4000/health → OK

时刻 T10: Pod 挂了，K8s 在 Node-B 重建，新 IP = 10.0.0.12
    │
    ▼
  新 Pod 启动，服务又调用 Consul API 注册:
    { name: "reservation", address: "10.0.0.12", port: 4000,
      health: "http://10.0.0.12:4000/health" }
           ↑ 这次启动的新 IP

同时:
  旧地址 10.0.0.5 的 /health 没人响应了
  → 连续 3 次失败 → 30s 后 Consul 自动摘除旧记录
```

#### 旧记录的两种清理路径

**路径一：正常退出 → 即时清理**

```go
// services/reservation/cmd/main.go
consulReg, _ = discovery.Register(consulClient, "reservation", ...)
defer consulReg.Deregister()  // ← Ctrl+C 或 K8s SIGTERM 时立刻执行
```

Pod 被正常终止时（滚动更新、缩容），K8s 先发 SIGTERM，服务优雅关闭，`defer` 执行 `Deregister()`——Consul 立刻删掉这条注册记录。**不需要等 30s。**

**路径二：进程崩溃 → 等 30s**

进程直接挂了（OOM、panic），`defer` 没机会执行。这时才依赖健康检查——Consul 发现 `/health` 不通，连续失败 30s 后自动摘除。**这是兜底机制，不是主路径。**

```
正常退出:   0 秒即刻清理（Deregister）
进程崩溃:   最多 30 秒自动摘除（健康检查兜底）
```

另外，为什么不直接"注册新的覆盖旧的"？因为 **ID 不同**。每次注册用的 ID 包含了 IP：

```go
ID: fmt.Sprintf("%s-%s-%d", name, addr, port)
//   "reservation-10.0.0.5-4000"   ← 旧实例
//   "reservation-10.0.0.12-4000"  ← 新实例，不同 ID
```

Consul 把它们当两个不同的服务实例。这其实是正确的行为——新旧两个 Pod 短暂共存时（滚动更新），Consul 里两个都可用，流量平滑切换。旧 Pod 被杀死后，graceful shutdown 的 `Deregister()` 立即清理，没有 30s 等待。

#### 关键设计点

| 决策 | 原因 |
|------|------|
| 服务主动注册，不是 Consul 发现 | 服务知道自己 IP，启动时主动报到；Consul 不需要猜测 |
| Consul 地址固定 | 通过环境变量 `CONSUL_ADDR` 配置，是 DNS 名或固定 IP |
| 可选集成 | 开发时不需要起个 Consul，`CONSUL_ADDR` 不设就跳过 |
| defer Deregister | 保证 Ctrl+C 退出时清理注册信息，O 延时 |
| 健康检查 30s 摘除 | 如果进程崩溃（没有 graceful shutdown），Consul 自动将它踢出 |
| ID 含 IP 而不覆盖 | 新旧两个 Pod 短暂共存时，Consul 里两个都可用，流量平滑切换 |
| 用 `/health` 而不是 `/ready` | health 表示进程活着，ready 表示依赖就绪。Consul 关心"活着" |

---

### 第三讲：Consul KV 配置中心

#### 这是什么

Consul 不仅做服务发现，还内置了一个 Key-Value 存储。可以把配置（数据库连接串、MQ 地址等）存到 Consul KV 里，服务启动时拉取。

#### 为什么需要它

现在配置靠 `.env` 文件。问题：
- 改了 `.env` 要重启服务才能生效
- 多个实例部署时每个都要配一份
- 密码、密钥跟代码混在一个文件里

Consul KV 替代方案：配置存在 Consul 里，服务启动时拉。

#### 代码怎么做的

```go
// pkg/config/config.go
func LoadFromConsul(consulAddr, prefix string) error {
    client, _ := consulapi.NewClient(&consulapi.Config{Address: consulAddr})
    kv := client.KV()

    // 列出 flash-sale/ 前缀下的所有 KV
    pairs, _, _ := kv.List("flash-sale/", nil)

    // "flash-sale/cache_url" → os.Setenv("CACHE_URL", "redis:6379")
    for _, p := range pairs {
        key := strings.TrimPrefix(p.Key, prefix)   // 去前缀
        key = strings.ReplaceAll(key, "/", "_")    // / 转 _
        key = strings.ToUpper(key)                 // 大写
        os.Setenv(key, string(p.Value))
    }
}
```

使用方式：`LoadEnv()` 先加载 `.env` 做默认值，然后 `LoadFromConsul` 覆盖。Consul 里的值优先级更高。

```go
// 各服务的 LoadConfig 里可以这样
config.LoadEnv()                     // .env 兜底
config.LoadFromConsul("consul:8500", "flash-sale/")  // Consul 覆盖
```

#### 关键设计点

| 决策 | 原因 |
|------|------|
| `.env` 兜底 | 开发时不用 Consul 也能跑 |
| Consul 覆盖 | 生产环境用 Consul 里的值 |
| 没有做热更新（watch） | 热更新需要复杂的状态管理，作为学习项目，启动时拉一次够用 |

---

### 第四讲：Kong API 网关

#### 这是什么

Kong 是一个 API 网关，站在所有服务前面。客户端只需知道 Kong 的地址，Kong 根据 URL 路径把请求转发到对应的服务。

#### 为什么需要它

没有网关时：

```
客户端 → reservation:4000/reserve
客户端 → user:4003/register
```

客户端需要知道每个服务的端口。网关统一入口后：

```
客户端 → Kong:8000/reserve  → reservation:4000
客户端 → Kong:8000/register → user:4003
```

#### 代码怎么做的

只有一个配置文件 `kong/kong.yml`，声明式定义路由：

```yaml
# kong/kong.yml
services:
  - name: reservation
    url: http://reservation:4000          # 上游真实地址
    routes:
      - name: reservation-api
        paths:
          - /reserve                      # 匹配这些路径就转发
          - /health /ready /metrics /swagger

  - name: user
    url: http://user:4003
    routes:
      - name: user-api
        paths:
          - /register
          - /login
      - name: user-health
        paths:
          - /user/health                  # 外部路径 /user/health → 内部 /health
        strip_path: true

plugins:
  - name: rate-limiting
    service: reservation
    config:
      minute: 600000                     # 每分钟 60 万次（10k QPS）
```

#### 关键设计点

| 决策 | 原因 |
|------|------|
| 声明式配置 | Kong 启动时加载 `kong.yml`，不需要 API 调用来配置 |
| `strip_path: true` | `/user/health` → 转发到 `/health` |
| 限流放在两个地方 | Kong 网关限流（粗粒度，全局），代码里令牌桶限流（细粒度，只限 `/reserve`），双层防护 |

### 阶段一总结

```
加网关前:
  Client ──► reservation:4000
  Client ──► user:4003

加网关、Consul、配置中心后:
  Client ──► Kong:8000 ──路由──► reservation (Consul 注册)
                     │          user (Consul 注册)
                     │
  启动时: 各服务 → Consul 注册
         Consul KV ← 各服务拉取配置
```

阶段一补全了微服务的基础骨架。

---

## 阶段二：可观测性

可观测性三支柱：**日志（知道发生了什么）、指标（知道系统状态）、追踪（知道请求走了哪些路径）**。再加上健康检查和优雅关闭——基础但不简单。

---

### 第一讲：结构化日志 — 从 `log.Printf` 到 zap

#### 问题

改造前三个服务都用 Go 标准库 `log`：

```go
log.Printf("Failed to handle payment message: %v", err)
// 输出: 2026/06/25 22:00:00 Failed to handle payment message: connection refused
```

问题：
- 没有字段化的结构（比如 `service=payment`），排查时没法按服务过滤
- 没有日志级别（info/warn/error），全挤在一起
- 多服务并发输出，同一个请求的日志分散，没法关联

#### 解决方案：zap

zap 是 Uber 开源的高性能结构化日志库。核心优势：**JSON 输出 + 零内存分配**。

```go
// pkg/logger/logger.go
func Init(service string) error {
    config := zap.NewProductionConfig()
    config.EncoderConfig.TimeKey = "timestamp"
    config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
    config.InitialFields = map[string]interface{}{
        "service": service,  // ← 每条日志自动带 service 名
    }
    logger, _ := config.Build()
    Log = logger.Sugar()  // Sugar 提供 Infof/Errorf 等便捷方法
}
```

**输出效果对比**：

```
改造前:
2026/06/25 22:00:00 Failed to handle payment

改造后:
{"timestamp":"2026-06-25T22:00:00.000+0800","level":"error","service":"payment","msg":"Failed to handle payment message: connection refused"}
```

**各服务接入**，以 reservation 为例：

```go
// services/reservation/cmd/main.go
logger.Init("reservation")       // 初始化，自动带 service=reservation
defer logger.Sync()              // 退出前 flush 缓冲区

// 业务代码中
logger.Log.Infof("Reservation service listening on %s", cfg.Addr)
logger.Log.Errorf("Failed to create cache: %v", err)
logger.Log.Fatalf("Failed to load config: %v", err)  // fatal 后 os.Exit(1)
```

#### 关键设计点

- **`InitialFields` 注入 `service` 字段**：所有日志自动带服务名，Grafana/Loki 里直接按 service 过滤
- **`Sugar()` vs 原始 logger**：Sugar 支持 `Infof/Errorf` 格式化，开发方便；原始 logger 用结构化字段，性能更好
- **`Sync()` 在 defer 里**：zap 有内部缓冲区，退出时 flush，否则最后几条日志可能丢

---

### 第二讲：健康检查 & 优雅关闭

#### 健康检查端点的设计

三个端点，各有用处：

| 端点 | 含义 | 谁用 |
|------|------|------|
| `/health` | 进程是否存活（永远 200） | Consul、K8s liveness probe |
| `/ready` | 是否准备好接流量 | K8s readiness probe |
| `/metrics` | Prometheus 指标数据 | Prometheus server 抓取 |

```go
// pkg/health/health.go
func New(addr string) *Server {
    mux := http.NewServeMux()
    mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })
    mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
        if s.ready == 1 {                  // MarkReady() 后才返回 ready
            w.WriteHeader(http.StatusOK)
            w.Write([]byte("ready"))
        } else {
            w.WriteHeader(http.StatusServiceUnavailable)  // 503
        }
    })
    mux.Handle("/metrics", metrics.Handler())  // Prometheus 的 handler
}
```

**health vs ready 的区别很关键**：`/health` 只检查进程是否活着，`/ready` 检查依赖是否就绪（DB 连上了、MQ 连上了）。K8s 会根据 readiness 决定要不要给这个 Pod 发流量。

#### 优雅关闭

看看 reservation 的关闭流程，每一步都有讲究：

```go
// services/reservation/cmd/main.go

// 1. 监听信号（阻塞）
quit := make(chan os.Signal, 1)
signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
<-quit  // Ctrl+C 或 K8s pod delete → 收到信号，往下走

// 2. 按顺序清理
logger.Log.Info("Reservation service shutting down...")
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

// 3. 先停 HTTP server（不再接受新请求，等待现有请求完成）
srv.Shutdown(ctx)     // 最多等 5s

// 4. 再关 MQ 连接（消费者停止，消息回到队列）
mqConn.Close()

// 5. Consul 自动注销（defer consulReg.Deregister()）
```

**关闭顺序很重要**：先停流量入口（HTTP），再停消费者（MQ），最后停出站连接。如果反过来，先关 MQ 再关 HTTP，正在处理的请求可能会因为发不出 MQ 消息而失败。

---

### 第三讲：Prometheus 指标监控

#### 什么是 Prometheus

Prometheus 是一个**拉模式**的指标系统。它定期去每个服务的 `/metrics` 端点抓数据，存到时序数据库里，Grafana 来可视化。

#### 哪些指标值得收集

```go
// pkg/metrics/metrics.go

// HTTP 级别（reservation）
HTTPRequestsTotal = prometheus.NewCounterVec(       // 请求计数
    prometheus.CounterOpts{Name: "http_requests_total"},
    []string{"method", "path", "status"},           // 标签: 按方法/路径/状态码分组
)
HTTPRequestDuration = prometheus.NewHistogramVec(   // 请求延迟
    prometheus.HistogramOpts{Name: "http_request_duration_seconds"},
    []string{"method", "path"},
)

// MQ 消费（payment + order）
MQConsumedTotal = prometheus.NewCounterVec(         // 消费了多少消息
    prometheus.CounterOpts{Name: "mq_consumed_total"},
    []string{"queue", "status"},                     // 按队列和成功/失败分组
)

// 业务级别
ReservationTotal   // 总预订请求数
ReservationSuccess // 成功的预订数
ReservationFailed  // 失败的预订数
PaymentTotal       // 支付总数（success/failed）
PaymentDuration    // 支付处理延迟（直方图）
OrderCreatedTotal  // 创建的订单数
```

#### 怎么收集的

**HTTP 指标**用 Gin 中间件：

```go
// services/reservation/internal/handler/middleware.go
func MetricsMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        start := time.Now()
        c.Next()  // 执行后续处理器
        status := strconv.Itoa(c.Writer.Status())
        metrics.HTTPRequestsTotal.WithLabelValues(c.Request.Method, c.FullPath(), status).Inc()
        metrics.HTTPRequestDuration.WithLabelValues(c.Request.Method, c.FullPath()).Observe(time.Since(start).Seconds())
    }
}
```
jkfdlsaj

**MQ 消费指标**在业务处理中：

```go
// services/order/internal/service/order.go
func (s *Service) handleOrderCreation(msg amqp.Delivery) error {
    if err := s.createOrderFromReservation(...); err != nil {
        metrics.MQConsumedTotal.WithLabelValues(mq.PaymentToOrderImmediateQueue, "error").Inc()
        mq.NackWithRetry(msg)
        return err
    }
    metrics.MQConsumedTotal.WithLabelValues(mq.PaymentToOrderImmediateQueue, "success").Inc()
    metrics.OrderCreatedTotal.Inc()
}
```

#### 关键设计点

| 设计 | 原因 |
|------|------|
| Counter 不用 Gauge | Counter 是只增不减的累计值（总请求数），Gauge 是可升可降的瞬时值（当前连接数） |
| Histogram 不是 Summary | Histogram 可以聚合（P50/P95/P99），Summary 不能跨实例聚合 |
| 标签要克制 | 每多一个标签维度，Prometheus 多存一份时序。用 `path` 不要用 `full_url` |

---

### 第四讲：分布式链路追踪 — OpenTelemetry + Jaeger

#### 问题：一个请求经过了哪些服务，每个服务花了多久？

没有追踪时，`/reserve` 请求慢，你不知道是 Redis 慢了、MQ 慢了、还是 Payment 慢了。链路追踪把每个服务的处理时间串起来，形成一个调用树。

#### 怎么工作的

OpenTelemetry 在每个服务边界注入/提取 trace context：

```
HTTP 请求 → otelgin 中间件创建 span（根 span）
    │  inject trace headers into AMQP message headers
    ▼
Reservation → MQ → Payment (extract headers, create child span)
    │              inject into next MQ
    ▼
Payment → MQ → Order (extract headers, create child span)
```

每个 span 有时间戳、耗时、标签，Jaeger 把它们拼成一棵树。

#### 代码怎么做的

**① 初始化 tracer**：

```go
// pkg/tracing/tracing.go
func Init(serviceName, endpoint string) (*sdktrace.TracerProvider, error) {
    // 创建 OTLP exporter，把 traces 发给 Jaeger
    exp, _ := otlptracehttp.New(context.Background(),
        otlptracehttp.WithEndpoint(endpoint),  // jaeger:4318
        otlptracehttp.WithInsecure(),
    )
    // 创建 TracerProvider
    tp := sdktrace.NewTracerProvider(
        sdktrace.WithBatcher(exp),
        sdktrace.WithResource(resource.NewWithAttributes(semconv.ServiceName(serviceName))),
        sdktrace.WithSampler(sdktrace.AlwaysSample()),  // 全量采样
    )
    otel.SetTracerProvider(tp)
    otel.SetTextMapPropagator(propagation.TraceContext{})  // W3C Trace Context 标准
}
```

**② HTTP 侧自动创建 span** — 一行代码：

```go
// services/reservation/cmd/main.go
r.Use(otelgin.Middleware("reservation"))  // 每个 HTTP 请求自动创建 span
```

**③ MQ 侧传播 trace context** — 这是关键。trace 信息通过 AMQP headers 传递：

```go
// pkg/tracing/tracing.go

// 发送时注入
func InjectAMQPHeaders(ctx context.Context, headers amqp.Table) {
    otel.GetTextMapPropagator().Inject(ctx, &amqpHeaderCarrier{headers})
}

// 接收时提取
func ExtractAMQPHeaders(ctx context.Context, headers amqp.Table) context.Context {
    return otel.GetTextMapPropagator().Extract(ctx, &amqpHeaderCarrier{headers})
}
```

**④ MQ 生产者注入**（在 `pkg/mq/mq.go` 的 `Publish` 函数里）：

```go
func Publish(ctx context.Context, ch *amqp.Channel, queueName string, message any) error {
    headers := amqp.Table{}
    tracing.InjectAMQPHeaders(ctx, headers)  // 把 trace 信息写入消息头
    return ch.PublishWithContext(ctx, "", queueName, false, false,
        amqp.Publishing{
            Headers: headers,  // 携带 trace context
        })
}
```

**⑤ MQ 消费者提取**（在 payment 消费时）：

```go
// services/payment/internal/service/payment.go
ctx := tracing.ExtractAMQPHeaders(context.Background(), msg.Headers)
// ctx 现在包含了父 span 的信息，后续操作会创建子 span
```

#### 完整的 trace 链路

```
[HTTP Request: POST /reserve]           ← otelgin middleware 创建根 span
  ├─ Redis Lua script (reservation)     ← 自动记录耗时
  ├─ Outbox insert (reservation)
  └─ [MQ: reservation.payment.pay]      ← inject headers
       └─ Payment handler               ← extract headers, create child span
            ├─ mock pay (100-1000ms)     ← 自动记录耗时
            └─ [MQ: payment.order.create] ← inject again
                 └─ Order handler        ← extract headers, create child span
                      └─ DB insert
```

在 Jaeger UI 里能看到完整的调用树，每个节点的耗时一目了然。

#### 关键设计点

| 设计 | 原因 |
|------|------|
| W3C Trace Context 标准 | 不同语言/框架都能互通，不是 OpenTelemetry 专有的 |
| AlwaysSample | 学习项目全量采，生产可以改成比例采样（0.1% 等） |
| 通过 AMQP headers 传播 | MQ 不像 HTTP 有标准 header，需要自定义 carrier |
| OTLP HTTP exporter | Jaeger 原生支持 OTLP，不需要 Jaeger 专用的 thrift exporter |

### 阶段二总结

```
改造前:
  log.Printf              ← 非结构化，无上下文
  无健康检查               ← 负载均衡器不知道服务是否可用
  无指标                   ← 不知道 QPS、延迟、错误率
  无追踪                   ← 慢请求无法定位

改造后:
  zap JSON 日志            ← 结构化的，带 service 字段
  /health /ready /metrics  ← 标准健康检查端点
  Prometheus 9 种指标      ← HTTP/MQ/业务全方位
  Jaeger 全链路追踪        ← reservation → MQ → payment → MQ → order
```

阶段二让系统从"黑盒"变成了"白盒"。

---

## 阶段三：弹性与容错

微服务架构的核心挑战：**服务多了，出问题的概率就大——你不能指望所有依赖永远健康。**

三件武器：
1. **熔断器**：依赖挂了，快速失败，别雪崩
2. **重试退避**：临时抖动，重试几次可能就好了
3. **限流**：保护自己，不让别人把你打垮

---

### 第一讲：熔断器 (Circuit Breaker)

#### 问题：Redis 挂了会怎样

看改造前的 reservation handler：

```go
reservationID, err := s.Cache.ReserveTicket(req.ShowtimeID, req.UserID)
// Redis 挂了 → err != nil → 返回 500
// 但每个请求都会等 Redis 超时（默认 3 秒），goroutine 堆积，内存暴涨
```

这就是**雪崩效应**：一个依赖挂了，所有请求都在那等超时，线程/连接池耗尽，整个服务不可用。

熔断器解决这个问题：**失败 N 次后，直接跳过调用，快速返回错误**。等一段时间后再试试，恢复了就继续正常调。

#### 代码：gobreaker v2 的两步式熔断器

gobreaker 有两版 API。v2 是 "两步式"——先问（Allow），再汇报结果（done）。这比 v1 的 "包一个函数" 更灵活，因为你可以在 Allow 和 done 之间做自己的逻辑：

```go
// pkg/resilience/circuit.go
func Init() {
    settings := gobreaker.Settings{
        Name:        "redis",
        MaxRequests: 3,      // 半开状态最多放 3 个请求试探
        Timeout:     30,     // 打开 30 秒后进入半开
        ReadyToTrip: func(counts gobreaker.Counts) bool {
            return counts.TotalFailures >= 5  // 连续 5 次失败就熔断
        },
    }
    RedisCB = gobreaker.NewTwoStepCircuitBreaker[any](settings)
}
```

**熔断器有三个状态**：

```
  ┌─────────┐  5次失败   ┌─────────┐
  │ CLOSED  │ ─────────► │  OPEN   │
  │ (正常)  │            │ (熔断)  │
  └─────────┘            └────┬────┘
       ▲                     │ 30秒后
       │  试探成功      ┌────▼─────┐
       └─────────────── │ HALF-OPEN│
                        │ (半开)   │
                        └──────────┘
```

#### 怎么用的——以 reservation 为例

```go
// services/reservation/internal/handler/reserve.go
func (s *Service) HandleReserve(ctx *gin.Context) {
    // ① 问熔断器：能继续吗？
    done, cbErr := resilience.RedisCB.Allow()
    if cbErr != nil {
        // 熔断了 → 立刻返回 503，不等 Redis
        logger.Log.Errorf("Circuit breaker open (redis): %v", cbErr)
        ctx.JSON(503, gin.H{"error": "Service temporarily unavailable"})
        return
    }

    // ② 正常调用
    reservationID, err := s.Cache.ReserveTicket(req.ShowtimeID, req.UserID)
    if err != nil {
        done(err)   // ③ 汇报失败 → 失败计数 +1
        // ...
        return
    }
    done(nil)       // ③ 汇报成功 → 失败计数归零
}
```

**关键：`done(err)` 通知熔断器结果**。如果连续 5 次 `done(err)`，熔断器跳到 OPEN 状态，后续 `Allow()` 直接返回错误，不再等 Redis。

#### Payment 和 Order 也用各自的 CB

```go
// payment — 保护 Redis
done, cbErr := resilience.RedisCB.Allow()

// order — 保护 Postgres
done, cbErr := resilience.PostgresCB.Allow()
```

#### 关键设计点

| 决策 | 原因 |
|------|------|
| 两步式 (Allow + done) | 灵活——你可以在 Allow 和 done 之间做任何操作 |
| 两个独立的 CB（Redis + Postgres） | Redis 挂了不该影响 Postgres 的正常调用 |
| 熔断条件：TotalFailures >= 5 | 不是 ConsecutiveFailures，TotalFailures 更加稳健 |
| 返回 503 而不是 500 | 503 = "暂时不可用"，语义更准确 |

---

### 第二讲：MQ 重试退避

#### 问题：MQ 消息处理失败怎么办

Payment 消费 MQ 消息，如果 Redis 操作失败了，消息就丢了？不能。MQ 消费者应该**重试直到成功**，但无限重试也不行——需要上限 + 退避。

#### 代码：基于 AMQP header 的重试计数

```go
// pkg/mq/mq.go
const maxRetries = 3

func NackWithRetry(msg amqp.Delivery) {
    count := getRetryCount(msg.Headers)

    // 首次失败 → 在 header 里标记重试次数
    msg.Headers["x-retry-count"] = count + 1

    if count < maxRetries {
        msg.Nack(false, true)   // requeue=true → 回到队列尾，等下次投递
    } else {
        msg.Nack(false, false)  // requeue=false → 丢弃（或进死信队列）
    }
}

func getRetryCount(headers amqp.Table) int32 {
    // 从 AMQP header 里读 x-retry-count
}
```

#### 怎么用的

**可重试的失败**（Redis 临时不可用、DB 连接中断）→ `NackWithRetry`：

```go
// services/payment/internal/service/payment.go
if err := s.mockPay(message.ReservationID); err != nil {
    mq.NackWithRetry(msg)  // 等会儿再试
    return 0, err
}
```

**不可重试的失败**（JSON 解析错误）→ 直接丢弃：

```go
if err := json.Unmarshal(msg.Body, &message); err != nil {
    msg.Nack(false, false)  // 格式错了，重试多少遍都一样
    return err
}
```

#### 关键设计点

| 决策 | 原因 |
|------|------|
| 通过 AMQP header 计数 | 消息被 requeue 后重新投递，header 还在，计数器不会丢 |
| 最多 3 次 | 超过 3 次说明不是临时故障，再重试只会填满队列 |
| 区分可重试和不可重试的失败 | JSON 解析失败、参数错误 → 不重试。DB/Redis 临时故障 → 重试 |
| 没用 DLX（死信队列） | 简化实现。生产环境应该把超过重试的消息发到 DLX 让人工处理 |

---

### 第三讲：令牌桶限流

#### 问题：为什么做了网关限流还要代码限流

Kong 网关有限流（每分钟 60 万次），但这是**粗粒度的全局限流**。万一有人绕过网关直接打 `reservation:4000` 呢？或者 Payment/Order 服务被内部消息风暴打爆？

**网关是防火墙，代码限流是风控——两道防线。**

#### 代码：标准库实现的令牌桶

没用第三方库，纯标准库实现——因为令牌桶算法核心就 20 行：

```go
// services/reservation/internal/handler/middleware.go
type tokenBucket struct {
    rate   int         // 每秒产生多少令牌
    burst  int         // 突发允许最大多少
    tokens int         // 当前令牌数
    lastTime time.Time // 上次补充令牌的时间
    mu    sync.Mutex   // 并发安全
}

func (tb *tokenBucket) allow() bool {
    tb.mu.Lock()
    defer tb.mu.Unlock()

    now := time.Now()
    elapsed := now.Sub(tb.lastTime)
    tb.lastTime = now

    // 补充令牌：经过的时间 × 速率
    tb.tokens += int(float64(tb.rate) * elapsed.Seconds())
    if tb.tokens > tb.burst {
        tb.tokens = tb.burst  // 不能超过 burst（防突发堆积）
    }

    if tb.tokens > 0 {
        tb.tokens--
        return true   // 拿到令牌，放行
    }
    return false      // 没令牌了，拒绝
}
```

#### 算法原理

```
想象一个桶，每秒往里面放 rate 个令牌:
  速率 rate=10000/s
  容量 burst=10000（最多堆积 10000 个令牌）

请求来了 → 取一个令牌 → 有 → 放行
                          → 没有 → 429 Too Many Requests
```

`burst` 的作用是允许**短时突发**。如果 1 秒内突然来 15000 个请求：
- burst=10000：前 10000 个放行（桶里之前累积的），后面 5000 被限
- burst=0：每个请求都严格按 10000/s 放行，没有缓冲

#### 怎么用的

只对 `/reserve` 限流，健康检查和 metrics 不受限：

```go
// services/reservation/cmd/main.go
api := r.Group("/")
api.Use(auth.GinMiddleware())                  // ① JWT 认证
api.Use(handler.RateLimitMiddleware(10000))    // ② 限流 10000/s
api.POST("/reserve", svc.HandleReserve)        // ③ 业务逻辑
```

**中间件顺序很重要**：JWT 在前 → 不合法的请求先拦住，不消耗令牌。如果限流在前 → 没 token 的恶意请求也能拒绝，但 JWT 验证是浪费的。

### 阶段三总结

```
每个服务现在有三层保护:

  请求 → [限流] → [JWT] → [熔断] → [业务逻辑]
            │                │
            │ 令牌不够       │ DB/Redis 挂了
            │ 返回 429      │ 返回 503
            ▼                ▼
         快速拒绝         快速失败
```

三种机制各司其职：

| 机制 | 保护谁 | 什么时候触发 |
|------|--------|-------------|
| 限流 | 保护自己 | 调用方太猛，超过处理能力 |
| 熔断 | 保护自己 | 依赖方挂了，别再等了 |
| 重试 | 保护消息 | 临时抖动，再试一次可能就好了 |

---

## 阶段四：分布式事务

微服务里最棘手的问题：**一次业务操作涉及多个服务，怎么保证数据最终一致？**

拿抢票举例——一个完整的预订流程跨越 3 个服务：

```
Reservation（扣库存）→ Payment（扣钱）→ Order（生成订单）
```

单体时代一个数据库事务搞定。拆成微服务后，三个服务三个独立数据库（或 Redis），没有跨服务的 ACID 事务。需要新的方案。

两件武器：
1. **Saga 模式**：长事务拆成多个本地事务 + 补偿
2. **Outbox 模式**：保证"写数据库"和"发消息"原子性

---

### 第一讲：Saga 模式

#### 先看改造前的问题

改造前，Payment 直接操作 Reservation 的 Redis 数据：

```go
// 改造前的 Payment —— 跨服务改别人的数据
func (s *Service) mockPay(reservationID uint) error {
    return s.Cache.MarkTicketAsPaid(reservationID)    // 直接写 Redis
}

func (s *Service) handleTimeout(msg amqp.Delivery) {
    s.Cache.MarkTicketAsTimeout(message.ReservationID) // 直接释放库存
}
```

这不是 Saga——这是**直接跨服务写数据**，打破了服务边界。

#### 什么是 Saga

Saga 的核心思想：**把一个大事务拆成 N 个本地事务，每个本地事务有一个对应的补偿操作。如果中间某步失败，按逆序执行补偿。**

```
正向流程:
  Reservation: 扣库存 → 发 MQ
  Payment:     处理支付 → 发 MQ
  Order:       创建订单

补偿流程（如果支付超时）:
  Reservation: 释放库存 ← 这是补偿
```

注意：**Saga 不是回滚**。数据库回滚一条 undo log 就搞定了。Saga 的补偿是**写一个新操作来抵消之前的影响**——释放库存不是"撤销扣库存"，而是"把票加回去"。

#### 代码怎么做的

**① 创建 Saga 状态表**，追踪每笔预订走到哪一步：

```go
// services/reservation/internal/model/model.go
type SagaState string
const (
    SagaStateReserved SagaState = "RESERVED"  // 已预订，等支付
    SagaStatePaid     SagaState = "PAID"      // 已支付
    SagaStateTimeout  SagaState = "TIMEOUT"   // 超时，已补偿
)

type Reservation struct {
    ID         uint      `gorm:"primaryKey"`
    ShowtimeID uint
    UserID     uint
    State      SagaState `gorm:"default:RESERVED"`  // ← 追踪 Saga 进度
}
```

**② 预订时创建 Saga 记录**：

```go
// services/reservation/internal/handler/reserve.go
reservationID, err := s.Cache.ReserveTicket(req.ShowtimeID, req.UserID)

// 在 Redis 中创建预订后，DB 里记录 Saga 状态
s.Repo.CreateReservation(reservationID, req.ShowtimeID, req.UserID, model.SagaStateReserved)
```

**③ Payment 只发 Saga 状态消息，不碰 Redis**：

```go
// services/payment/internal/service/payment.go
// 支付成功 → 通知 Reservation "PAID"
mq.SendImmediateMessage(ctx, ch, mq.SagaStateQueue,
    mq.SagaStateMessage{ReservationID: reservationID, State: "PAID"})

// 支付超时 → 通知 Reservation "TIMEOUT"（触发补偿）
mq.SendImmediateMessage(ctx, ch, mq.SagaStateQueue,
    mq.SagaStateMessage{ReservationID: message.ReservationID, State: "TIMEOUT"})
```

**④ Reservation 的 Saga Consumer 执行真正的状态变更**：

```go
// services/reservation/internal/handler/saga.go
func handleSagaState(msg amqp.Delivery, repo *repository.Repo, redisCache *cache.RedisCache) {
    switch message.State {
    case "PAID":
        // 正向操作：标记已支付
        redisCache.MarkTicketAsPaid(message.ReservationID)
        repo.UpdateState(message.ReservationID, model.SagaStatePaid)
    case "TIMEOUT":
        // 补偿操作：释放库存（这就是 Saga 的"补偿"）
        redisCache.MarkTicketAsTimeout(message.ReservationID)  // HSET status=TIMEOUT + INCR ticket
        repo.UpdateState(message.ReservationID, model.SagaStateTimeout)
    }
}
```

#### 流程图

```
┌──────────────┐     ┌──────────┐     ┌──────────┐
│ Reservation  │────►│ Payment  │────►│  Order   │
│              │ MQ  │          │ MQ  │          │
│ ① 扣库存     │     │ ② 模拟支付│     │ ③ 创建订单│
│   写 Saga DB │     │   发PAID │     │          │
└──────┬───────┘     └────┬─────┘     └──────────┘
       │                  │
       │     Saga Queue   │
       │◄─────────────────┘
       │  ④ 收到 PAID → Redis MarkPaid
       │  收到 TIMEOUT → Redis MarkTimeout (补偿)
       ▼
```

#### 关键设计点

| 决策 | 原因 |
|------|------|
| Saga 状态存在 Reservation 自己的 DB | 每个服务管理自己的数据，不跨服务写 |
| 补偿操作（释放库存）在 Reservation 端 | Payment 不知道库存细节，Reservation 自己补偿 |
| 用 MQ 传递 Saga 指令 | 和业务 MQ 同一套基础设施，不引入额外的 RPC |
| Saga 消费者 + 业务处理共存在 Reservation | 对于 4 服务规模不需要独立 Saga Orchestrator |

---

### 第二讲：Outbox 模式

#### 问题：Redis 写入成功了，MQ 发送失败了怎么办？

看预订流程：

```go
// 改造前的 handler
reservationID, err := s.Cache.ReserveTicket(req.ShowtimeID, req.UserID)
// ① Redis 写成功了

ch, _ := mq.NewChannel(s.MQConn)
mq.SendImmediateMessage(ch, queue, message)
// ② MQ 发送失败了 → 内存里有预订，但 Payment 永远不会处理
```

这就是**双写问题**（Dual Write Problem）：两个系统（Redis + MQ），没有跨系统的事务，② 失败后 ① 不会回滚。

#### Outbox 怎么解决

**不直接发 MQ，先写到数据库的 outbox 表，后台再异步发送。** 这保证：只要 Redis 操作成功 + outbox 写入成功（同在一个 DB 事务内），消息最终一定会被发出。

```
改造前:
  Redis.Lua → MQ.Publish  ← 两步，后一步可能失败

改造后:
  Redis.Lua → DB.Outbox.Insert  ← 在同一个本地事务里
                    │
          后台 Dispatcher 轮询 → MQ.Publish
```

#### 代码怎么做的

**① Outbox 表**：

```go
// services/reservation/internal/model/model.go
type Outbox struct {
    ID          uint       `gorm:"primaryKey"`
    QueueName   string     // 目标队列
    Body        string     // JSON 消息体
    PublishedAt *time.Time // nil = 未发送, 有值 = 已发送
    CreatedAt   time.Time
}
```

**② 预订时写入 Outbox 而不是直接发 MQ**：

```go
// services/reservation/internal/handler/reserve.go
// 不再: mq.SendImmediateMessage(...)
// 改为写 outbox:
s.OutboxStore.Insert(mq.ReservationToPaymentImmediateQueue,
    mq.ReservationToPaymentImmediateMessage{
        ReservationID: reservationID,
        ShowtimeID:    req.ShowtimeID,
        UserID:        req.UserID,
        Price:         1,
    })
s.OutboxStore.Insert(mq.ReservationToPaymentDelayQueue,
    mq.ReservationToPaymentDelayMessage{ReservationID: reservationID})
```

**③ 后台 Dispatcher 轮询发送**：

```go
// services/reservation/internal/outbox/outbox.go
func (d *Dispatcher) Start() {
    go func() {
        ticker := time.NewTicker(500 * time.Millisecond)  // 每 500ms 轮询
        for range ticker.C {
            d.process()
        }
    }()
}

func (d *Dispatcher) process() {
    // 查未发送的记录
    var records []model.Outbox
    d.db.Where("published_at IS NULL").Order("id").Limit(20).Find(&records)

    for _, record := range records {
        ch, _ := pkgmq.NewChannel(d.mqConn)
        // 直接发到 RabbitMQ
        ch.PublishWithContext(ctx, "", record.QueueName, false, false,
            amqp.Publishing{
                ContentType: "application/json",
                DeliveryMode: amqp.Persistent,
                Body:         []byte(record.Body),
            })
        // 标记为已发送
        now := time.Now()
        d.db.Model(&record).Update("published_at", &now)
    }
}
```

**④ 在 main.go 中启动 Dispatcher**：

```go
// services/reservation/cmd/main.go
outboxStore := outbox.NewStore(db)
outboxDispatcher := outbox.NewDispatcher(db, mqConn)
outboxDispatcher.Start()  // 后台 goroutine 开始轮询
```

#### 完整数据流

```
POST /reserve
    │
    ├── Redis Lua（原子扣库存+创建预订）← 这一步成功，预订就成立了
    │
    ├── DB INSERT reservation（Saga 状态）┐
    └── DB INSERT outbox × 2             │  同在一个本地上下文，
                                          │  写入后 handler 返回 200
                                          │
                    ┌─────────────────────┘
                    │  Outbox Dispatcher（500ms 轮询）
                    ▼
              RabbitMQ → Payment → Order
```

**even if MQ 在 Outbox Dispatcher 发送时失败**，500ms 后下一轮轮询会重试——因为 `published_at` 还是 NULL，消息不会丢。

#### 关键设计点

| 决策 | 原因 |
|------|------|
| 500ms 轮询 | 不是实时但够快；用定时器比 channel 通知简单可靠 |
| LIMIT 20 批量发送 | 防止一次性查太多导致内存占用 |
| 不删记录，标记 published_at | 保留发送历史，方便排查"消息是否发出" |
| Outbox 存在 Reservation 的 PostgreSQL | 和 Reservation 表同一个 DB，可以用事务保证原子性 |

### 阶段四总结

```
改造前:
  Reservation → MQ (可能丢)
  Payment 直接操作 Redis (跨服务写数据)
  Order 直接读 Redis (跨服务读数据)

改造后:
  Reservation → Outbox → Dispatcher → MQ (可靠投递)
  Payment 只发 Saga 消息 → Reservation Saga Consumer (Reservation 自己改自己的 Redis)
  Order 从 MQ 消息体拿数据 (不读 Redis)
```

两个模式配合使用：**Outbox 保证消息不丢，Saga 保证跨服务数据最终一致。**

---

## 阶段五：安全

两件事：
1. **JWT 认证 + User 服务** — 谁可以调用 API
2. **Swagger API 文档** — 让调用方知道 API 长什么样

---

### 第一讲：JWT 认证 + User 服务

#### 问题：任何人都能调用 `/reserve`

改造前，reservation 的 endpoint 是完全开放的：

```
POST /reserve → 直接处理，不验证身份
```

`user_id` 居然是**请求参数里传的**：

```go
type ReserveRequest struct {
    UserID     uint `json:"user_id"`     // ← 谁都可以填任意值
    ShowtimeID uint `json:"showtime_id"`
}
```

你可以 `curl -d '{"user_id":999,"showtime_id":1}'` 冒充任何人。这在真实系统里显然不行——user_id 应该从**身份凭证**里提取，而不是让调用方自己填。

#### JWT 是什么

JWT（JSON Web Token）是一个加密签名的 JSON，可以安全地在各方之间传递。三个部分用 `.` 分隔：

```
eyJhbGciOiJIUzI1NiJ9.eyJ1c2VyX2lkIjoxfQ.signature
│                      │                │
Header (算法)           Payload (用户信息)  Signature (签名)
```

关键特性：**签名保证 Payload 没有被篡改。** 如果中间人改了 `user_id`，签名就对不上了。

#### 代码：User 服务

我们新建了第 4 个微服务。它只有两个端点：

**① 注册** — bcrypt 哈希密码，存储到 `user_db`：

```go
// services/user/internal/handler/handler.go
func (s *Service) Register(c *gin.Context) {
    var req struct {
        Username string `json:"username" binding:"required,min=3,max=32"`
        Password string `json:"password" binding:"required,min=6"`
    }

    hash, _ := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
    // bcrypt cost=10 → 约 100ms 哈希时间，暴力破解成本极高

    user := model.User{Username: req.Username, PasswordHash: string(hash)}
    s.DB.Create(&user)
    // 返回 {user_id: 1, username: "test"}
}
```

**② 登录** — 校验密码，签发 JWT：

```go
func (s *Service) Login(c *gin.Context) {
    var user model.User
    s.DB.Where("username = ?", req.Username).First(&user)

    // bcrypt 验证：把输入的密码哈希后和存储的哈希比对
    bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))

    // 签发 JWT，24 小时过期
    token, _ := auth.GenerateToken(user.ID, user.Username)
}
```

**③ JWT 生成**：

```go
// services/user/internal/auth/jwt.go
func GenerateToken(userID uint, username string) (string, error) {
    claims := Claims{
        UserID:   userID,
        Username: username,
        RegisteredClaims: jwt.RegisteredClaims{
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
            IssuedAt:  jwt.NewNumericDate(time.Now()),
        },
    }
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString(jwtSecret)  // HMAC-SHA256 签名
}
```

#### 代码：JWT 验证中间件

User 服务负责**签发** token，但 Reservation 需要**验证** token。所以把验证逻辑放在 `pkg/auth/`——可以让多个服务复用：

```go
// pkg/auth/jwt.go
func GinMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        auth := c.GetHeader("Authorization")
        // "Bearer eyJhbGci..."

        parts := strings.SplitN(auth, " ", 2)
        if len(parts) != 2 || parts[0] != "Bearer" {
            c.JSON(401, gin.H{"error": "Invalid authorization format"})
            c.Abort()
            return
        }

        claims, err := ParseToken(parts[1])
        if err != nil {
            c.JSON(401, gin.H{"error": "Invalid or expired token"})
            c.Abort()
            return
        }

        // 把用户信息注入 context，后续 handler 可以直接用
        c.Set("user_id", claims.UserID)
        c.Set("username", claims.Username)
        c.Next()
    }
}
```

#### Reservation 怎么接入

中间件链：**JWT 验证 → 限流 → 业务逻辑**：

```go
// services/reservation/cmd/main.go
api := r.Group("/")
api.Use(auth.GinMiddleware())                  // ① 先验证 JWT
api.Use(handler.RateLimitMiddleware(10000))    // ② 再限流
api.POST("/reserve", svc.HandleReserve)        // ③ 业务处理
```

为什么 JWT 在限流前面？因为不合法请求根本不应该消耗限流令牌。

#### 完整认证流程

```
① 注册
POST /register {"username":"test","password":"123456"}
    → User 服务 → bcrypt 哈希 → INSERT user_db → {user_id:1}

② 登录
POST /login {"username":"test","password":"123456"}
    → User 服务 → 校验密码 → 签发 JWT → {token:"eyJh..."}

③ 调用业务 API
POST /reserve {"showtime_id":1}
    Authorization: Bearer eyJh...
    → JWT 中间件 → 解密 token → c.Set("user_id", 1)
    → 限流 → 业务逻辑
```

#### 关键设计点

| 决策 | 原因 |
|------|------|
| User 服务独立 | 认证是独立领域，不该和预订逻辑耦合 |
| JWT 验证在 pkg/auth | User 签发、Reservation 验证——两个服务共用同一套验证逻辑 |
| HS256 对称加密 | 学习项目够了。生产环境用 RS256（非对称），只有认证服务有私钥 |
| 密码用 bcrypt | 比 SHA256 慢很多，暴力破解成本高。cost=10 每次哈希约 100ms |
| `user_id` 从 token 里取 | 不再信任请求参数里的 user_id，防止冒充 |
| 24 小时过期 | 平衡安全性和用户体验 |

---

### 第二讲：Swagger API 文档

#### 问题：别人怎么知道你的 API 怎么调

不给文档的话，调用方需要看代码才知道有哪些端点、参数长什么样、返回什么。Swagger 的作用是**用注解自动生成可交互的 API 文档**。

#### 代码：Swaggo 注解

`swaggo/swag` 从 Go 代码的**注释**里提取 API 信息，生成 `swagger.json`。注解写在 handler 的函数声明上方：

```go
// services/reservation/internal/handler/reserve.go

// HandleReserve godoc
//
//	@Summary		Reserve a ticket
//	@Description	Reserve a ticket for a showtime. Requires Bearer token.
//	@Tags			reservation
//	@Accept			json
//	@Produce		json
//	@Param			request	body		ReserveRequest	true	"Reservation request"
//	@Success		200		{object}	ReserveResponse
//	@Failure		400		{object}	ErrorResponse
//	@Failure		401		{object}	ErrorResponse    ← 未认证
//	@Failure		409		{object}	ErrorResponse    ← 票卖完/已购
//	@Failure		429		{object}	ErrorResponse    ← 限流
//	@Failure		503		{object}	ErrorResponse    ← 熔断
//	@Security		BearerAuth
//	@Router			/reserve [post]
func (s *Service) HandleReserve(ctx *gin.Context) { ... }
```

**main.go 上的全局注解**：

```go
//	@title			Flash Sale API
//	@version		1.0
//	@host			localhost:4000
//	@BasePath		/
//	@securityDefinitions.apikey	BearerAuth  ← 定义 JWT 认证方式
//	@in							header
//	@name						Authorization
```

**生成 + 暴露**：

```bash
swag init -g cmd/main.go -o docs/     # 从注解生成 swagger.json
```

```go
// main.go 里暴露 Swagger UI
r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
// 访问 http://localhost:4000/swagger/index.html 就能看到可交互文档
```

#### Swagger UI 的价值

不仅可看，还能**直接调**——在页面上填参数、点 "Execute"，就能发请求。Swagger 会根据 `@Security BearerAuth` 注解自动加上 Authorization header 输入框。

### 阶段五总结

```
改造前:
  POST /reserve {"user_id": 任意值}    ← 无认证，可以冒充

改造后:
  POST /register → 注册用户
  POST /login    → 拿到 JWT token
  POST /reserve + Bearer token → JWT 验证 → 从 token 里提取 user_id
  GET /swagger/*any → 交互式 API 文档
```

---

## 阶段六：Kubernetes 部署 & CI/CD

最后一块拼图：**把服务从本地 Docker Compose 搬到生产级的容器编排平台，并建立自动化流水线。**

---

### 第一讲：Kubernetes 部署

#### 为什么需要 K8s？Docker Compose 不行吗？

Docker Compose 做的事：在一台机器上 `docker compose up` 启动所有容器。

Kubernetes 做的事：在多台机器组成的**集群**上，自动调度、扩缩容、自愈、滚动更新。

对比一下：

| 能力 | Docker Compose | Kubernetes |
|------|:---:|:---:|
| 启动容器 | ✓ | ✓ |
| 多机器集群 | ✗ | ✓ |
| 自动重启挂了容器 | ✓ (restart: on-failure) | ✓ |
| 滚动更新（零停机） | ✗ | ✓ |
| 自动扩缩容 | ✗ | ✓ (HPA) |
| 服务发现 | ✗ (靠容器名 DNS) | ✓ (内置 DNS + Service) |
| 配置管理 | .env 文件 | ConfigMap + Secret |
| 存储管理 | named volume | PersistentVolumeClaim |

Docker Compose 适合**开发测试**，K8s 是**生产标准**。

#### K8s 的核心抽象

在 Kubernetes 里，我们写的那些 YAML 文件，每个种类解决不同的问题：

| 资源 | 做什么 | 类比 |
|------|--------|------|
| **Namespace** | 资源隔离 | 项目文件夹 |
| **Deployment** | 管理 Pod 副本（自动重启、滚动更新） | 进程管理器 |
| **Service** | 给 Pod 分配固定 IP 和 DNS 名 | 负载均衡器 |
| **ConfigMap** | 非敏感配置 | 配置文件 |
| **Secret** | 密码、密钥 | 加密的配置文件 |

#### 代码：Namespace 隔离

```yaml
# k8s/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: flash-sale
```

所有资源放在 `flash-sale` namespace 下，和系统其它应用隔开。`kubectl delete namespace flash-sale` 一键清理整个项目。

#### 代码：基础设施——PostgreSQL、Redis、RabbitMQ

以 reservation 的 PostgreSQL 为例：

```yaml
# k8s/infrastructure.yaml

# Service：给 Pod 分配稳定 DNS
apiVersion: v1
kind: Service
metadata:
  name: postgres-reservation    # ← 集群内 DNS: postgres-reservation.flash-sale.svc
spec:
  selector:
    app: postgres-reservation   # ← 代理这些 Pod
  ports:
    - port: 5432

---
# Deployment：管理单副本 Pod
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres-reservation
spec:
  replicas: 1                   # ← DB 通常 1 副本（多副本需要主从同步）
  selector:
    matchLabels:
      app: postgres-reservation
  template:
    spec:
      containers:
        - name: postgres
          image: postgres:13
          env:
            - name: POSTGRES_DB
              value: reservation_db
            - name: POSTGRES_USER
              value: flash_sale_user
            - name: POSTGRES_PASSWORD
              value: flash-sale
```

**Service + Deployment 的配合**是 K8s 的精髓：

- Deployment 管理 Pod 的生命周期（挂了就创建新的，新 Pod IP 会变）
- Service 给一组 Pod 分配**固定的集群内 DNS**（不管 Pod IP 怎么变，DNS 不变）

#### 代码：业务服务

以 reservation 为例：

```yaml
# k8s/services.yaml

---
apiVersion: v1
kind: ConfigMap
metadata:
  name: flash-sale-env
data:
  CACHE_URL: "redis:6379"
  RABBIT_MQ_URL: "amqp://guest:guest@rabbitmq:5672/"

---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: reservation
spec:
  replicas: 2                             # ← 2 个副本，K8s 自动调度到不同节点
  selector:
    matchLabels:
      app: reservation
  template:
    spec:
      containers:
        - name: reservation
          image: flash-sale/reservation:latest
          env:
            - name: DATABASE_DSN
              value: "host=postgres-reservation user=flash_sale_user ..."
              # ↑ host 直接写 Service 名，K8s DNS 自动解析
            - name: CACHE_URL
              valueFrom:
                configMapKeyRef:          # ← 从 ConfigMap 引用，不改 Deployment
                  name: flash-sale-env
                  key: CACHE_URL
          ports:
            - containerPort: 4000

---
apiVersion: v1
kind: Service
metadata:
  name: reservation
spec:
  selector:
    app: reservation
  ports:
    - port: 4000
      targetPort: 4000
  type: NodePort                            # ← 外部可访问（生产用 LoadBalancer 或 Ingress）
```

#### 四个服务的副本和类型

| 服务 | replicas | Service type | 原因 |
|------|----------|-------------|------|
| reservation | 2 | NodePort | 有 HTTP 端点，外部需访问 |
| user | 2 | NodePort | 同上 |
| payment | 2 | ClusterIP | MQ 消费者，无需外部访问 |
| order | 2 | ClusterIP | 同上 |

#### ConfigMap vs 硬编码

`DATABASE_DSN` 直接写在 Deployment 的 env 里（每个服务不同，改了要重新 deploy），`CACHE_URL` 和 `RABBIT_MQ_URL` 放在 ConfigMap 里（多个服务共享，改一次全部生效）：

```yaml
# 共享配置 → ConfigMap
CACHE_URL: "redis:6379"

# 服务独有配置 → 写在 Deployment 里
DATABASE_DSN: "host=postgres-reservation ..."
```

#### 部署命令

```bash
kubectl apply -f k8s/namespace.yaml
kubectl apply -f k8s/infrastructure.yaml
kubectl apply -f k8s/services.yaml

# 查看状态
kubectl -n flash-sale get all

# 扩容 reservation 到 5 副本
kubectl -n flash-sale scale deployment/reservation --replicas=5
```

---

### 第二讲：GitHub Actions CI/CD

#### 为什么需要 CI/CD

你现在是这样部署的：

```
改代码 → 本地 go build → 本地 docker build → docker push → 服务器 pull → 重启
```

每次手动操作，容易漏步骤。CI/CD 自动化这条流水线：**push 代码 → GitHub Actions 触发 → 自动构建、测试、打包镜像、推送。**

#### 代码：GitHub Actions Workflow

```yaml
# .github/workflows/ci.yml
name: CI

on:
  push:
    branches: [main]         # ← 推到 main 时触发
  pull_request:
    branches: [main]         # ← 提 PR 到 main 也触发

jobs:
  # ── Job 1: 构建和测试 ──
  build-and-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4          # ① 拉代码

      - uses: actions/setup-go@v5          # ② 装 Go
        with:
          go-version: "1.25"

      - name: Build all services           # ③ 编译 4 个服务
        run: |
          GOWORK=$PWD/go.work go work sync
          go build ./services/reservation/...
          go build ./services/payment/...
          go build ./services/order/...
          go build ./services/user/...

      - name: Run tests                    # ④ 运行并发测试
        run: go test -v ./test/...

  # ── Job 2: 构建 Docker 镜像（仅 main 分支 push）──
  docker:
    needs: build-and-test                 # ← 测试过了才构建
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    strategy:
      matrix:
        service: [reservation, payment, order, user]  # ← 4 个并行 job
    steps:
      - uses: actions/checkout@v4

      - name: Login to GHCR               # ① 登录 GitHub Container Registry
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build and push              # ② 构建并推送
        uses: docker/build-push-action@v5
        with:
          context: .                         # 项目根目录
          file: ./services/${{ matrix.service }}/Dockerfile
          push: true
          tags: ghcr.io/${{ github.repository }}/${{ matrix.service }}:latest
```

#### 流水线流程

```
git push main
    │
    ▼
┌──────────────┐
│ build-and-test│ ← 并行编译 4 服务 + 跑测试
└──────┬───────┘
       │ 全部通过
       ▼
┌──────────────┐
│   docker      │ ← 并行构建 4 个 Docker 镜像，推送到 ghcr.io
│   (matrix)    │    ghcr.io/qs-lzh/flash-sale/reservation:latest
│               │    ghcr.io/qs-lzh/flash-sale/payment:latest
│               │    ghcr.io/qs-lzh/flash-sale/order:latest
│               │    ghcr.io/qs-lzh/flash-sale/user:latest
└──────────────┘
```

#### 关键设计点

| 决策 | 原因 |
|------|------|
| `needs: build-and-test` | 测试不通过就不构建镜像，防止推送坏代码 |
| `strategy.matrix` | 4 个服务的 Docker 构建**并行**跑，节省时间 |
| `if: github.ref == 'refs/heads/main'` | 只有 main 分支 push 才推镜像，PR 只跑测试 |
| GHCR 不用 Docker Hub | GitHub 内置，不需要额外注册账号和 secret |
| 用 `secrets.GITHUB_TOKEN` | GitHub 自动注入的 token，不用自己配 |

### 阶段六总结

```
改代码前（Docker Compose）:
  ┌─────────────┐
  │ docker-compose│  ← 一台机器，手动 restart
  │ 7 个容器     │
  └─────────────┘

改代码后（K8s + CI/CD）:
  git push main
     │
     ▼
  GitHub Actions
     ├── go build × 4 (并行)
     ├── go test (并发测试)
     └── docker build × 4 + push (matrix 并行)
           │
           ▼
  kubectl apply -f k8s/
     │
     ▼
  ┌─────────────────────────────────────┐
  │  K8s Cluster                        │
  │  reservation × 2   payment × 2      │
  │  user × 2          order × 2        │
  │  postgres × 3      redis × 1        │
  │  rabbitmq × 1       consul × 1      │
  └─────────────────────────────────────┘
```

---

## 全阶段总结

这 20 项改进的脉络：

```
阶段零: 单体 → 3 服务 + MQ 异步通信
阶段一: 共享库 + Consul + Kong + 配置中心 ← 基础设施骨架
阶段二: 日志 + 指标 + 追踪 + 健康检查   ← 可观测性，从黑盒变白盒
阶段三: 熔断 + 重试 + 限流              ← 弹性，系统能扛住故障
阶段四: Saga + Outbox                   ← 分布式事务，数据最终一致
补充:   Redis 解耦 + Payment/Order 独立  ← 服务数据边界清晰
阶段五: JWT + User 服务 + Swagger       ← 安全 + API 文档
阶段六: K8s manifests + GitHub Actions  ← 容器编排 + 自动化
```

从一个简单的 3 服务 MQ 架构，到 4 服务、独立存储、可观测、有弹性、事务可靠、安全认证、K8s 部署的完整微服务项目。每个阶段都是在前一阶段的基础上叠加，没有推倒重来。

### 最终架构全景

```
                        Kong API Gateway (:8000)
                        /    |    \    \
                       /     |     \    \
                 ┌────────┐  │  ┌────────┐  ┌────────┐
                 │  User  │  │  │Payment │  │ Order  │
                 │ :4003  │  │  │ :4001  │  │ :4002  │
                 │/register│  │  │(MQ)    │  │(MQ)    │
                 │/login  │  │  └───┬────┘  └───┬────┘
                 └───┬────┘  │      │           │
                     │       │      │  Saga     │
            JWT      │       │      └─────┐     │
         ┌───────────┘       │            │     │
         ▼                   ▼            ▼     ▼
    ┌─────────────────────────────────────────────────┐
    │              Reservation (:4000)                 │
    │  /reserve /health /ready /metrics /swagger      │
    │  JWT + RateLimit(令牌桶) + CB(Redis)             │
    │  Outbox → 异步投递 MQ                            │
    │  Saga consumer ← 接收 PAID/TIMEOUT 状态更新      │
    └────────────────┬────────────────────────────────┘
                     │
         ┌───────────┼───────────┐
         ▼           ▼           ▼
   ┌─────────┐ ┌────────┐ ┌─────────┐
   │ Consul  │ │ Redis  │ │RabbitMQ │
   │注册+配置│ │        │ │  4 队列 │
   └─────────┘ └────────┘ └─────────┘
```

### 6 阶段 20 项改进清单

| # | 阶段 | 改进项 |
|---|------|--------|
| 1 | 一 | 共享库 pkg/ |
| 2 | 一 | gRPC（跳过） |
| 3 | 一 | Consul 服务注册 |
| 4 | 一 | Kong API 网关 |
| 5 | 一 | Consul KV 配置中心 |
| 6 | 二 | zap 结构化日志 |
| 7 | 二 | 健康检查 & 优雅关闭 |
| 8 | 二 | Prometheus 指标 |
| 9 | 二 | Jaeger 链路追踪 |
| 10 | 三 | 熔断器 (gobreaker) |
| 11 | 三 | MQ 重试退避 |
| 12 | 三 | 令牌桶限流 |
| 13 | 四 | Saga 模式 |
| 14 | 四 | Outbox 模式 |
| 15 | 五 | JWT + User 服务 |
| 16 | 五 | Swagger 文档 |
| 17 | 六 | K8s manifests |
| 18 | 六 | GitHub Actions CI |
| 19 | 补充 | Redis 解耦 |
| 20 | 补充 | Docker 构建修复 |

# HTTP Proxy Manager v2 — 重构计划

> 当前实现已将运行数据迁移到 SQLite：默认数据库为 `data/http-proxy.db`，
> 保存订阅、解析后的代理、应用配置和测速结果。下文中关于
> `subscriptions.json` 的内容是早期设计记录，不再代表当前存储实现。

## 目标

重写当前 Node.js 项目为 Go + 嵌入 sing-box 库，消除子进程开销，提供性能更好的 HTTP 代理 API 服务。

## 架构概览

```
┌─ 用户 ─────────────────────────────┐
│  Web 浏览器  ←→  HTTP API          │
└─────────────────────────────────────┘
              │
        ┌─────┴──────┐
        │  main.go   │  net/http server
        │  server.go │
        └─────┬──────┘
              │
   ┌──────────┼──────────┬─────────────┐
   ▼          ▼          ▼             ▼
┌──────┐ ┌────────┐ ┌────────┐ ┌──────────┐
│subscription│ │parser │ │tester  │ │  pool    │
│manager │ │.go     │ │.go     │ │  .go     │
│.go     │ │        │ │        │ │          │
└───┬────┘ └────────┘ └───┬────┘ └────┬─────┘
    │                      │           │
    ▼                      ▼           ▼
┌──────────┐         ┌───────────────────────┐
│subscriptions.json   │   sing-box Box 实例    │
│ (持久化)  │         │   (进程内, 零子进程)    │
└──────────┘         └───────────────────────┘
```

## 项目结构

```
http-proxy/
├── main.go                    # 入口，启动 HTTP 服务器
├── go.mod / go.sum
│
├── core/                      # 核心业务逻辑
│   ├── config.go              # 应用配置常量
│   ├── storage.go             # subscriptions.json 读写
│   ├── subscription.go        # 订阅管理 (CRUD / fetch / refresh)
│   ├── parser.go              # trojan:// vless:// ss:// vmess:// 解析
│   ├── tester.go              # 延迟测试 (嵌入 sing-box Box)
│   └── pool.go                # 代理实例池管理
│
├── server/                    # HTTP 服务与路由
│   ├── server.go              # HTTP 路由 + CORS 中间件
│   └── handler.go             # 所有 API handler
│
├── webui/
│   └── index.html             # Web UI (Go embed.FS 内嵌)
│
├── build.bat                  # 构建脚本
└── start.bat                  # 启动脚本
```

## 模块职责

### 1. config.go — 配置

```go
const (
    DefaultProxyCount    = 10
    MaxLatencyMs         = 500
    TestConcurrency      = 3
    TestResultTTL        = 2 * time.Hour   // 测速结果缓存有效期
    PoolPortStart        = 10000
    PoolPortEnd          = 10099
    ListenPort           = 9090
    SubscriptionsFile    = "subscriptions.json"
)
```

### 2. parser.go — 代理解析

将现有 `server.mjs` 中的 `parseProxyUrl`、`parseVless`、`parseTrojan`、`parseSs`、`parseSubscription` 逐行翻译为 Go。

解析结果构造为 `option.Outbound` 结构体，附加元数据：

```go
type ProxyInfo struct {
    option.Outbound
    Name     string `json:"_name"`
    Tag      string `json:"_tag"`
    Protocol string `json:"_protocol"`
    SourceID string `json:"_source_id"`
}
```

### 3. subscription.go — 订阅管理

```go
type Subscription struct {
    ID        string       `json:"id"`
    URL       string       `json:"url"`
    Name      string       `json:"name"`
    Enabled   bool         `json:"enabled"`
    AddedAt   time.Time    `json:"added_at"`
    UpdatedAt time.Time    `json:"updated_at"`
    Proxies   []*ProxyInfo `json:"proxies"`
}
```

方法：
- `Add(url)` — HTTP GET → base64 decode → parse → store
- `Remove(id)` — 删除订阅及其代理
- `Refresh(id)` — 重新 fetch 并 parse，替换 proxies
- `GetAllProxies()` — 聚合所有订阅的去重代理列表

### 4. storage.go — 持久化

文件格式 `subscriptions.json`：

```json
[
  {
    "id": "a1b2c3d4",
    "url": "https://example.com/sub",
    "name": "机场A",
    "enabled": true,
    "added_at": "2026-07-04T10:00:00Z",
    "updated_at": "2026-07-04T10:00:00Z",
    "proxies": [
      {
        "type": "vless",
        "server": "38.244.20.164",
        "server_port": 443,
        "uuid": "...",
        "tls": { "enabled": true },
        "_name": "US-01",
        "_tag": "proxy_38_244_20_164_443",
        "_protocol": "vless",
        "_source_id": "a1b2c3d4"
      }
    ]
  }
]
```

### 5. tester.go — 延迟测试（核心优化点）

**原则：避免频繁 new/destroy Box，而是共享一个长生命周期 Box + 动态切换 Outbound。**

#### 架构思路

```
┌──────────────────────────────────────┐
│         sing-box Box (全局单例)         │
│                                      │
│  outbounds: [                        │
│    { tag: "proxy_us_01", type: "vless", ... },
│    { tag: "proxy_jp_02", type: "trojan", ... },
│    { tag: "proxy_sg_03", type: "ss", ... },
│    ... (所有待测代理)                  │
│  ]                                    │
│  route: { final: "dynamic" }          │
└──────────┬───────────────────────────┘
           │
           │  测试时，不创建/销毁任何资源
           │  只需要：切换 route + dial 测速
           ▼
    ┌──────────────┐
    │ router.DialContext │  ← 无需 inbound 端口！
    │ (直接通过指定    │
    │  outbound 拨号)  │
    └──────────────┘
           │
           ▼
    TCP 连接目标 → HTTP GET → 计时 → 完成
```

#### 启动阶段

```go
// 应用启动时，创建唯一一个全局测试 Box
func NewTester(cfg *AppConfig) *Tester {
    // 创建 Box，包含 SINGLE mixed inbound + 所有已知 outbound
    // inbound: 用于 fallback 测试方式
    // 实际测速优先走 router 直拨
}

// 当订阅刷新/新增代理时，动态更新 Box 的 outbounds
func (t *Tester) SyncOutbounds(proxies []*ProxyInfo) {
    // 将新的代理列表注入到 Box 的 outbound 列表中
}
```

#### 测速缓存（避免重复测速）

每次 `TestAll` 的结果按 `{tag → {latency, err, timestamp}}` 缓存，有效期 2 小时：

```go
type TestResult struct {
    Tag       string    `json:"tag"`
    Latency   int       `json:"latency"`
    Err       error     `json:"-"`
    Timestamp time.Time `json:"timestamp"`
    Stale     bool      `json:"-"`
}

type Tester struct {
    mu        sync.RWMutex
    testBox   *box.Box
    outbounds []option.Outbound
    cache     map[string]*TestResult   // tag → 最近一次测速结果
}

// GetResults 返回所有缓存的测速结果，标识过期项
func (t *Tester) GetResults() []TestResult {
    t.mu.RLock()
    defer t.mu.RUnlock()
    results := make([]TestResult, 0, len(t.cache))
    for _, r := range t.cache {
        r.Stale = time.Since(r.Timestamp) > config.TestResultTTL
        results = append(results, *r)
    }
    return results
}

// refreshCache 增量刷新：只对缓存过期或新增的代理重新测速
func (t *Tester) refreshCache(proxies []*ProxyInfo) {
    // 1. 遍历 proxies，对比 cache
    // 2. 对 stale(过期) 或 missing(新增) 的节点并发测速
    // 3. 写回 cache
    // 4. cache 中已有且未过期的节点直接保留
}
```

#### 测速方式（优先级从高到低）

**方式 B（首选）：`router.DialContext` 直拨**
- 无需任何 inbound 端口
- 从 `box.Router()` 获取 Router
- `router.DialContext(ctx, "tcp", "www.gstatic.com:80", &metadata.Metadata{Outbound: tag})`
- 直接通过指定 outbound 建立 TCP 连接
- 发送 HTTP GET 请求，测量响应时间
- **零端口占用，零 inbound 开销，一次 Box 初始化即可测试全部代理**

**方式 A（兜底）：单一 mixed inbound 轮询**
- Box 内配置一个固定的 mixed inbound（如 127.0.0.1:21000）
- 测试时只需动态修改 route 的 final outbound 指向目标 tag
- TCP 连接该 inbound 完成测试
- 无需反复创建/销毁 Box，但需要占用一个端口

```
100 个代理测速 (concurrency=5)：
  当前 Node:  每次 spawn sing-box(~1.5s) + 等待(~0.5s) + 测试(~0.3s) = ~2.3s × 20 批 = ~46s
  方式 A (单 Box + 动态 inbound):  Box初始化(0.1s 仅一次) + 测试(~0.3s) × 100 = ~30s
  方式 B (router.DialContext):     Box初始化(0.1s 仅一次) + 测试(~0.2s) × 100 = ~20s
```

### 6. pool.go — 代理实例池

```go
type PoolInstance struct {
    Proxy     *ProxyInfo
    Port      int
    Box       *box.Box
    Latency   int
    StartedAt time.Time
}
```

#### 端口复用与热替换

**问题**：StopPool 释放端口后，OS 可能进入 TIME_WAIT 状态，紧接着 StartPool 尝试 Listen 同一端口会报 "port already in use"。

**方案：Hot Swap（差集替换）**

```
旧池: [A:10000, B:10001, C:10002]     ← 正在运行
新池: [B:10001, C:10002, D:10003]     ← 期望的新列表

差集计算:
  保留: B(10001), C(10002)   ← tag+port 匹配，不动
  移除: A(10000)             ← 旧池有，新池无 → 关闭即可
  新增: D(10003)             ← 新池有，旧池无 → 新分配端口启动

结果: 原有连接不中断，仅停 A 开 D
```

实现：

```go
// HotSwap(proxies, latencies)  — 差集替换，零停机
func (p *ProxyPool) HotSwap(proxies []*ProxyInfo, latencies map[string]int) {
    p.mu.Lock()
    defer p.mu.Unlock()

    oldByTag := make(map[string]*PoolInstance)
    for _, inst := range p.instances {
        oldByTag[inst.Proxy.Tag] = inst
    }

    newInstances := make([]*PoolInstance, 0, len(proxies))

    // 1. 遍历新列表，保留匹配项
    for i, proxy := range proxies {
        port := p.config.PoolPortStart + i
        if old, ok := oldByTag[proxy.Tag]; ok {
            // tag 匹配 → 保留，更新 latency
            old.Latency = latencies[proxy.Tag]
            newInstances = append(newInstances, old)
            delete(oldByTag, proxy.Tag) // 移除出待关闭列表
        } else {
            // 新节点 → 创建 Box 监听
            box := p.startProxyBox(proxy, port, latencies[proxy.Tag])
            newInstances = append(newInstances, box)
        }
    }

    // 2. 关闭不再需要的旧节点
    for _, old := range oldByTag {
        old.Box.Close()
    }

    p.instances = newInstances
}
```

**保底**：当 HotSwap 计算发现端口被 TIME_WAIT 占用时，尝试将新节点分配到空闲端口（PoolPortStart 以上第一个可用端口），而非硬绑定位置序号。

**so_reuseaddr**：在 sing-box inbound 配置中启用 `SO_REUSEADDR`（system 级 socket 选项），加快 TIME_WAIT 状态端口的复用。

### 7. server.go — HTTP 路由

使用标准库 `net/http`，无需第三方框架。

| Method | Path | 说明 |
|---|---|---|
| GET | `/` | Web UI |
| GET | `/api/subscriptions` | 列出所有订阅 |
| POST | `/api/subscriptions` | 添加订阅 `{url}` |
| DELETE | `/api/subscriptions/{id}` | 删除订阅 |
| POST | `/api/subscriptions/{id}/refresh` | 刷新订阅 |
| GET | `/api/proxies` | 列出所有已解析代理 |
| **GET** | **`/api/proxies/available?count=5`** | **核心 API** |
| GET | `/api/pool/status` | 池状态 |
| POST | `/api/pool/stop` | 停止池 |
| GET | `/api/config` | 获取配置 |
| PUT | `/api/config` | 更新配置 |

### 8. 核心 API 流程

```
GET /api/proxies/available?count=5

1. subMgr.GetAllProxies()         → 获取所有代理

2. tester.GetResults()            → 取缓存测速结果
   ├─ 全部未过期? 直接跳到步骤3
   ├─ 有过期/新增? → tester.refreshCache(proxies)
   │   只对 stale 和 missing 的节点并发测速
   │   已有且未过期的节点直接保留，不重复测
   └─ 返回 [{tag, latency, err, stale}]

3. 健康过滤 (health gate)
   条件: latency < 500ms && err == nil
   通过者 → 按延迟升序排序
   通过者 < count? → 返回 503 或 partial

4. 优质节点滑动窗口 (sliding window)
   window = min(len(健康列表), count * 3)
   candidates = 健康列表[0:window]
   目的: 把绝对低延迟放宽到体感无差异范围, 为随机性提供基数

5. 内存局部洗牌 (shuffle)
   rand.Shuffle(len(candidates), swap)
   目的: 窗口内每个节点均等概率被选中, 避免每次都固定返回那两三个

6. 截取 top count
   selected = candidates[0:count]

7. pool.HotSwap(selected, latencyMap)
   差集比对 → 保留匹配 (零中断) → 关移除 → 启新增

8. return {
     proxies: [{
       http: "http://127.0.0.1:10000",
       name: "US-01",
       latency: 123,
       protocol: "vless"
     }],
     count: 5
   }
```

### 9. Web UI — index.html

独立 HTML 文件，通过 `//go:embed` 内嵌：

```go
//go:embed webui/index.html
var webUI embed.FS
```

4 个 Tab：
1. **📡 订阅管理** — 添加/删除/刷新订阅 URL
2. **🔌 代理列表** — 所有已解析代理，搜索/测试
3. **🌐 代理池** — 当前运行的 HTTP 代理，停止按钮
4. **⚙️ 设置** — API 使用说明、前置代理配置

## 数据流

```
用户提交订阅 URL
    → subscription.Add(url)
        → HTTP GET url
        → base64 decode
        → parser.ParseSubscription(text)
        → subscriptions.json 写入
    → 完成

外部调用 API 获取代理
    → GET /api/proxies/available?count=5
        → subscription.GetAllProxies()
        → tester.GetResults()
            ├─ 全部未过期? 直接跳过测速
            └─ 有过期/新增? → refreshCache(proxies)
                → 对 stale 和 missing 增量并发测速
                → router.DialContext 并发拨号各 outbound
                → 直接通过 sing-box 内部路由测速
                → 0 端口占用, 0 Box 创建开销
                → 更新缓存
        → 健康过滤: latency < 500ms && 无错误
        → 滑动窗口: min(健康数, count × 3)
        → 原地洗牌: rand.Shuffle(candidates)
        → 截取前 count 个
        → pool.HotSwap(selected)         → 差集替换
            → 旧池和新池比对 tag+port
            → 保留匹配的实例（零中断）
            → 关闭不再需要的实例
            → 仅新节点启动 Box: inbound mixed + outbound
            → 监听对应端口
        → 返回 HTTP 代理地址列表
```

## 并发安全设计

### 竞争场景分析

```
时间线 ──────────────────────────────────────────────>
                                                    │
  Goroutine A:  GET /api/proxies/available?count=5   │
               ├─ GetAllProxies()      ← 读 proxies  │
               ├─ TestAll()            ← 遍历 outbounds
               ├─ pool.HotSwap()       ← 差集替换    │
               │   ├─ 找出"保留"节点   ← 无操作       │
               │   ├─ 关闭"移除"节点   ← 仅关这些    │
               │   └─ 启动"新增"节点   ← 仅开这些    │
               └─ 返回结果                            │
                                                    │
  Goroutine B:  POST /api/subscriptions (刷新订阅)    │
               ├─ 修改 subs → Save()   ← 写 proxies  │
               └─ SyncOutbounds()      ← 改 outbounds│
                                                    ▼
                              ↑ 数据竞态 ↑ (无端口竞态)
```

具体竞态：

| # | 场景 | 后果 |
|---|---|---|
| 1 | A 读 proxies 时 B 写 proxies | 读脏数据 / panic (slice append) |
| 2 | A 遍历 outbound 列表测速时 B 更新 SyncOutbounds | 漏测 / 测错对象 |
| 3 | 两次并发 `GET /api/proxies/available` | 端口抢占, 重复创建 Box |
| 4 | A 正在 StartPool 建 Box 时 B 来 StopPool | 孤儿进程 / 端口泄漏 |

### 锁分层策略

```
级别          锁                     保护对象
─────────────────────────────────────────────────
L0     atomic.Bool (or chan struct{})  测速+重建池的排他执行权
L1     subscription.RWMutex            proxies 列表 + subscriptions 文件
L2     tester.RWMutex                  outbound 列表 + 测试 Box 引用
L3     pool.RWMutex                    instances 列表 + 端口分配表
```

#### L0 — 操作锁（排他信号量）

```go
var busy atomic.Bool

// GET /api/proxies/available
if !busy.CompareAndSwap(false, true) {
    // 正在处理中，返回上次缓存的结果（或 409 Too Many Requests）
    return cachedResult
}
defer busy.Store(false)
```

保证**同时只有一个人**在执行测速+重建池的完整流程。后续请求直接复用缓存，既不阻塞也不重复计算。

#### L1-L3 — 数据锁（读写锁）

```go
// subscription.go
type SubscriptionManager struct {
    mu   sync.RWMutex
    subs []*Subscription
}

func (m *SubscriptionManager) GetAllProxies() []*ProxyInfo {
    m.mu.RLock()
    defer m.mu.RUnlock()
    // ...
}

func (m *SubscriptionManager) Add(url string) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    // ...
}
```

### 锁获取顺序（防止死锁）

```
所有代码必须遵守:  L0 → L1 → L2 → L3
                  不能反向获取

合法:  handler.GetAvailable → L0 → L1.RLock → L2.RLock → L3.Lock
合法:  handler.RefreshSub  → L1.Lock → L2.Lock
非法:  tester.SyncOutbounds → L2.Lock → L1.Lock  (反向，会造成死锁)
```

如果 SyncOutbounds 需要读 subscription 的数据，必须先释放 L2 锁，或把所需数据作为参数传入。

### 各模块锁细则

#### subscription.go

```go
type SubscriptionManager struct {
    mu   sync.RWMutex
    subs []*Subscription
}

// Add / Remove / Refresh: mu.Lock()
// GetAllProxies / List:    mu.RLock()
// Save():  mu.RLock() (不阻塞读，只阻塞写)
```

#### tester.go

```go
type Tester struct {
    mu       sync.RWMutex
    testBox  *box.Box
    outbounds []option.Outbound
    cache    map[string]*TestResult   // tag → 测速缓存, 2h TTL
}

// SyncOutbounds: mu.Lock() → 重建 outbounds 列表 + 更新 Box
// GetResults:    mu.RLock() → 读 cache
// refreshCache:  mu.Lock() → 增量测速过期/新增节点 → 写 cache
```

#### pool.go

```go
type ProxyPool struct {
    mu        sync.RWMutex
    instances []*PoolInstance
    ports     map[int]bool       // 端口占用表
}

// HotSwap:  mu.Lock() → 差集比对 → 关移除节点 → 启新增节点
// StopPool: mu.Lock() → 停所有 → 释放端口
// GetStatus: mu.RLock()
```

### 缓存机制

```go
type CacheEntry struct {
    Result    []AvailableProxy
    Timestamp time.Time
    Count     int      // 请求的 count 值，不同 count 不能复用
}
```

- 当 `busy` 为 true 时，后续请求直接返回 `CacheEntry`
- 当 `busy` 从 false→true 时，在读锁保护下取 proxies，测速重建池完成后写入缓存
- 缓存有效期：`config.CacheTTL` (默认 30s)，超时后即使 busy=false 也必须重新测速

### 时序修正

```
Goroutine A:  GET /api/proxies/available
              L0.CAS(true)
              L1.RLock → 取 proxies
              L1.RUnlock
              L2.RLock → 遍历 outbounds 测速
              L2.RUnlock
              L3.Lock → HotSwap (差集比对 → 关移除 → 启新增)
              L3.Unlock
              写缓存
              L0.Store(false)

Goroutine B:  POST /api/subscriptions (来晚了)
              L1.Lock → 更新 subs + Save
              L1.Unlock
              L2.Lock → SyncOutbounds (更新测试 Box)
              L2.Unlock
              成功后清空缓存 (让下次 /available 重新测速)
```

## 分期交付计划

每个阶段产出可独立验证的工作成果，避免单次周期过长导致任务降级。

---

### 第一期：骨架 + 代理解析（1天）

**目标**：项目框架跑通，能解析订阅并持久化，Web UI 可查看。

| 交付物 | 内容 | 验收标准 |
|---|---|---|
| `go.mod` + `main.go` | Go module 初始化，HTTP server 启动 | `go build .` 通过，访问 `http://127.0.0.1:9090` 返回页面 |
| `core/config.go` | 配置常量 | — |
| `core/parser.go` | `parseProxyUrl` / `parseSubscription` — 翻译现有 JS 解析逻辑 | 读入 sub.txt 能解析出 ProxyInfo 列表 |
| `core/storage.go` | `subscriptions.json` 读写 | 启动时自动创建空文件，写入后能读出 |
| `core/subscription.go` | `Add(url)` — fetch + decode + parse + save | 调用后 subs 写入文件 |
| `server/server.go` + `handler.go` | 路由框架 + `/api/subscriptions` `/api/proxies` GET | curl 能看到订阅和代理列表 |
| `webui/index.html` | Tab 1（订阅管理） + Tab 2（代理列表） | 页面加载显示数据 |

**可演示场景**：打开 Web UI → 输入订阅 URL → 提交 → 看到解析出的代理列表。

---

### 第二期：延迟测试 + 缓存（1天）

**目标**：能测速并缓存结果，实现健康过滤 → 滑动窗口 → 洗牌 → 截取的完整管道。

| 交付物 | 内容 | 验收标准 |
|---|---|---|
| `core/tester.go` | 全局单例 Box + `router.DialContext` 直拨 | 测速 10 个代理 < 3s，无需 inbound 端口 |
| 测速缓存 | `GetResults` / `refreshCache` 增量刷新，2h TTL | 第二次请求不触发测速 |
| 管道逻辑 | 健康过滤 + 滑动窗口 + Shuffle + 截取 | API 返回的节点每次不同 |

**可演示场景**：`curl /api/proxies/available?count=3` → 返回 3 个 HTTP 代理（仅 JSON，尚无实际监听端口）。

---

### 第三期：代理池 HotSwap + 端口复用（1天）

**目标**：能在本地端口启动/替换 sing-box 实例，连接可用。

| 交付物 | 内容 | 验收标准 |
|---|---|---|
| `core/pool.go` | `HotSwap` 差集替换 + 端口管理 | 重复调用 API，端口不冲突，旧端口不泄漏 |
| inbound 配置生成 | 为每个代理生成 sing-box Options | `curl -x http://127.0.0.1:10000 http://www.google.com` 返回 200 |

**可演示场景**：`curl /api/proxies/available?count=2` → 返回包含 `http://127.0.0.1:10000` 和 `10001`，实际 HTTP 代理可用。

---

### 第四期：并发安全 + 上游代理 + 收尾（1天）

**目标**：生产级健壮性，完整 Web UI。

| 交付物 | 内容 | 验收标准 |
|---|---|---|
| 并发锁 | L0-L3 完整锁层级，`atomic.Bool` 排他 | 并发 10 个请求无竞态、无端口冲突 |
| 上游代理 | 前置代理配置（复用现有代码逻辑） | 启用后所有代理流量经过上游 |
| Web UI 完善 | Tab 3（代理池状态） + Tab 4（设置/API 说明） | 完整 UI 交互 |
| 错误处理 | 健康节点不足时返回 503 + 错误信息 | `count=100` 但仅有 3 个健康节点时给出明确提示 |

**可演示场景**：完整串起来 — 添加订阅 → 调 API 获取可用代理 → 看到不同节点 → 并发调用不崩溃。

---

### 第五期：优化与测试（1天，可选）

| 交付物 | 内容 |
|---|---|
| 性能压测 | 100 节点测速 + 100 并发 API 请求 |
| edge case | 订阅 URL 失效、空列表、网络断开 |
| CI / bat 脚本 | `build.bat` 一键构建，`start.bat` 启动 |

---

### 第六期：UI 可用性增强（1天）

**目标**：全中文界面、交互反馈完善、可配置测速目标、代理列表显示延迟。

| # | 交付物 | 内容 | 验收标准 |
|---|--------|------|----------|
| 1 | 中文界面 | 全部文案中文化：Tab、表头、按钮、toast、空状态、API 说明 | 页面无英文残留 |
| 2 | Pool 刷新状态锁 | 点击刷新时按钮 disabled + 显示"正在刷新..."，完成后恢复 | 刷新期间无法二次点击 |
| 3 | 删除订阅自动刷新 | 删除后等 backend 响应完再刷新双列表，按钮 disabled 防连点 | 删除后订阅列表和代理列表自动更新 |
| 4 | 添加订阅防卡死 | 按钮显示"添加中..."，同时禁用 input 和按钮，完成后恢复 | 网络慢时界面不假死 |
| 5 | 代理列表显示延迟 | 后端返回每个代理的最近测试延迟值，前端加"延迟"列，未测显示"-" | 列表能看到每个代理的延迟 |
| 6 | 可配置测试目标 | Settings 增加"延迟测试目标"输入框（`host:port`），持久化到 config | 改目标后测速立即使用新地址 |

**涉及改动：**

| 文件 | 改动 |
|------|------|
| `core/config.go` | `AppConfig` 加 `TestTarget string` |
| `core/tester.go` | `testTarget` 从 package var 改为 `Tester` 结构体字段；新增 `SetTestTarget(hostPort)` 和 `GetResults() map[string]*TestResult` |
| `service.go` | `SetConfig` 传递 `TestTarget` 到 `tester.SetTestTarget()`；新增 `GetTestResults()` 方法暴露只读缓存 |
| `server/server.go` | Service 接口添加 `GetTestResults()`；`handleProxies` 返回数据附带延迟；WebUI 全部中文 + 交互修复 |

---

**总体估算：6天，分 6 期，每期 1 天交付可验证成果。**

## 依赖

```
go.mod:
  github.com/sagernet/sing-box  v1.13.x
  github.com/sagernet/sing       v0.x
  # HTTP 路由: 标准库 net/http
```

## 与现有项目的差异

| 文件 | Node 现状 | Go 新方案 |
|---|---|---|
| server.mjs | 1000行单体 | 拆分 6 个 Go 文件 |
| proxy2http.mjs | 冗余 CLI | 不需要 |
| sub.txt | 静态文件 | subscriptions.json 动态管理 |
| configs/_test_*.json | 大量临时文件 | 全在内存中 |
| sing-box/sing-box.exe | 需下载 exe | Go module import |
| upstream.json | 独立 | 合入 config |

## 性能预期

```
测速 100 个代理 (concurrency=5)：
  当前 Node (每次 spawn 子进程):           ~46s    瓶颈: 进程创建
  Go + 单Box + router.DialContext (现方案): ~6s   瓶颈: 纯网络延迟
                                                   (100/5=20批 × 0.3s/批 = 6s)

首次延迟 (cold start)：
  当前 Node:  ~2.3s/个 (spawn + init + test)
  Go + 直拨:  ~0.3s/个 (仅网络延迟)

后续延迟 (warm, TLS 会话复用)：
  当前 Node:  ~2.3s/个 (仍要 spawn)
  Go + 直拨:  ~0.15s/个 (TLS 会话复用 + 连接池复用)

端口占用：
  当前 Node:  测速时每代理占 1 端口, 同时 3 个 = 3 端口
  Go + 直拨:  测速时 0 端口占用
```

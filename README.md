# 代理中转器

[![Docker Image CI](https://github.com/Jakens0426/http-proxy/actions/workflows/docker.yml/badge.svg)](https://github.com/Jakens0426/http-proxy/actions/workflows/docker.yml)

嵌入式 sing-box 代理池：将 VLESS/Trojan/SS 订阅转成本地 HTTP 代理，零子进程。

## 架构

```
订阅 → parseVLESS/Trojan/SS → ProxyInfo[]
                                      ↓
           Tester (Box 单例 + DialContext 延迟测试)
                                      ↓
           SelectProxies (健康门→排序→滑窗→打乱→截断)
                                      ↓
           Pool.HotSwap (diff 替换, 保留匹配实例)
                                      ↓
           localhost:10000-10099  (Mixed Inbound)
```

## 经验

### 1. sing-box 作为 Go 库嵌入

核心代码只 import，不需要 sing-box 二进制：

```go
import (
    "github.com/sagernet/sing-box"
    "github.com/sagernet/sing-box/option"
    "github.com/sagernet/sing-box/include"
)

ctx := include.Context(context.Background())
b, err := box.New(box.Options{
    Options: option.Options{...},
    Context: ctx,
})
b.Start()
```

**不要用子进程**——`include.Context()` 注册所有内置 outbound 类型，
`box.New()` 全内存初始化，毫秒级启动。

### 2. 必须加 `-tags with_utls`

如果有 Reality / uTLS outbound，不加此 tag 会 panic：

```
uTLS ... is not included in this build, rebuild with -tags with_utls
```

因为 sing-box 用 Go build tag 控制 uTLS 的编译包含，
减少对不需要 Reality 用户的体积影响。

```bat
go build -tags with_utls -o http-proxy.exe .
```

### 3. Outbound Options 必须是指针

sing-box outbound registry 在 `CreateOutbound` 时对 `Options` 做 type assertion
到具体的 `*option.VLESSOutboundOptions` 等指针类型。
传值会 panic：

```go
// ❌ 崩溃
Options: option.VLESSOutboundOptions{...}

// ✅ 正确
Options: &option.VLESSOutboundOptions{...}
```

同理，`proxyToOutbound()` 返回 `option.Outbound`，其 `Options any`
必须是指针。

### 4. DialerOptions Detour 实现上游代理

所有 outbound 选项结构体都嵌入了 `DialerOptions`（来自 `option/simple.go`），
设置 `Detour` 字段即可让该 outbound 的出站连接经过另一 outbound：

```go
opts := &option.VLESSOutboundOptions{...}
opts.Detour = "upstream"  // 走 tag=upstream 的 outbound

// upstream outbound
{
    Type: C.TypeHTTP,  // 或 C.TypeSOCKS
    Tag:  "upstream",
    Options: &option.HTTPOutboundOptions{
        ServerOptions: option.ServerOptions{Server: "host", ServerPort: 8080},
        Username: "user", Password: "pass",
    },
}
```

因为 `DialerOptions` 是内嵌的，类型断言后直接赋值即可。实测 HTTP/SOCKS5 都可用。

### 5. Pool 端口管理 + TAG 持久化

不要用 position-based 端口分配——HotSwap 会新增/删除/保序，
tag 才是稳定标识：

```go
portByTag map[string]int
usedPorts map[int]bool
```

- 首次出现分配端口，记录 `portByTag[tag] = port`
- 后续 HotSwap 匹配的实例保留原有端口（零中断）
- 删除时 `freePort(tag)` 回收
- 范围 10000-10099（100 个 slot），耗尽返回错误

这样即使订阅刷新改变了 proxy 顺序，同一 proxy 的端口不变，
客户端不会断连。

### 6. HotSwap Diff 算法

冷热分离，只操作变化的部分：

```
oldByTag = {tag: instance}
for proxy in selected:
    if oldByTag[tag] exists:
        keep it (update latency)
        delete oldByTag[tag]
    else:
        assignPort + startInstance
for remaining in oldByTag:
    close (不影响当前活跃实例)
```

零 downtime——始终保持 `newInstances` 就绪后替换 `p.instances`。

### 7. L0-L3 锁层级

避免死锁，固定加锁顺序：

| 层级 | 锁 | 保护 |
|------|----|------|
| L0 | `atomic.Bool` | 排他 test+swap（只有一把） |
| L1 | `subMgr.RWMutex` | 订阅增删改 |
| L2 | `tester.RWMutex` | Test 缓存 |
| L3 | `pool.Mutex` | 端口 + 实例 |

**L0 排他门 + 30s 缓存：** 并发请求时仅第一个执行完整 test+swap，
后续走缓存。锁顺序 L0 → L1 → L2 → L3，禁止逆序。

### 8. 测试缓存 + 滑窗打乱

```
TestResultCache (TTL 2h)
    ↓ refreshCache(stale/missing only)
    ↓ concurrency = 3 (semaphore)
健康门: latency < 500ms && err == nil
    ↓ sort asc
滑窗: min(healthy, count*3)
    ↓ shuffle (rand.Shuffle)
取前 count 个
```

增量刷新 + 滑窗打乱确保多样性——不会被同一批最快节点垄断。

### 9. Clash 订阅解析坑

- `TLS.Reality` 可能为 nil，要加 nil check：`p.TLS != nil && p.TLS.Reality != nil && p.TLS.Reality.Enabled`
- `Flow` 是 string（非指针），空串留空即可
- Transport WS 的 `Headers` 是 `map[string]string`，sing-box 要求 `badoption.HTTPHeader`
- VLESS 的 `PacketEncoding` 常见 `"xudp"`
- Trojan 通常不含 transport/flow，只有 password + TLS

### 10. 测试延迟的方法

用配置里的 HTTP/HTTPS URL（默认 `https://www.gstatic.com/generate_204`）
解析出 host、port、path 后，通过 `out.DialContext()` 建立 TCP 连接；HTTPS
目标会先完成目标站 TLS 握手，然后发送两次 HTTP GET 请求。第一次成功后
复用同一条连接，间隔 100ms 发起第二次请求，取成功结果里的最小耗时，而
不是 ping（ICMP 可能被禁）：

```go
target := M.ParseSocksaddrHostPort("www.gstatic.com", 443)
request := "GET /generate_204 HTTP/1.1\r\nHost: www.gstatic.com\r\n\r\n"

conn, err := out.DialContext(ctx, "tcp", target)
// latency = time.Since(start)
```

零端口占用——不需要监听任何端口即可测试。

## 持久化

运行数据保存到 SQLite 数据库 `data/http-proxy.db`，包括订阅、解析后的代理、
应用配置和测速结果。测速结果按 2 小时 TTL 复用，应用重启后未过期结果仍会
显示并参与健康代理选择。

旧的 `subscriptions.json` 不会自动导入；首次使用 SQLite 时会从空库开始。
Docker Compose 默认挂载 `./data:/app/data`，保留该目录即可保留状态。

## 构建

项目的管理界面位于 `webui/`，使用 Vue 3 + Vite 构建，并通过 Go
`embed.FS` 打进最终二进制。执行构建脚本即可完成前端依赖安装、前端打包
和 Go 编译：

```bat
build.bat
```

产物 `overtls.exe`，运行于 `127.0.0.1:9090`。

修改前端后，完整应用验证必须先构建前端，再启动 Go 程序：

```bat
npm --prefix webui run build
go run .
```

Go 程序会同时提供后端 API 和嵌入式前端，访问
`http://127.0.0.1:9090`。不要用 Vite dev server 作为完整应用的启动或验收方式。

### CI Docker 构建

推送到 `main` 分支或 `v*` 标签时，GitHub Actions 自动构建 Docker 镜像并推送到
ghcr.io：

```bash
docker pull ghcr.io/Jakens0426/http-proxy:latest
```

使用 docker-compose 运行（需先创建 `data/` 目录）：

```yaml
services:
  http-proxy:
    image: ghcr.io/Jakens0426/http-proxy:latest
    container_name: http-proxy
    restart: unless-stopped
    ports:
      - "9090:9090"
    volumes:
      - ./data:/app/data
    environment:
      - TZ=Asia/Shanghai
```

## API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/subscriptions` | 订阅列表 |
| POST | `/api/subscriptions` | 添加订阅 `{"url":"..."}` |
| DELETE | `/api/subscriptions/{id}` | 删除 |
| POST | `/api/subscriptions/{id}/refresh` | 刷新 |
| GET | `/api/proxies` | 所有解析后的代理 |
| POST | `/api/proxies/{tag}/test` | 测试单个代理 |
| GET | `/api/proxies/available?count=N&token=TOKEN` | 获取 N 个健康 HTTP 代理，需要设置页中的 Available Token |
| GET | `/api/pool/status` | 池状态 |
| POST | `/api/pool/stop` | 停止所有实例 |
| GET | `/api/config` | 配置；设置了管理 Token 后需要 `X-Admin-Token` |
| PUT | `/api/config` | 更新 `{"upstream_proxy":"socks5://...","test_target":"https://www.gstatic.com/generate_204","test_timeout_seconds":3,"admin_token":"...","available_token":"...","pool_proxy_username":"...","pool_proxy_password":"..."}`
| POST | `/api/config/upstream/test` | 测试上游代理 `{"upstream_proxy":"socks5://...","test_target":"https://www.gstatic.com/generate_204"}` |
| GET | `/api/logs?limit=N` | 最近诊断日志 |
| POST | `/api/logs/clear` | 清空诊断日志 |
| GET | `/api/request-logs/dates` | 已保存的代理请求日志日期 |
| GET | `/api/request-logs?date=YYYY-MM-DD&limit=N` | 指定日期代理请求日志 |
| DELETE | `/api/request-logs?date=YYYY-MM-DD` | 删除指定日期代理请求日志；不传 `date` 删除全部 |

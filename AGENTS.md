# AGENTS.md

## 项目定位

这是一个 Go + Vue 的 HTTP 代理池管理器。Go 后端负责订阅解析、节点测速、代理池热切换、SQLite 持久化和 API；Vue 前端构建到 `webui/dist` 后通过 Go `embed.FS` 内嵌，由同一个 Go 进程提供前端页面和后端 API。

## 目录与职责

- `main.go`：程序入口，内嵌 `webui/dist`，固定监听 `127.0.0.1:9090`。
- `service.go`：应用服务层，串联订阅、测速器、代理池、配置、诊断日志和缓存。
- `server/server.go`：HTTP API、静态前端服务、管理 token 和 available token 鉴权。
- `core/`：核心逻辑。
  - `config.go`：默认值和 `AppConfig`。
  - `storage.go`：SQLite schema、自动迁移、订阅/配置/测速结果持久化。
  - `subscription.go`、`parser.go`、`share.go`：订阅拉取、节点解析、分享链接生成。
  - `tester.go`：基于 sing-box 的节点测速和健康结果选择。
  - `pool.go`：本地 HTTP mixed inbound 代理池，端口范围 `10000-10099`。
  - `diagnostics.go`：诊断日志环形缓冲。
- `webui/src/`：Vue 3 前端源码，`api.js` 统一封装 API 请求和 `X-Admin-Token`。
- `webui/dist/`：前端构建产物，供 Go embed 使用；不要手写编辑。
- `scripts/`、`build.bat`：构建脚本。
- `data/http-proxy.db`：默认运行数据库。
- `temp/`、`webui/node_modules/`：外部源码/依赖目录，除非明确要求，不要改。

## 启动与验证规约

- 不要把 Vite dev server 当作本项目的启动方式，不要用 `npm --prefix webui run dev` 来验证完整应用。
- 修改前端后，先执行：

```powershell
npm --prefix webui run build
```

- 前端 build 通过后，使用 Go 程序启动完整应用，由 Go 同时提供后端 API 和嵌入式前端：

```powershell
go run .
```

- 完整应用固定访问地址为 `http://127.0.0.1:9090`。
- `npm --prefix webui run dev` 仅允许用于前端局部开发调试；交付验证、功能验收和向用户提供访问地址时必须使用 Go 程序。
- 后端回归测试使用：

```powershell
go test ./...
```

- 打包构建可使用：

```powershell
.\build.bat
```

## 运行链路

- 启动时 `main.go` 打开 `data/http-proxy.db`，加载订阅和配置，创建 `SubscriptionManager` 与 `service`。
- `service` 初始化时会过滤 sing-box 不支持的节点，创建 `Tester`，创建 `ProxyPool`，并加载配置中的上游代理、测速目标、代理池认证信息。
- 添加/刷新订阅会拉取订阅内容，解析 VLESS / Trojan / Shadowsocks 节点，写入 SQLite，并同步测速器。
- 调用 `/api/proxies/available?count=N&token=...` 会触发健康节点选择和代理池热切换：
  - 先复用 `CacheTTL = 30s` 内的 available 结果。
  - 未命中缓存时用 L0 原子锁避免并发重复测速/换池。
  - 健康阈值由 `MaxLatencyMs = 500` 控制。
  - 选中的节点通过 `ProxyPool.HotSwap` 绑定到 `127.0.0.1:10000-10099`。

## 配置与鉴权规则

- 配置统一在 `core.AppConfig`、SQLite `app_config`、`/api/config` 和设置页维护。
- `storage.go` 会对旧数据库自动补齐新增配置列；新增配置字段时也要同步 schema 迁移、读写 SQL、服务层归一化和前端设置页。
- `admin_token`：
  - 为空时管理 API 暂不鉴权，便于首次配置。
  - 非空后，除 `/api/proxies/available` 外的 `/api/*` 管理接口都要求请求头 `X-Admin-Token`。
- `available_token`：
  - `/api/proxies/available` 独立使用 query token：`/api/proxies/available?count=N&token=TOKEN`。
  - 未配置或 token 错误时返回 `403`，不应调用服务层生成代理池。
- `pool_proxy_username` / `pool_proxy_password`：
  - 必须同时为空或同时填写。
  - 填写后 sing-box inbound 会强制认证，返回的代理地址会包含 `http://user:pass@127.0.0.1:port`。
- 不要在日志里输出 token 或代理池密码明文；现有配置保存日志只记录布尔状态。

## 前端规则

- 前端入口是 `webui/src/App.vue`，API 封装在 `webui/src/api.js`。
- 登录 token 存在 `sessionStorage`，通过 `X-Admin-Token` 发送。
- 代理池刷新调用必须传设置页中的 `available_token`，不要用管理 token 替代。
- 修改 `webui/src` 后必须重新执行 `npm --prefix webui run build`，否则 Go embed 仍会使用旧的 `webui/dist`。
- `webui/dist` 是构建产物，只能由 build 生成，不要手工改。

## 后端规则

- Go 文件修改后运行 `gofmt`。
- API handler 保持在 `server/server.go`，业务编排放在 `service.go`，核心能力放在 `core/`。
- 服务层错误如果需要指定 HTTP 状态，使用 `server.NewStatusError`，handler 通过 `statusFromError` 输出。
- 新增持久化字段时，必须同时补测试，覆盖旧库默认值、保存、重启读取。
- `ProxyPool` 配置变化会关闭旧实例并清空端口映射；不要保留旧认证状态的实例。

## 常用验证

- 后端和核心逻辑：

```powershell
go test ./...
```

- 前端构建：

```powershell
npm --prefix webui run build
```

- 完整本地运行：

```powershell
go run .
```

- Docker 运行路径通过 `Dockerfile` 多阶段构建前端和 Go 二进制，`docker-compose.yml` 暴露 `9090` 并挂载 `./data:/app/data`。

## 环境与命令习惯

- 当前项目主要在 Windows + PowerShell 下操作，优先使用 PowerShell 原生命令。
- 查找文本优先 `rg`，查找文件优先 `fd`，查看文件优先 `bat --style=plain --paging=never`。
- 不要为了探测环境反复执行等价命令；遇到明显沙箱/权限/网络限制时，说明原因并按需申请授权。

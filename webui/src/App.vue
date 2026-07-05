<template>
  <section v-if="!initialized" class="auth-screen">
    <div class="auth-card">
      <div class="brand auth-brand">
        <div class="brand-mark">转</div>
        <div>
          <h1>代理中转器</h1>
          <p>sing-box proxy pool</p>
        </div>
      </div>
      <div class="auth-loading">加载中</div>
    </div>
  </section>

  <section v-else-if="authRequired" class="auth-screen">
    <form class="auth-card" @submit.prevent="handleLogin">
      <div class="brand auth-brand">
        <div class="brand-mark">转</div>
        <div>
          <h1>代理中转器</h1>
          <p>sing-box proxy pool</p>
        </div>
      </div>
      <label>
        <span>访问 Token</span>
        <input v-model.trim="loginToken" type="password" autocomplete="current-password" autofocus />
      </label>
      <button class="button primary" type="submit" :disabled="loading.login">
        {{ loading.login ? '登录中' : '登录' }}
      </button>
    </form>
  </section>

  <div v-else class="app-shell" :class="{ 'sidebar-collapsed': sidebarCollapsed }">
    <aside class="sidebar">
      <div class="sidebar-header">
        <div class="brand">
          <div class="brand-mark">转</div>
          <div class="brand-text">
            <h1>代理中转器</h1>
            <p>sing-box proxy pool</p>
          </div>
        </div>
      </div>

      <nav class="nav-list" aria-label="主导航">
        <button
          v-for="tab in tabs"
          :key="tab.id"
          class="nav-item"
          :class="{ active: activeTab === tab.id }"
          type="button"
          :title="tab.label"
          :aria-label="tab.label"
          :aria-current="activeTab === tab.id ? 'page' : undefined"
          @click="navigateToTab(tab.id)"
        >
          <span class="nav-icon">{{ tab.icon }}</span>
          <span class="nav-label">{{ tab.label }}</span>
        </button>
      </nav>

      <button
        class="sidebar-toggle"
        type="button"
        :title="sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'"
        :aria-label="sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'"
        @click="toggleSidebar"
      >
        <span aria-hidden="true">{{ sidebarCollapsed ? '›' : '‹' }}</span>
      </button>
    </aside>

    <main class="workspace">
      <header class="topbar">
        <div>
          <p class="eyebrow">{{ activeTabLabel }}</p>
          <h2>{{ activeTabTitle }}</h2>
        </div>
        <button v-if="canRefreshActiveTab" class="button ghost" type="button" :disabled="loading.refreshPage" @click="refreshCurrentPage">
          <span aria-hidden="true">↻</span>
          <span>{{ loading.refreshPage ? '刷新中' : '刷新' }}</span>
        </button>
      </header>

      <section class="metrics-grid" aria-label="状态概览">
        <article class="metric">
          <span>订阅</span>
          <strong>{{ subscriptions.length }}</strong>
        </article>
        <article class="metric">
          <span>节点</span>
          <strong>{{ proxies.length }}</strong>
        </article>
        <article class="metric">
          <span>活跃端口</span>
          <strong>{{ poolStatus.count || 0 }}</strong>
        </article>
        <article class="metric">
          <span>健康节点</span>
          <strong>{{ healthyProxyCount }}</strong>
        </article>
      </section>

      <section v-if="activeTab === 'subscriptions'" class="panel">
        <div class="panel-header">
          <div>
            <h3>订阅管理</h3>
            <p>添加订阅后会自动解析 VLESS / Trojan / SS 节点。</p>
          </div>
        </div>

        <form class="inline-form" @submit.prevent="handleAddSubscription">
          <input v-model.trim="subscriptionURL" type="url" placeholder="https://example.com/subscription" />
          <button class="button primary" type="submit" :disabled="loading.addSubscription">
            {{ loading.addSubscription ? '添加中' : '添加订阅' }}
          </button>
        </form>

        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>名称</th>
                <th>URL</th>
                <th>代理数</th>
                <th>更新时间</th>
                <th class="actions">操作</th>
              </tr>
            </thead>
            <tbody v-if="subscriptions.length">
              <tr v-for="sub in subscriptions" :key="sub.id">
                <td>
                  <strong>{{ sub.name }}</strong>
                </td>
                <td class="muted truncate">{{ sub.url }}</td>
                <td>{{ sub.proxies?.length || 0 }}</td>
                <td class="muted">{{ timeAgo(sub.updated_at) }}</td>
                <td class="actions">
                  <button class="button small" type="button" :disabled="busySubscriptions[sub.id]" @click="handleRefreshSubscription(sub.id)">
                    {{ busySubscriptions[sub.id] === 'refresh' ? '刷新中' : '刷新' }}
                  </button>
                  <button class="button small danger" type="button" :disabled="busySubscriptions[sub.id]" @click="handleDeleteSubscription(sub.id)">
                    删除
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!subscriptions.length" class="empty-state">暂无订阅</div>
        </div>
      </section>

      <section v-if="activeTab === 'proxies'" class="panel">
        <div class="panel-header">
          <div>
            <h3>代理列表</h3>
            <p>延迟来自缓存或单节点测试结果。</p>
          </div>
        </div>

        <div class="table-wrap">
          <table class="proxies-table">
            <thead>
              <tr>
                <th>协议</th>
                <th>名称</th>
                <th>服务器</th>
                <th>端口</th>
                <th>TLS</th>
                <th>延迟</th>
                <th>测速有效期</th>
                <th class="actions">操作</th>
              </tr>
            </thead>
            <tbody v-if="proxies.length">
              <tr v-for="proxy in proxies" :key="proxy.tag">
                <td><span class="badge" :class="protocolClass(proxy.protocol)">{{ protocolLabel(proxy.protocol) }}</span></td>
                <td><strong>{{ proxy.name }}</strong></td>
                <td class="mono truncate">{{ proxy.server }}</td>
                <td>{{ proxy.port }}</td>
                <td><span class="badge subtle">{{ proxy.tls || 'none' }}</span></td>
                <td :class="latencyClass(proxy)">{{ latencyText(proxy) }}</td>
                <td :class="testTTLClass(proxy)">{{ testTTLText(proxy) }}</td>
                <td class="actions">
                  <button class="button small" type="button" :disabled="!proxy.share_url" @click="copyShareURL(proxy)">
                    复制链接
                  </button>
                  <button class="button small" type="button" :disabled="proxyTesting[proxy.tag]" @click="handleTestProxy(proxy)">
                    {{ proxyTesting[proxy.tag] ? '测试中' : '测试' }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!proxies.length" class="empty-state">暂无代理</div>
        </div>
      </section>

      <section v-if="activeTab === 'pool'" class="panel">
        <div class="panel-header split">
          <div>
            <h3>代理池</h3>
            <p>选择健康节点并映射为本地 HTTP 代理端口。</p>
          </div>
          <div class="toolbar">
            <input v-model.number="poolCount" class="count-input" type="number" min="1" max="20" />
            <button class="button primary" type="button" :disabled="loading.pool" @click="handleRefreshPool">
              {{ loading.pool ? '刷新中' : '启动/刷新' }}
            </button>
            <button class="button danger" type="button" :disabled="loading.stopPool" @click="handleStopPool">停止全部</button>
          </div>
        </div>

        <div class="pool-status-strip">
          <span><strong>阶段</strong>{{ availableStageText }}</span>
          <span><strong>候选池</strong>{{ availableStatus.candidate_count || 0 }}</span>
          <span><strong>待测</strong>{{ availableStatus.pending || 0 }}</span>
          <span><strong>已测</strong>{{ availableStatus.tested || 0 }}</span>
          <span><strong>健康</strong>{{ availableStatus.healthy || 0 }}</span>
          <span><strong>失败</strong>{{ availableStatus.failed || 0 }}</span>
          <span><strong>缓存 TTL</strong>{{ formatSeconds(availableStatus.available_cache_ttl_seconds) }}</span>
          <span><strong>测速 TTL</strong>{{ formatSeconds(availableStatus.test_result_ttl_seconds) }}</span>
          <span v-if="availableStatus.last_error" class="bad"><strong>错误</strong>{{ availableStatus.last_error }}</span>
        </div>

        <div class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>协议</th>
                <th>名称</th>
                <th>HTTP 地址</th>
                <th>延迟</th>
                <th>运行时长</th>
              </tr>
            </thead>
            <tbody v-if="poolRows.length">
              <tr v-for="item in poolRows" :key="item.tag || item.http">
                <td><span class="badge" :class="protocolClass(item.protocol)">{{ protocolLabel(item.protocol) }}</span></td>
                <td><strong>{{ item.name }}</strong></td>
                <td class="mono">{{ item.http }}</td>
                <td class="good">{{ item.latency }}ms</td>
                <td class="muted">{{ item.uptime || '活跃中' }}</td>
              </tr>
            </tbody>
          </table>
          <div v-if="!poolRows.length" class="empty-state">暂无活跃代理</div>
        </div>
      </section>

      <section v-if="activeTab === 'logs'" class="panel">
        <div class="panel-header split">
          <div>
            <h3>诊断日志</h3>
            <p>展示最近的订阅、测速、过滤和代理池事件。</p>
          </div>
          <div class="toolbar">
            <button class="button" type="button" :disabled="loading.logs" @click="handleRefreshLogs">
              {{ loading.logs ? '刷新中' : '刷新日志' }}
            </button>
            <button class="button danger" type="button" :disabled="loading.clearLogs" @click="handleClearLogs">清空日志</button>
          </div>
        </div>

        <div class="table-wrap">
          <table class="logs-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>级别</th>
                <th>范围</th>
                <th>节点</th>
                <th>阶段</th>
                <th>消息</th>
                <th>详情</th>
              </tr>
            </thead>
            <tbody v-if="diagnosticLogs.length">
              <tr v-for="entry in diagnosticLogs" :key="entry.id" :class="`log-row-${entry.level || 'info'}`">
                <td class="mono">{{ absoluteTime(entry.time) }}</td>
                <td><span class="badge" :class="logLevelClass(entry.level)">{{ logLevelLabel(entry.level) }}</span></td>
                <td>{{ entry.scope || '-' }}</td>
                <td class="log-node">
                  <strong>{{ entry.name || entry.tag || '-' }}</strong>
                  <span v-if="entry.tag && entry.name" class="mono muted">{{ entry.tag }}</span>
                </td>
                <td><span class="badge subtle">{{ entry.stage || '-' }}</span></td>
                <td class="log-message">{{ entry.message || '-' }}</td>
                <td class="mono log-detail" :title="entry.detail || ''">{{ entry.detail || '-' }}</td>
              </tr>
            </tbody>
          </table>
          <div v-if="!diagnosticLogs.length" class="empty-state">暂无日志</div>
        </div>
      </section>

      <section v-if="activeTab === 'requestLogs'" class="panel">
        <div class="panel-header split">
          <div>
            <h3>代理请求日志</h3>
            <p>按天保存通过代理池转发的请求目标。</p>
          </div>
          <div class="toolbar">
            <input v-model="requestLogDate" class="date-input" type="date" list="request-log-dates" @change="handleRefreshRequestLogs" />
            <datalist id="request-log-dates">
              <option v-for="date in requestLogDates" :key="date" :value="date" />
            </datalist>
            <button class="button" type="button" :disabled="loading.requestLogs" @click="handleRefreshRequestLogs">
              {{ loading.requestLogs ? '刷新中' : '刷新日志' }}
            </button>
            <button class="button danger" type="button" :disabled="loading.clearRequestLogs || !requestLogDate" @click="handleClearRequestLogs">删除当天</button>
            <button class="button danger" type="button" :disabled="loading.clearAllRequestLogs" @click="handleClearAllRequestLogs">删除全部</button>
          </div>
        </div>

        <div class="table-wrap">
          <table class="request-logs-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>节点</th>
                <th>端口</th>
                <th>协议</th>
                <th>网络</th>
                <th>目标</th>
                <th>原始消息</th>
              </tr>
            </thead>
            <tbody v-if="requestLogs.length">
              <tr v-for="entry in requestLogs" :key="entry.id">
                <td class="mono">{{ absoluteTime(entry.time) }}</td>
                <td class="log-node">
                  <strong>{{ entry.proxy_name || entry.proxy_tag || '-' }}</strong>
                  <span v-if="entry.proxy_tag && entry.proxy_name" class="mono muted">{{ entry.proxy_tag }}</span>
                </td>
                <td class="mono">{{ entry.port || '-' }}</td>
                <td><span class="badge" :class="protocolClass(entry.protocol)">{{ protocolLabel(entry.protocol) }}</span></td>
                <td><span class="badge subtle">{{ (entry.network || '-').toUpperCase() }}</span></td>
                <td class="mono request-destination" :title="entry.destination || ''">{{ entry.destination || '-' }}</td>
                <td class="request-message-cell">
                  <button
                    class="mono request-message-toggle"
                    :class="{ expanded: isRequestLogExpanded(entry) }"
                    type="button"
                    :title="entry.message || ''"
                    :disabled="!entry.message"
                    @click="toggleRequestLogMessage(entry)"
                  >
                    {{ entry.message || '-' }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-if="!requestLogs.length" class="empty-state">暂无代理请求日志</div>
        </div>
      </section>

      <section v-if="activeTab === 'help'" class="panel">
        <div class="panel-header">
          <div>
            <h3>API 参考</h3>
            <p>当前后端支持的 HTTP API 端点。</p>
          </div>
        </div>

        <div class="table-wrap">
          <table class="api-table">
            <colgroup>
              <col class="api-col-method" />
              <col class="api-col-endpoint" />
              <col class="api-col-purpose" />
              <col class="api-col-params" />
              <col class="api-col-response" />
            </colgroup>
            <thead>
              <tr>
                <th>方法</th>
                <th>端点</th>
                <th>用途</th>
                <th>请求体/参数</th>
                <th>响应说明</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="endpoint in apiEndpoints" :key="`${endpoint.method}-${endpoint.path}`">
                <td>
                  <span class="badge api-method-badge" :class="methodClass(endpoint.method)">{{ endpoint.method }}</span>
                </td>
                <td><code class="mono api-code endpoint-code">{{ endpoint.path }}</code></td>
                <td>{{ endpoint.purpose }}</td>
                <td><code class="mono api-code">{{ endpoint.params }}</code></td>
                <td><code class="mono api-code">{{ endpoint.response }}</code></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="activeTab === 'settings'" class="panel settings-panel">
        <div class="panel-header">
          <div>
            <h3>设置</h3>
            <p>配置鉴权、代理池认证、测速目标、超时和上游代理。</p>
          </div>
        </div>

        <div class="settings-sections">
          <section class="settings-section">
            <h4>鉴权</h4>
            <div class="settings-grid">
              <label>
                <span>管理 Token</span>
                <input v-model.trim="config.admin_token" type="password" autocomplete="new-password" />
              </label>
              <label>
                <span>Available Token</span>
                <input v-model.trim="config.available_token" type="password" autocomplete="new-password" />
              </label>
            </div>
          </section>

          <section class="settings-section">
            <h4>代理池认证</h4>
            <div class="settings-grid">
              <label>
                <span>代理池用户名</span>
                <input v-model.trim="config.pool_proxy_username" type="text" autocomplete="username" />
              </label>
              <label>
                <span>代理池密码</span>
                <input v-model.trim="config.pool_proxy_password" type="password" autocomplete="new-password" />
              </label>
            </div>
          </section>

          <section class="settings-section">
            <h4>测速</h4>
            <div class="settings-grid">
              <label>
                <span>上游代理</span>
                <input v-model.trim="config.upstream_proxy" type="text" placeholder="http://... / socks5://... / ss://... / vless://... / trojan://..." />
              </label>
              <label>
                <span>测试目标</span>
                <input v-model.trim="config.test_target" type="url" placeholder="https://www.gstatic.com/generate_204" />
              </label>
              <label>
                <span>测试超时（秒）</span>
                <input v-model.number="config.test_timeout_seconds" type="number" min="1" max="60" />
              </label>
              <label>
                <span>测速结果 TTL（分钟）</span>
                <input v-model.number="config.test_result_ttl_minutes" type="number" min="5" max="1440" />
              </label>
            </div>
          </section>

          <section class="settings-section">
            <h4>Available 刷新策略</h4>
            <div class="settings-grid">
              <label>
                <span>缓存 TTL（秒）</span>
                <input v-model.number="config.available_cache_ttl_seconds" type="number" min="10" max="3600" />
              </label>
              <label>
                <span>快速探测预算（秒）</span>
                <input v-model.number="config.available_quick_probe_seconds" type="number" min="1" max="10" />
              </label>
              <label>
                <span>快速探测并发</span>
                <input v-model.number="config.available_quick_concurrency" type="number" min="1" max="50" />
              </label>
              <label>
                <span>后台刷新并发</span>
                <input v-model.number="config.available_background_concurrency" type="number" min="1" max="20" />
              </label>
              <label>
                <span>最小暖池数量</span>
                <input v-model.number="config.available_min_warm_pool_size" type="number" min="1" max="100" />
              </label>
            </div>
          </section>
        </div>

        <div class="form-actions">
          <button class="button" type="button" :disabled="loading.upstreamTest" @click="handleTestUpstream">
            {{ loading.upstreamTest ? '测试中' : '测试上游' }}
          </button>
          <button class="button primary" type="button" :disabled="loading.saveConfig" @click="handleSaveConfig">
            {{ loading.saveConfig ? '保存中' : '保存设置' }}
          </button>
        </div>
      </section>
    </main>

    <div class="toast-stack" aria-live="polite">
      <div v-for="toast in toasts" :key="toast.id" class="toast" :class="toast.type">{{ toast.message }}</div>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import {
  addSubscription,
  clearRequestLogs,
  clearLogs,
  deleteSubscription,
  getAvailableProxies,
  getAvailableStatus,
  getAdminToken,
  getConfig,
  getPoolStatus,
  listRequestLogDates,
  listRequestLogs,
  listLogs,
  listProxies,
  listSubscriptions,
  refreshSubscription,
  saveConfig,
  setAdminToken,
  stopPool,
  testProxy,
  testUpstream
} from './api'

const tabs = [
  { id: 'subscriptions', path: '/subscriptions', label: '订阅', title: '订阅管理', icon: '＋' },
  { id: 'proxies', path: '/proxies', label: '节点', title: '代理列表', icon: '◎' },
  { id: 'pool', path: '/pool', label: '代理池', title: '代理池', icon: '▦' },
  { id: 'logs', path: '/logs', label: '日志', title: '诊断日志', icon: '≡' },
  { id: 'requestLogs', path: '/request-logs', label: '请求', title: '代理请求日志', icon: '↗' },
  { id: 'help', path: '/help', label: '帮助', title: 'API 参考', icon: '?' },
  { id: 'settings', path: '/settings', label: '设置', title: '运行设置', icon: '⚙' }
]

const DEFAULT_TAB_ID = 'subscriptions'
const SIDEBAR_COLLAPSED_KEY = 'http-proxy-sidebar-collapsed'
const tabsById = Object.fromEntries(tabs.map((tab) => [tab.id, tab]))
const tabsByPath = Object.fromEntries(tabs.map((tab) => [tab.path, tab]))

const apiEndpoints = [
  {
    method: 'GET',
    path: '/api/subscriptions',
    purpose: '获取全部订阅及其解析出的节点。',
    params: '无',
    response: '200 Subscription[]；无订阅时返回 []。'
  },
  {
    method: 'POST',
    path: '/api/subscriptions',
    purpose: '添加一个订阅并解析代理节点。',
    params: '{"url":"https://example.com/subscription"}',
    response: '201 Subscription；400 invalid JSON / url is required；500 error。'
  },
  {
    method: 'DELETE',
    path: '/api/subscriptions/{id}',
    purpose: '删除指定订阅。',
    params: '路径参数：id 订阅 ID。',
    response: '200 {"message":"deleted"}；500 error。'
  },
  {
    method: 'POST',
    path: '/api/subscriptions/{id}/refresh',
    purpose: '重新拉取并解析指定订阅。',
    params: '路径参数：id 订阅 ID。',
    response: '200 Subscription；500 error。'
  },
  {
    method: 'GET',
    path: '/api/proxies',
    purpose: '获取全部已解析代理及缓存测速结果。',
    params: '无',
    response: '200 ProxyView[]，字段含 name、server、port、latency、err、test_timestamp、test_ttl_remaining_seconds、test_expired。'
  },
  {
    method: 'POST',
    path: '/api/proxies/{tag}/test',
    purpose: '对单个代理节点执行测速。',
    params: '路径参数：tag 代理标签，需 URL 编码；无请求体。',
    response: '200 TestResult，字段含 tag、latency、err、timestamp；404/500 返回 {"error":"..."}。'
  },
  {
    method: 'GET',
    path: '/api/proxies/available?count=N&token=TOKEN',
    purpose: '筛选健康节点并启动或刷新本地 HTTP 代理池。',
    params: '查询参数：count=N，正整数；token 使用设置中的 Available Token。',
    response: '200 {"proxies":[AvailableProxy],"count":N}；403 forbidden；503 返回 {"error":"..."}。'
  },
  {
    method: 'GET',
    path: '/api/proxies/available.txt?count=N&token=TOKEN',
    purpose: '筛选健康节点并以纯文本返回代理池 HTTP 地址。',
    params: '查询参数：count=N，正整数；token 使用设置中的 Available Token。',
    response: '200 text/plain，一行一个 HOST:PORT 代理地址；403 forbidden；503 返回 {"error":"..."}。'
  },
  {
    method: 'GET',
    path: '/api/pool/status',
    purpose: '获取当前代理池实例状态。',
    params: '无',
    response: '200 {"instances":[PoolInstanceStatus],"count":N}。'
  },
  {
    method: 'GET',
    path: '/api/pool/available/status',
    purpose: '获取 available 候选池和刷新进度。',
    params: '无',
    response: '200 AvailableStatus，字段含 stage、candidate_count、pending、tested、healthy、failed、last_error。'
  },
  {
    method: 'POST',
    path: '/api/pool/stop',
    purpose: '停止所有代理池实例。',
    params: '无',
    response: '200 {"message":"pool stopped"}。'
  },
  {
    method: 'GET',
    path: '/api/config',
    purpose: '读取运行配置。',
    params: '无',
    response: '200 AppConfig；配置了管理 Token 后需要 X-Admin-Token。'
  },
  {
    method: 'PUT',
    path: '/api/config',
    purpose: '更新运行配置。',
    params: '{"admin_token":"...","available_token":"...","available_cache_ttl_seconds":30,"test_result_ttl_minutes":120}',
    response: '200 {"message":"config updated"}；400 invalid JSON / 配置错误；403 forbidden；500 error。'
  },
  {
    method: 'POST',
    path: '/api/config/upstream/test',
    purpose: '测试上游代理到目标地址的连通性。',
    params: '{"upstream_proxy":"socks5://127.0.0.1:1080","test_target":"https://www.gstatic.com/generate_204"}',
    response: '200 TestResult，字段含 tag、latency、err、timestamp；400/500 返回 {"error":"..."}。'
  },
  {
    method: 'GET',
    path: '/api/logs?limit=N',
    purpose: '获取最近诊断日志。',
    params: '查询参数：limit=N，正整数；为空或无效时默认 200。',
    response: '200 DiagnosticEvent[]，字段含 id、time、level、scope、tag、name、stage、message、detail。'
  },
  {
    method: 'POST',
    path: '/api/logs/clear',
    purpose: '清空诊断日志。',
    params: '无',
    response: '200 {"message":"logs cleared"}。'
  },
  {
    method: 'GET',
    path: '/api/request-logs/dates',
    purpose: '获取已有代理请求日志日期。',
    params: '无',
    response: '200 string[]，日期格式 YYYY-MM-DD，按新到旧排序。'
  },
  {
    method: 'GET',
    path: '/api/request-logs?date=YYYY-MM-DD&limit=N',
    purpose: '获取指定日期的代理请求日志。',
    params: '查询参数：date 可选，默认当天；limit=N，正整数，默认 200。',
    response: '200 RequestLogEntry[]，字段含 id、time、proxy_tag、proxy_name、port、protocol、network、destination、message。'
  },
  {
    method: 'DELETE',
    path: '/api/request-logs?date=YYYY-MM-DD',
    purpose: '删除指定日期代理请求日志；不传 date 时删除全部。',
    params: '查询参数：date 可选。',
    response: '200 {"message":"request logs cleared"}。'
  }
]

const activeTab = ref(tabIdFromCurrentPath())
const sidebarCollapsed = ref(readSidebarCollapsed())
const subscriptionURL = ref('')
const subscriptions = ref([])
const proxies = ref([])
const poolRows = ref([])
const poolStatus = ref({ count: 0, instances: [] })
const availableStatus = ref({
  stage: 'idle',
  candidate_count: 0,
  quick_refreshing: false,
  background_refreshing: false,
  total: 0,
  pending: 0,
  tested: 0,
  healthy: 0,
  failed: 0,
  available_cache_ttl_seconds: 30,
  test_result_ttl_seconds: 7200,
  last_error: ''
})
const diagnosticLogs = ref([])
const requestLogs = ref([])
const requestLogDates = ref([])
const requestLogDate = ref(todayDate())
const expandedRequestLogIds = ref(new Set())
const poolCount = ref(5)
const toasts = ref([])
const initialized = ref(false)
const authRequired = ref(false)
const loginToken = ref(getAdminToken())

const config = reactive({
  upstream_proxy: '',
  test_target: 'https://www.gstatic.com/generate_204',
  test_timeout_seconds: 3,
  admin_token: '',
  available_token: '',
  pool_proxy_username: '',
  pool_proxy_password: '',
  available_cache_ttl_seconds: 30,
  test_result_ttl_minutes: 120,
  available_quick_probe_seconds: 1,
  available_quick_concurrency: 10,
  available_background_concurrency: 3,
  available_min_warm_pool_size: 20
})

const loading = reactive({
  login: false,
  refreshPage: false,
  addSubscription: false,
  pool: false,
  stopPool: false,
  saveConfig: false,
  upstreamTest: false,
  logs: false,
  clearLogs: false,
  requestLogs: false,
  clearRequestLogs: false,
  clearAllRequestLogs: false
})
const busySubscriptions = reactive({})
const proxyTesting = reactive({})

const activeTabTitle = computed(() => tabs.find((tab) => tab.id === activeTab.value)?.title || '')
const activeTabLabel = computed(() => tabs.find((tab) => tab.id === activeTab.value)?.label || '')
const canRefreshActiveTab = computed(() => activeTab.value !== 'help')
const healthyProxyCount = computed(() => proxies.value.filter((proxy) => !proxy.err && proxy.latency >= 0 && proxy.latency < 500).length)
const availableStageText = computed(() => availableStageLabel(availableStatus.value.stage))

onMounted(() => {
  window.addEventListener('popstate', handlePopState)
  initializeApp()
})

onBeforeUnmount(() => {
  window.removeEventListener('popstate', handlePopState)
})

async function initializeApp() {
  activeTab.value = tabIdFromCurrentPath()
  loading.refreshPage = canRefreshActiveTab.value
  try {
    await loadConfig()
    authRequired.value = false
    initialized.value = true
    await loadCurrentPageData(activeTab.value)
  } catch (error) {
    if (isForbidden(error)) {
      requireLogin(false)
      return
    }
    initialized.value = true
    notify(error.message, 'error')
  } finally {
    loading.refreshPage = false
  }
}

async function handleLogin() {
  if (!loginToken.value) {
    notify('请输入访问 Token', 'error')
    return
  }
  loading.login = true
  setAdminToken(loginToken.value)
  try {
    await loadConfig()
    authRequired.value = false
    await loadCurrentPageData(activeTab.value)
    notify('已登录', 'success')
  } catch (error) {
    if (isForbidden(error)) {
      setAdminToken('')
      loginToken.value = ''
      notify('Token 无效', 'error')
      return
    }
    notify(`登录失败：${error.message}`, 'error')
  } finally {
    loading.login = false
  }
}

async function refreshCurrentPage() {
  if (!canRefreshActiveTab.value) return
  loading.refreshPage = true
  try {
    await loadCurrentPageData(activeTab.value)
  } catch (error) {
    notifyRequestError(error, '刷新失败：')
  } finally {
    loading.refreshPage = false
  }
}

async function loadSubscriptions() {
  const data = await listSubscriptions()
  subscriptions.value = Array.isArray(data) ? data : []
}

async function loadProxies() {
  const data = await listProxies()
  proxies.value = Array.isArray(data) ? data : []
}

async function loadPoolStatus() {
  const status = await getPoolStatus()
  poolStatus.value = status || { count: 0, instances: [] }
  poolRows.value = Array.isArray(poolStatus.value.instances) ? poolStatus.value.instances : []
}

async function loadAvailableStatus() {
  const status = await getAvailableStatus()
  availableStatus.value = status || availableStatus.value
}

async function loadConfig() {
  const data = await getConfig()
  config.upstream_proxy = data.upstream_proxy || ''
  config.test_target = data.test_target || 'https://www.gstatic.com/generate_204'
  config.test_timeout_seconds = data.test_timeout_seconds || 3
  config.admin_token = data.admin_token || ''
  config.available_token = data.available_token || ''
  config.pool_proxy_username = data.pool_proxy_username || ''
  config.pool_proxy_password = data.pool_proxy_password || ''
  config.available_cache_ttl_seconds = data.available_cache_ttl_seconds || 30
  config.test_result_ttl_minutes = data.test_result_ttl_minutes || 120
  config.available_quick_probe_seconds = data.available_quick_probe_seconds || 1
  config.available_quick_concurrency = data.available_quick_concurrency || 10
  config.available_background_concurrency = data.available_background_concurrency || 3
  config.available_min_warm_pool_size = data.available_min_warm_pool_size || 20
}

async function loadLogs() {
  const data = await listLogs(200)
  diagnosticLogs.value = Array.isArray(data) ? data : []
}

async function loadRequestLogDates() {
  const data = await listRequestLogDates()
  requestLogDates.value = Array.isArray(data) ? data : []
  if (!requestLogDate.value) {
    requestLogDate.value = requestLogDates.value[0] || todayDate()
  }
}

async function loadRequestLogs() {
  if (!requestLogDate.value) {
    requestLogDate.value = todayDate()
  }
  const data = await listRequestLogs(requestLogDate.value, 200)
  requestLogs.value = Array.isArray(data) ? data : []
  clearExpandedRequestLogs()
}

async function loadRequestLogPageData() {
  await loadRequestLogDates()
  await loadRequestLogs()
}

async function loadCurrentPageData(tabId = activeTab.value) {
  switch (tabId) {
    case 'subscriptions':
      await Promise.all([loadSubscriptions(), loadProxies()])
      return
    case 'proxies':
      await loadProxies()
      return
    case 'pool':
      await Promise.all([loadPoolStatus(), loadAvailableStatus(), loadConfig()])
      return
    case 'logs':
      await loadLogs()
      return
    case 'requestLogs':
      await loadRequestLogPageData()
      return
    case 'settings':
      await loadConfig()
      return
    case 'help':
      return
    default:
      await Promise.all([loadSubscriptions(), loadProxies()])
  }
}

async function loadPageAfterNavigation(tabId) {
  if (!initialized.value || authRequired.value) return
  loading.refreshPage = tabId !== 'help'
  try {
    await loadCurrentPageData(tabId)
  } catch (error) {
    notifyRequestError(error, '加载失败：')
  } finally {
    loading.refreshPage = false
  }
}

function refreshLogsQuietly() {
  loadLogs().catch(() => {})
}

function refreshRequestLogsQuietly() {
  loadRequestLogPageData().catch(() => {})
}

function navigateToTab(id) {
  const tab = tabsById[id] || tabsById[DEFAULT_TAB_ID]
  if (currentPath() !== tab.path) {
    window.history.pushState({ tab: tab.id }, '', tab.path)
  }
  activeTab.value = tab.id
  loadPageAfterNavigation(tab.id)
}

function handlePopState() {
  const tabId = tabIdFromCurrentPath()
  activeTab.value = tabId
  loadPageAfterNavigation(tabId)
}

function tabIdFromCurrentPath() {
  const path = currentPath()
  if (path === '/') {
    replaceCurrentPath(tabsById[DEFAULT_TAB_ID].path)
    return DEFAULT_TAB_ID
  }
  const tab = tabsByPath[path]
  if (tab) {
    return tab.id
  }
  replaceCurrentPath(tabsById[DEFAULT_TAB_ID].path)
  return DEFAULT_TAB_ID
}

function currentPath() {
  const pathname = window.location.pathname || '/'
  if (pathname.length > 1) {
    return pathname.replace(/\/+$/, '')
  }
  return pathname
}

function replaceCurrentPath(path) {
  const suffix = `${window.location.search || ''}${window.location.hash || ''}`
  window.history.replaceState({ tab: tabsByPath[path]?.id || DEFAULT_TAB_ID }, '', `${path}${suffix}`)
}

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
  try {
    localStorage.setItem(SIDEBAR_COLLAPSED_KEY, sidebarCollapsed.value ? '1' : '0')
  } catch {
    // Ignore unavailable local storage.
  }
}

function readSidebarCollapsed() {
  try {
    return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === '1'
  } catch {
    return false
  }
}

async function handleAddSubscription() {
  if (!subscriptionURL.value) {
    notify('请输入订阅 URL', 'error')
    return
  }
  loading.addSubscription = true
  try {
    await addSubscription(subscriptionURL.value)
    subscriptionURL.value = ''
    notify('订阅已添加', 'success')
    await Promise.all([loadSubscriptions(), loadProxies()])
  } catch (error) {
    notifyRequestError(error, '添加失败：')
  } finally {
    refreshLogsQuietly()
    loading.addSubscription = false
  }
}

async function handleRefreshSubscription(id) {
  busySubscriptions[id] = 'refresh'
  try {
    await refreshSubscription(id)
    notify('订阅已刷新', 'success')
    await Promise.all([loadSubscriptions(), loadProxies()])
  } catch (error) {
    notifyRequestError(error, '刷新失败：')
  } finally {
    refreshLogsQuietly()
    delete busySubscriptions[id]
  }
}

async function handleDeleteSubscription(id) {
  if (!confirm('确认删除此订阅？')) return
  busySubscriptions[id] = 'delete'
  try {
    await deleteSubscription(id)
    notify('订阅已删除', 'success')
    await Promise.all([loadSubscriptions(), loadProxies(), loadPoolStatus()])
  } catch (error) {
    notifyRequestError(error, '删除失败：')
  } finally {
    refreshLogsQuietly()
    delete busySubscriptions[id]
  }
}

async function handleTestProxy(proxy) {
  proxyTesting[proxy.tag] = true
  try {
    const result = await testProxy(proxy.tag)
    proxy.latency = result.latency
    proxy.err = result.err || ''
    proxy.test_timestamp = result.timestamp || new Date().toISOString()
    proxy.test_age_seconds = 0
    proxy.test_ttl_seconds = proxy.test_ttl_seconds || Number(config.test_result_ttl_minutes || 120) * 60
    proxy.test_ttl_remaining_seconds = proxy.test_ttl_seconds
    proxy.test_expired = false
    notify(proxy.err ? `测试失败：${proxy.err}` : `测试完成：${proxy.latency}ms`, proxy.err ? 'error' : 'success')
  } catch (error) {
    notifyRequestError(error, '测试失败：')
  } finally {
    refreshLogsQuietly()
    delete proxyTesting[proxy.tag]
  }
}

async function copyShareURL(proxy) {
  if (!proxy.share_url) {
    notify('该代理没有可用分享链接', 'error')
    return
  }
  try {
    await copyText(proxy.share_url)
    notify('分享链接已复制', 'success')
  } catch (error) {
    notify(`复制失败：${error.message}`, 'error')
  }
}

async function copyText(text) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text)
    return
  }

  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.left = '-9999px'
  document.body.appendChild(textarea)
  textarea.select()
  const ok = document.execCommand('copy')
  textarea.remove()
  if (!ok) {
    throw new Error('浏览器拒绝复制')
  }
}

async function handleRefreshPool() {
  loading.pool = true
  try {
    const data = await getAvailableProxies(poolCount.value || 5, config.available_token.trim())
    poolRows.value = Array.isArray(data.proxies) ? data.proxies : []
    await Promise.all([loadPoolStatus(), loadAvailableStatus()])
    notify(`已更新 ${data.count || poolRows.value.length} 个代理`, 'success')
  } catch (error) {
    notify(`刷新失败：${error.message}`, 'error')
  } finally {
    refreshLogsQuietly()
    loading.pool = false
  }
}

async function handleStopPool() {
  if (!confirm('确认停止所有代理实例？')) return
  loading.stopPool = true
  try {
    await stopPool()
    poolRows.value = []
    poolStatus.value = { count: 0, instances: [] }
    await loadAvailableStatus()
    notify('代理池已停止', 'success')
  } catch (error) {
    notifyRequestError(error, '停止失败：')
  } finally {
    refreshLogsQuietly()
    loading.stopPool = false
  }
}

async function handleRefreshLogs() {
  loading.logs = true
  try {
    await loadLogs()
  } catch (error) {
    notifyRequestError(error, '刷新日志失败：')
  } finally {
    loading.logs = false
  }
}

async function handleClearLogs() {
  if (!confirm('确认清空诊断日志？')) return
  loading.clearLogs = true
  try {
    await clearLogs()
    diagnosticLogs.value = []
    notify('诊断日志已清空', 'success')
  } catch (error) {
    notifyRequestError(error, '清空日志失败：')
  } finally {
    loading.clearLogs = false
  }
}

async function handleRefreshRequestLogs() {
  loading.requestLogs = true
  try {
    await loadRequestLogPageData()
  } catch (error) {
    notifyRequestError(error, '刷新代理请求日志失败：')
  } finally {
    loading.requestLogs = false
  }
}

async function handleClearRequestLogs() {
  if (!requestLogDate.value) return
  if (!confirm(`确认删除 ${requestLogDate.value} 的代理请求日志？`)) return
  loading.clearRequestLogs = true
  try {
    await clearRequestLogs(requestLogDate.value)
    requestLogs.value = []
    clearExpandedRequestLogs()
    await loadRequestLogDates()
    notify('代理请求日志已删除', 'success')
  } catch (error) {
    notifyRequestError(error, '删除代理请求日志失败：')
  } finally {
    loading.clearRequestLogs = false
  }
}

async function handleClearAllRequestLogs() {
  if (!confirm('确认删除全部代理请求日志？')) return
  loading.clearAllRequestLogs = true
  try {
    await clearRequestLogs()
    requestLogs.value = []
    requestLogDates.value = []
    requestLogDate.value = todayDate()
    clearExpandedRequestLogs()
    notify('全部代理请求日志已删除', 'success')
  } catch (error) {
    notifyRequestError(error, '删除全部代理请求日志失败：')
  } finally {
    loading.clearAllRequestLogs = false
  }
}

async function handleSaveConfig() {
  loading.saveConfig = true
  try {
    await saveConfig({
      upstream_proxy: config.upstream_proxy.trim(),
      test_target: config.test_target.trim(),
      test_timeout_seconds: Number(config.test_timeout_seconds) || 3,
      admin_token: config.admin_token.trim(),
      available_token: config.available_token.trim(),
      pool_proxy_username: config.pool_proxy_username.trim(),
      pool_proxy_password: config.pool_proxy_password.trim(),
      available_cache_ttl_seconds: Number(config.available_cache_ttl_seconds) || 30,
      test_result_ttl_minutes: Number(config.test_result_ttl_minutes) || 120,
      available_quick_probe_seconds: Number(config.available_quick_probe_seconds) || 1,
      available_quick_concurrency: Number(config.available_quick_concurrency) || 10,
      available_background_concurrency: Number(config.available_background_concurrency) || 3,
      available_min_warm_pool_size: Number(config.available_min_warm_pool_size) || 20
    })
    setAdminToken(config.admin_token.trim())
    loginToken.value = getAdminToken()
    await loadConfig()
    if (activeTab.value === 'pool') {
      await loadAvailableStatus()
    }
    notify('设置已保存', 'success')
  } catch (error) {
    notifyRequestError(error, '保存失败：')
  } finally {
    refreshLogsQuietly()
    loading.saveConfig = false
  }
}

async function handleTestUpstream() {
  loading.upstreamTest = true
  try {
    const result = await testUpstream(config.upstream_proxy.trim(), config.test_target.trim())
    notify(result.err ? `上游测试失败：${result.err}` : `上游测试成功：${result.latency}ms`, result.err ? 'error' : 'success')
  } catch (error) {
    notifyRequestError(error, '上游测试失败：')
  } finally {
    refreshLogsQuietly()
    loading.upstreamTest = false
  }
}

function protocolLabel(protocol = '') {
  const value = protocol.toLowerCase()
  if (value === 'shadowsocks') return 'SS'
  return value ? value.toUpperCase() : 'UNKNOWN'
}

function protocolClass(protocol = '') {
  return `protocol-${protocol.toLowerCase().replace(/[^a-z0-9]/g, '') || 'unknown'}`
}

function latencyText(proxy) {
  if (proxy.err) return '失败'
  if (proxy.latency === -1 || proxy.latency === undefined || proxy.latency === null) return '未测试'
  return `${proxy.latency}ms`
}

function latencyClass(proxy) {
  if (proxy.err) return 'bad'
  if (proxy.latency === -1 || proxy.latency === undefined || proxy.latency === null) return 'muted'
  if (proxy.latency < 150) return 'good'
  if (proxy.latency < 300) return 'warn'
  return 'bad'
}

function testTTLText(proxy) {
  if (!proxy.test_timestamp) return '未测试'
  if (proxy.test_expired || Number(proxy.test_ttl_remaining_seconds) <= 0) return '已过期'
  return `剩余 ${formatDurationShort(proxy.test_ttl_remaining_seconds)}`
}

function testTTLClass(proxy) {
  if (!proxy.test_timestamp) return 'muted'
  if (proxy.test_expired || Number(proxy.test_ttl_remaining_seconds) <= 0) return 'bad'
  if (Number(proxy.test_ttl_remaining_seconds) < 600) return 'warn'
  return 'good'
}

function formatDurationShort(seconds) {
  const total = Math.max(0, Math.floor(Number(seconds) || 0))
  if (total < 60) return '<1m'
  if (total < 3600) return `${Math.floor(total / 60)}m`
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  return minutes ? `${hours}h ${minutes}m` : `${hours}h`
}

function formatSeconds(seconds) {
  const total = Math.max(0, Math.floor(Number(seconds) || 0))
  if (total < 60) return `${total}s`
  return formatDurationShort(total)
}

function availableStageLabel(stage = '') {
  if (stage === 'quick') return '快速探测'
  if (stage === 'background') return '后台刷新'
  return '空闲'
}

function logLevelLabel(level = '') {
  const value = level.toLowerCase()
  if (value === 'error') return '错误'
  if (value === 'warn') return '警告'
  if (value === 'success') return '成功'
  return '信息'
}

function logLevelClass(level = '') {
  return `level-${level.toLowerCase() || 'info'}`
}

function methodClass(method = '') {
  return `method-${method.toLowerCase() || 'unknown'}`
}

function timeAgo(value) {
  if (!value) return '-'
  const timestamp = new Date(value).getTime()
  if (Number.isNaN(timestamp)) return '-'
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000))
  if (seconds < 60) return '刚刚'
  if (seconds < 3600) return `${Math.floor(seconds / 60)}分钟前`
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}小时前`
  return `${Math.floor(seconds / 86400)}天前`
}

function todayDate() {
  const date = new Date()
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function absoluteTime(value) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '-'
  return date.toLocaleString()
}

function requestLogKey(entry) {
  if (entry?.id !== undefined && entry?.id !== null) {
    return String(entry.id)
  }
  return `${entry?.time || ''}|${entry?.proxy_tag || ''}|${entry?.destination || ''}|${entry?.message || ''}`
}

function isRequestLogExpanded(entry) {
  return expandedRequestLogIds.value.has(requestLogKey(entry))
}

function toggleRequestLogMessage(entry) {
  if (!entry?.message) return
  const key = requestLogKey(entry)
  const next = new Set(expandedRequestLogIds.value)
  if (next.has(key)) {
    next.delete(key)
  } else {
    next.add(key)
  }
  expandedRequestLogIds.value = next
}

function clearExpandedRequestLogs() {
  expandedRequestLogIds.value = new Set()
}

function isForbidden(error) {
  return error?.status === 403
}

function requireLogin(showMessage = true) {
  setAdminToken('')
  loginToken.value = ''
  authRequired.value = true
  initialized.value = true
  subscriptions.value = []
  proxies.value = []
  poolRows.value = []
  poolStatus.value = { count: 0, instances: [] }
  availableStatus.value = {
    stage: 'idle',
    candidate_count: 0,
    quick_refreshing: false,
    background_refreshing: false,
    total: 0,
    pending: 0,
    tested: 0,
    healthy: 0,
    failed: 0,
    available_cache_ttl_seconds: 30,
    test_result_ttl_seconds: 7200,
    last_error: ''
  }
  diagnosticLogs.value = []
  requestLogs.value = []
  requestLogDates.value = []
  requestLogDate.value = todayDate()
  clearExpandedRequestLogs()
  if (showMessage) {
    notify('请重新登录', 'error')
  }
}

function notifyRequestError(error, prefix) {
  if (isForbidden(error)) {
    requireLogin()
    return
  }
  notify(`${prefix}${error.message}`, 'error')
}

function notify(message, type = 'info') {
  const id = Date.now() + Math.random()
  toasts.value.push({ id, message, type })
  setTimeout(() => {
    toasts.value = toasts.value.filter((toast) => toast.id !== id)
  }, 3200)
}
</script>

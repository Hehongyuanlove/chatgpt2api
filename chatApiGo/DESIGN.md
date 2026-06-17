# OpenAI 兼容网关设计

## 目录结构 (new files only, existing root files unchanged)

```
chatApiGo/
├── DESIGN.md            # 本设计文档
├── main.go              # 入口: server 子命令启动服务
├── config.go            # 配置结构体 + 环境变量加载
├── apitype.go           # OpenAI API 请求/响应类型定义
├── pool.go              # SessionPool: 多session管理, 刷新, 健康检查
├── translate.go         # 翻译: OpenAI ↔ ChatGPT 格式转换
├── srv_server.go        # HTTP 服务器启动, 路由注册
├── srv_chat.go          # POST /v1/chat/completions 处理器
├── srv_models.go        # GET /v1/models 处理器
├── srv_middleware.go    # API Key 鉴权, CORS, 限速
├── pow.go               # 现有 (不变)
├── turnstile.go         # 现有 (不变)
├── sentinel.go          # 现有 (不变)
├── conversation.go      # 现有 (不变)
├── sse.go               # 现有 (不变)
├── token.go             # 现有 (不变)
├── bootstrap.go         # 现有 (不变)
├── client.go            # 现有 (不变)
├── session.go           # 现有 (不变)
└── session.json         # 示例会话文件
```

## 架构分层

```
Client (OpenAI SDK/curl)
    │ POST /v1/chat/completions (stream=true/false)
    ▼
┌──────────────────────┐
│  srv_middleware.go    │  API Key 鉴权 + CORS
└──────────┬───────────┘
┌──────────▼───────────┐
│  srv_chat.go         │  处理器入口
└──────────┬───────────┘
┌──────────▼───────────┐
│  translate.go        │  请求翻译: OpenAI→ChatGPT内部格式
└──────────┬───────────┘
┌──────────▼───────────┐
│  pool.go             │  SessionPool → 选会话 → 获取许可证
└──────────┬───────────┘
┌──────────▼───────────┐
│  现有 Client         │  Bootstrap → Sentinel → SendConversation
└──────────┬───────────┘
┌──────────▼───────────┐
│  translate.go        │  响应翻译: ChatGPT SSE → OpenAI delta
└──────────┬───────────┘
    ▼ SSE stream / JSON response
Client
```

## 核心组件

### 1. SessionPool (`pool.go`)

**职责**: 管理多个ChatGPT会话, 提供健康会话供请求使用

```
SessionPool {
    sessions []*ManagedSession
    mu       sync.RWMutex
    strategy SelectionStrategy  // round-robin
}

ManagedSession {
    rawSession  *Session       // 原始Session (从session.json加载)
    client      *Client        // 对应Client实例
    state       SessionState   // active / expired / cooldown / banned
    inFlight    int64          // 当前处理中请求数
    semaphore   chan struct{}  // 最大并发令牌 (默认为1)
    proxyURL    string
    lastUsed    time.Time
    lastError   error
    failCount   int
}
```

**功能**:
- `LoadFromDir(dir string)` — 扫描目录下所有 `.json` 文件加载为 session
- `Acquire() (*ManagedSession, error)` — 获取最健康可用的会话 (blocking)
- `Release(sess *ManagedSession)` — 归还会话
- 后台刷新协程: 每30分钟检查所有会话, 需要刷新时自动执行
- 错误追踪: 连续失败 N 次标记为 `cooldown`, 冷却后重试

### 2. 翻译层 (`translate.go`)

**请求翻译 (OpenAI → ChatGPT)**:
- `OpenAIRequest` → `ChatGPTConversationPayload`
- Model 映射: `gpt-4o` → `auto`, `o1` → `o1`, `gpt-4o-mini` → `auto`
- Messages: system/ user/ assistant 转 ChatGPT 内部格式
- 多轮对话: 通过 `conversation_id` + `parent_message_id` 追踪
- Stream 参数传递 (force_use_sse)

**响应翻译 (ChatGPT SSE → OpenAI)**:
- 流式: 逐行解析 `data:` 事件 → 发射 `data: {"choices": [{"delta": {...}}]}`
- 非流式: 收集所有 delta 合并为完整响应
- Finish reason: `stop` / `length`
- Error 映射: ChatGPT 错误 → OpenAI APIError 格式

### 3. API 服务器 (`srv_*.go`)

**路由**:
- `POST /v1/chat/completions` — 流式/非流式聊天补全
- `GET /v1/models` — 可用模型列表
- `GET /v1/dashboard` — (可选) 会话状态面板

**中间件**:
- `apiKeyMiddleware` — 检查 `Authorization: Bearer <key>` (配置为空则跳过)
- `corsMiddleware` — 允许跨域
- `rateLimitMiddleware` — 令牌桶限速

### 4. 配置文件 (`config.go`)

环境变量驱动:
| 变量 | 默认 | 说明 |
|------|------|------|
| `OA_ENV` | `dev` | 运行环境 |
| `OA_LISTEN` | `:8080` | 监听地址 |
| `OA_SESSION_DIR` | `.` | session JSON 目录 |
| `OA_PROXY` | `` | HTTP/SOCKS5 代理 |
| `OA_API_KEY` | `` | API 鉴权密钥 (空=不鉴权) |
| `OA_MAX_CONCURRENT` | `1` | 每 session 最大并发 |
| `OA_SESSION_STRATEGY` | `round-robin` | 选session策略 |

### 5. OpenAI API 类型 (`apitype.go`)

```go
type ChatCompletionRequest struct {
    Model       string        `json:"model"`
    Messages    []Message     `json:"messages"`
    Stream      bool          `json:"stream"`
    MaxTokens   int           `json:"max_tokens"`
    Temperature float64       `json:"temperature"`
    TopP        float64       `json:"top_p"`
    User        string        `json:"user,omitempty"`
}

type ChatCompletionResponse struct {
    ID      string   `json:"id"`
    Object  string   `json:"object"`
    Created int64    `json:"created"`
    Model   string   `json:"model"`
    Choices []Choice `json:"choices"`
    Usage   *Usage   `json:"usage,omitempty"`
}

type ChatCompletionChunk struct {
    ID      string       `json:"id"`
    Object  string       `json:"object"`
    Created int64        `json:"created"`
    Model   string       `json:"model"`
    Choices []ChunkChoice `json:"choices"`
}
```

## 数据流: 请求周期

1. Client → `POST /v1/chat/completions`
2. `srv_middleware.go` → 验证 API Key (若配置)
3. `srv_chat.go` → 解析 JSON body
4. `translate.go: OpenAIRequest → ChatGPT messages`
5. `pool.Acquire()` → 获得 `ManagedSession`
6. `ManagedSession.client.Bootstrap()` (若未初始化)
7. `ManagedSession.client.GetChatRequirements()` → Sentinel 令牌
8. `ManagedSession.client.SendConversation()` → ChatGPT API
9. `sse.go` 逐行解析响应, 同时 `translate.go` 转换为 OpenAI 格式
10. 流式: 逐块 `flusher.Flush()` 发送 `data: {...}\n\n`
11. 非流式: 合并块, 发送完整 JSON
12. `pool.Release()`

## 错误处理

| ChatGPT 错误 | HTTP 状态 | OpenAI 格式 |
|---|---|---|
| Token 过期 (refresh 失败) | 401 | `{"error":{"code":"token_expired"}}` |
| Rate limit (429) | 429 | `{"error":{"code":"rate_limit_exceeded"}}` |
| Sentinel 失败 | 502 | `{"error":{"code":"upstream_error"}}` |
| 会话不可用 | 503 | `{"error":{"code":"session_unavailable"}}` |

## 后续可扩展

1. 会话健康检测: 定期发送 ping 请求验证
2. 请求队列: 所有会话繁忙时排队等待
3. 多用户鉴权: 多 API Key + 会话绑定
4. 指标暴露: Prometheus `/metrics`
5. Tools/Function Calling 支持
6. Vision (多模态) 支持
7. Dockerfile + docker-compose



chatApiGo server > /tmp/server.log 2>&1 &
SERVER_PID=$!
sleep 3

curl -sN http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"say hi in german"}],"stream":true}' \
  --max-time 120


测试	结果
GET /health	{"status":"ok"}
GET /v1/models	8 模型列表
API Key 鉴权 (无key)	401 {"error":{"code":"unauthorized"}}
API Key 鉴权 (有效key)	200
POST /v1/chat/completions (非流式)	"Hello, nice day! 👋"
POST /v1/chat/completions (流式)	增量 SSE chunks + [DONE]
核心流程验证:
请求 → API Key检查 → session池获取 → Bootstrap → Sentinel
→ conversation API → SSE解析 → delta diff → OpenAI格式响应
已实现功能:
- SessionPool: 多session加载, 30min自动刷新token, 并发控制, 失败冷却
- 翻译层: OpenAI ↔ ChatGPT 互转, delta 去重, 模型映射
- API层: CORS, API Key 鉴权, 流式/非流式, 优雅关闭
启动方式:
# 简单启动
go run . server

# 多session + 鉴权 + 代理
OA_SESSION_DIR=./sessions OA_API_KEY=sk-xxx OA_PROXY=http://127.0.0.1:7890 go run . server
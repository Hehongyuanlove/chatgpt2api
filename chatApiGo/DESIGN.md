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
    │ GET  /v1/conversations[/{id}]
    ▼
┌──────────────────────────┐
│  srv_middleware.go       │  API Key 鉴权 + CORS
└──────────┬───────────────┘
┌──────────▼───────────────┐
│  srv_chat.go             │  处理器入口
│  srv_conversations.go    │  对话列表/历史
└──────────┬───────────────┘
┌──────────▼───────────────┐
│  pool.go: AcquireSticky  │  粘性选择: convID→session 映射优先
│            / Acquire     │  无convID → round-robin 兜底
└──────────┬───────────────┘
┌──────────▼───────────────┐
│  ManagedSession          │  Bootstrap → Sentinel → SendConversation
└──────────┬───────────────┘
┌──────────▼───────────────┐
│  translate.go            │  响应翻译: ChatGPT SSE → OpenAI delta
│  流完成后 → BindConv()   │  convID 回写到 convSessions 映射
└──────────┬───────────────┘
    ▼ SSE stream / JSON response
Client
```

## 核心组件

### 1. SessionPool (`pool.go`)

**职责**: 管理多个ChatGPT会话, 提供健康会话供请求使用

```
SessionPool {
    sessions     []*ManagedSession
    mu           sync.RWMutex
    convSessions map[string]*ManagedSession  // conversation_id → session 粘性映射
    next         uint64                      // round-robin 计数器
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
- `Acquire() (*ManagedSession, error)` — round-robin 获取可用会话 (blocking)
- `AcquireSticky(ctx, convID) (*ManagedSession, error)` — 粘性获取: 优先返回 convID 绑定的会话; 无映射或会话不可用时降级为普通 Acquire
- `BindConversation(convID, ms)` — 将 conversation_id 绑定到 ManagedSession, 后续请求路由到同一会话
- `Release(sess *ManagedSession)` — 归还会话
- 后台刷新协程: 每30分钟检查所有会话, 需要刷新时自动执行
- 错误追踪: 连续失败 N 次标记为 `cooldown`, 冷却后重试
- 粘性映射自动清理: 绑定会话进入 cooldown/banned 时, 下次请求自动删除旧映射

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
- `GET /v1/conversations` — 会话列表
- `GET /v1/conversations/{id}` — 会话历史
- `GET /health` — 健康检查 `{"status":"ok"}`
- `GET /v1/dashboard` — 会话池统计 `pool.Stats()`

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
3. `srv_chat.go` → 解析 JSON body, 提取 `conversation_id`
4. `translate.go: OpenAIRequest → ChatGPT messages`
5. `pool.AcquireSticky(ctx, conversation_id)` — 若 convID 有绑定则直接返回该会话; 否则 round-robin
6. `ManagedSession.client.Bootstrap()` (若未初始化)
7. `ManagedSession.client.GetChatRequirements()` → Sentinel 令牌
8. `ManagedSession.client.SendConversation()` → ChatGPT API
9. `translate.go: SSE → OpenAI delta` 逐行解析, 提取 `conversation_id`
10. 流式: 逐块 `flusher.Flush()` 发送 `data: {...}\n\n`
11. 非流式: 合并块, 发送完整 JSON
12. `pool.BindConversation(convID, sess)` — 将响应中的 conversation_id 绑定到当前会话
13. `pool.Release()`

### 粘性会话 (Sticky Session)

**问题**: 多 ChatGPT 账号 (session) 共享一个池。轮询分配会导致对话 A 在 session 1 创建, 但后续请求落到 session 2, 引发 "conversation not found" 错误。

**方案**: `convSessions map` 维护 conversation_id → ManagedSession 映射。

- **绑定时机**: 对话创建/继续的 SSE 响应完成后, 调用 `BindConversation()` 写入映射
- **查找时机**: 请求携带 `conversation_id` 时, `AcquireSticky()` 优先查映射
- **降级**: 绑定会话繁忙/失效 → 自动删除映射, 降级为 round-robin
- **历史接口**: `GET /v1/conversations/{id}` 同样走粘性路由, 同时建立映射

## 错误处理

| ChatGPT 错误 | HTTP 状态 | OpenAI 格式 |
|---|---|---|
| Token 过期 (refresh 失败) | 401 | `{"error":{"code":"token_expired"}}` |
| Rate limit (429) | 429 | `{"error":{"code":"rate_limit_exceeded"}}` |
| Sentinel 失败 | 502 | `{"error":{"code":"upstream_error"}}` |
| 会话不可用 | 503 | `{"error":{"code":"session_unavailable"}}` |

## 启动方式

```bash
# 简单启动
go run . server

# 多 session + 鉴权 + 代理
OA_SESSION_DIR=./sessions OA_API_KEY=sk-xxx OA_PROXY=http://127.0.0.1:7890 go run . server

# 启动后自动加载 session 目录下所有 .json 文件
```

## 快速测试

```bash
# 新对话
curl -s http://localhost:8080/v1/chat/completions \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"Hi"}],"stream":false}'

# 拿到响应中的 conversation_id, 继续对话
curl -s http://localhost:8080/v1/chat/completions \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"继续"}],"stream":true,"conversation_id":"<conv_id>"}'

# 流式测试
curl -sN http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"say hi in german"}],"stream":true}' \
  --max-time 120
```

## 功能验证

| 测试 | 预期 |
|------|------|
| GET /health | `{"status":"ok"}` |
| GET /v1/models | 模型列表 |
| POST 聊天 (非流式) | JSON 响应含 choices |
| POST 聊天 (流式) | SSE chunks → `[DONE]` |
| API Key 鉴权 (无 key) | 401 |
| API Key 鉴权 (有效 key) | 200 |
| 连续对话 (带 conversation_id) | 同一 session 处理 |
| 对话历史 GET /v1/conversations/{id} | 消息列表 |

## 后续可扩展

1. 会话健康检测 — 定期 ping 验证
2. 请求队列 — 全部繁忙时排队
3. 多用户鉴权 — 多 API Key + session 绑定
4. 指标暴露 — Prometheus `/metrics`
5. Tools / Function Calling
6. Vision (多模态)
7. Dockerfile + docker-compose
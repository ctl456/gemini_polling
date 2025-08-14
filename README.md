# Gemini Polling 🚀(个人)

一个高性能、高可用的 Google Gemini API 轮询代理。它将多个 Gemini API Key 汇聚成一个单一的、同时支持 **OpenAI 格式**和 **Gemini 原生格式**的 API 端点，并提供了负载均衡、自动故障切换和强大的 Web 管理后台。

[![Go Version](https://img.shields.io/badge/go-1.23%2B-blue.svg)](https://golang.org/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Docker](https://img.shields.io/badge/docker-ready-blue.svg?logo=docker)](https://hub.docker.com/)

---

## ✨ 核心功能

*   **双 API 引擎**:
    *   **OpenAI 格式兼容**: 无缝接入各类第三方应用。
        *   支持 `/v1/chat/completions` (流式与非流式)。
        *   支持 `/v1/models` 模型列表。
        *   **支持函数调用 (Function Calling)**，可传递 `tools` 和 `tool_choice` 参数。
    *   **Gemini 原生代理**: 提供原生 Gemini API 体验。
        *   支持 `/v1beta/models/{model}:generateContent` (非流式)。
        *   支持 `/v1beta/models/{model}:streamGenerateContent` (流式)。
        *   支持 `/v1beta/models/{model}:countTokens`。
        *   支持 `/v1beta/models` 模型列表。

*   **智能密钥池**:
    *   **API Key 轮询池**: 将您所有的 Gemini API Key 添加到池中，程序会自动进行负载均衡，随机选择一个可用 Key 处理请求。
    *   **自动故障切换**: 当某个 Key 因额度耗尽、被封禁或遇到速率限制时，系统会自动尝试下一个可用 Key，对用户透明。
    *   **智能速率限制处理**: 自动识别 `429 (Too Many Requests)` 错误，并临时禁用相关 Key 一段可配置的时间，避免 Key 被永久封禁。
    *   **全自动健康检查**: 后台服务会**定期扫描所有 Key**（包括已启用和已禁用），自动禁用失效的 Key，并**自动重新启用**已恢复的 Key。

*   **强大的 Web 管理后台**:
    *   **仪表盘**: 集中管理所有 API Keys，可在“已启用”、“已禁用”、“临时禁用”状态间切换查看。
    *   **状态监控**: 在“临时禁用”列表中可查看被 429 限制的 Key 及其解禁倒计时。
    *   **批量操作**: 支持批量添加、删除、校验 Keys，批量添加时自动去重。
    *   **手动扫描**: 可在后台随时触发对所有 Key 的全面健康检查。
    *   **系统设置**: 在 Web 界面中直接修改 `.env` 配置文件，并支持热重载（部分配置需重启生效）。

*   **灵活部署与访问控制**:
    *   **灵活的数据库支持**: 支持 SQLite (开箱即用) 和 MySQL，方便生产环境部署。
    *   **配置热重载**: 大部分配置（如 Admin Key, Polling Key, 重试策略等）修改后可立即生效，无需重启服务。
    *   **统一访问控制**: 可为所有公共 API 端点设置独立的访问密钥（Bearer Token），同时兼容 OpenAI 的 `Authorization` 和 Gemini 的 `x-goog-api-key` Header。
    *   **Docker Ready**: 提供优化后的 `Dockerfile`，支持快速容器化部署。

## 🔧 安装与部署

### 1. 先决条件

*   [Go](https://go.dev/doc/install) (版本 1.23 或更高)
*   Git

### 2. 克隆项目

```bash
git clone https://github.com/ctl456/gemini_polling.git
cd gemini_polling
```

### 3. 配置环境

项目通过根目录下的 `.env` 文件进行配置。您可以复制 `.env.example` 来创建自己的配置文件：

```bash
cp .env.example .env
```

然后，编辑 `.env` 文件。请务_必修改 `ADMIN_API_KEY`_ 为一个复杂的随机字符串。

```dotenv
# .env

# --- 服务器配置 ---
# 服务监听的端口 (数据库和端口更改后需要重启程序)
SERVER_PORT=8080

# --- API Keys ---
# 管理后台的登录密钥，请务必修改为一个复杂的随机字符串！
ADMIN_API_KEY=your_super_secret_admin_key

# 访问 /v1 公共接口所需的 Bearer Token。如果留空，则 /v1 路径无需认证即可访问。
POLLING_API_KEY=your_optional_public_api_key

# --- 轮询策略 ---
# 单次请求失败后，尝试使用不同 Key 进行重试的最大次数。
MAX_RETRIES=5

# 当一个 Key 遇到 429 错误时，临时禁用的时长（单位：秒）。
RATE_LIMIT_COOLDOWN=60

# --- 健康检查 ---
# 后台 Key 健康检查的并发数。根据你的网络环境和机器性能调整。
# 7000个key建议设置为 50-100
HEALTH_CHECK_CONCURRENCY=10

# --- 数据库配置 (二选一) ---
# 数据库驱动，可选值为: "sqlite3" 或 "mysql" (更改后需要重启程序)
DB_DRIVER=sqlite3

# 如果使用 "sqlite3"
SQLITE_PATH=./data/data.db

# 如果使用 "mysql" (请确保下面的信息正确)
MYSQL_HOST=127.0.0.1
MYSQL_PORT=3306
MYSQL_DBNAME=gemini_proxy
MYSQL_USER=root
MYSQL_PASSWORD=your_mysql_password
```

### 4. Docker 部署 (推荐)

```bash
# 构建 Docker 镜像
docker build -t gemini-polling .

# 运行容器
# sqlite3
docker run -d --name gemini-app -p 8080:8080 \
  -v $(pwd)/data:/app/data \
  -e SERVER_PORT="8080" \
  -e ADMIN_API_KEY="请务必修改为一个复杂的随机字符串" \
  -e POLLING_API_KEY="sk-Tkxxxx(用于对话的密钥)" \
  -e HEALTH_CHECK_CONCURRENCY="50" \
  -e MAX_RETRIES="10" \
  -e RATE_LIMIT_COOLDOWN="7200" \
  -e DB_DRIVER="sqlite3" \
  -e SQLITE_PATH="./data/data.db" \
  --restart always \
  gemini-polling

# mysql
docker run -d --name gemini-app -p 8080:8080 \
  -v $(pwd)/data:/app/data \
  -e SERVER_PORT="8080" \
  -e ADMIN_API_KEY="请务必修改为一个复杂的随机字符串" \
  -e POLLING_API_KEY="sk-Tkxxxx(用于对话的密钥)" \
  -e HEALTH_CHECK_CONCURRENCY="50" \
  -e MAX_RETRIES="10" \
  -e RATE_LIMIT_COOLDOWN="7200" \
  -e DB_DRIVER="mysql" \
  -e MYSQL_HOST="127.0.0.1" \
  -e MYSQL_PORT="3306" \
  -e MYSQL_DBNAME="gemini_proxy" \
  -e MYSQL_USER="root" \
  -e MYSQL_PASSWORD="your_mysql_password" \
  --restart always \
  gemini-polling
```
* `-p 8080:8080`: 将容器的 8080 端口映射到主机的 8080 端口。
* `-v ./data:/app/data`: 将主机当前目录下的 `data` 文件夹挂载到容器的 `/app/data` 目录，用于持久化 SQLite 数据库。

### 5. 本地编译运行

```bash
# 自动下载依赖
go mod tidy

# 编译项目
go build -o gemini-polling

# 运行程序
./gemini-polling
```

程序启动后，您会看到类似以下的输出：
```
=========================================================
  服务器正在启动，监听地址: http://localhost:8080
  管理后台登录地址:     http://localhost:8080/admin/login.html
---
  聊天 API Endpoint:      http://localhost:8080/v1/chat/completions
  Gemini 原生格式 API:    http://localhost:8080/v1beta/models/gemini-pro:generateContent
  访问公共 API 认证:     Bearer Token (或 无)
=========================================================
```

## ⚙️ 后台管理

### 1. 登录
![登录页面](./Images/login.png)
访问 `http://localhost:8080/admin/login.html`，输入您在 `.env` 文件中设置的 `ADMIN_API_KEY` 进行登录。

### 2. Key 管理
![Key管理页面](./Images/admin.png)
在 "Key 管理" 页面，您可以：
*   **切换视图**: 在 "已启用", "已禁用", "临时禁用" 列表之间切换。
*   **查看临时禁用**: 查看因 429 被限制的 Key 和解禁倒计时。
*   **批量操作**: 批量添加（自动去重）、删除、校验 Keys。
*   **全面健康检查**: 手动触发一次对所有 Key 的后台健康扫描。

### 3. 系统设置
![设置页面](./Images/setting.png)
在 "系统设置" 页面，您可以直接修改 `.env` 文件中的配置项。大部分配置（如 API Keys, 重试策略）修改并保存后会立即生效。

## 🔌 API 使用

如果设置了 `POLLING_API_KEY`，所有请求都需要进行认证。认证方式兼容两种 Header:
*   `Authorization: Bearer <POLLING_API_KEY>`
*   `x-goog-api-key: <POLLING_API_KEY>`

### 1. OpenAI 兼容接口

#### 非流式请求
**Request:**
```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your_optional_public_api_key" \
  -d 
  {
    "model": "gemini-pro",
    "messages": [{"role": "user", "content": "你好，请介绍一下你自己"}],
    "stream": false,
    "temperature": 0.7
  }
```
**Response:**
```json
{
  "id": "chatcmpl-",
  "object": "chat.completion",
  "created": 1715000000,
  "model": "gemini-pro",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "你好！我是一个大型语言模型..."
      },
      "finish_reason": "stop"
    }
  ],
  "usage": { ... }
}
```

#### 流式请求
将请求体中的 `"stream": true` 即可使用流式响应，响应格式为 Server-Sent Events (SSE)。


### 2. Gemini 原生接口

#### generateContent (非流式)
**Request:**
```bash
curl http://localhost:8080/v1beta/models/gemini-pro:generateContent \
  -H "Content-Type: application/json" \
  -H "x-goog-api-key: your_optional_public_api_key" \
  -d 
  {
    "contents": [{"parts": [{"text": "你好，请介绍一下你自己"}]}]
  }
```
**Response:**
```json
{
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "text": "你好！我是一个由 Google 训练的..."
          }
        ],
        "role": "model"
      },
      "finishReason": "STOP",
      ...
    }
  ],
  ...
}
```

#### streamGenerateContent (流式)
**Request:**
```bash
curl http://localhost:8080/v1beta/models/gemini-pro:streamGenerateContent?alt=sse \
  -H "Content-Type: application/json" \
  -H "x-goog-api-key: your_optional_public_api_key" \
  -d 
  {
    "contents": [{"parts": [{"text": "写一首关于宇宙的诗"}]}]
  }
```
响应为 Gemini 原生的 SSE 流。

## 📂 项目结构

```
.
├── config/              # 配置管理与 .env 文件处理
├── handler/             # Gin 的 HTTP 请求处理器
├── middleware/          # Gin 中间件（如认证）
├── model/               # 数据库模型 (GORM) 和 API 数据结构
├── service/             # 核心业务逻辑（如 Gemini 请求、Key 健康检查）
├── static/              # 存放前端 HTML/CSS/JS 文件
├── storage/             # 数据库交互层 (GORM)
├── .env.example         # 配置文件示例
├── Dockerfile           # Docker 配置文件
├── go.mod               # Go 模块文件
├── main.go              # 程序入口
└── README.md            # 项目说明文档
```

## 🤝 贡献

欢迎提交 Pull Requests 或 Issues 来改进此项目！

## 📄 许可证

本项目基于 [MIT License](LICENSE) 开源。

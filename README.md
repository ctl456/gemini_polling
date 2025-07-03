# Gemini Polling 🚀(个人)

一个高性能、高可用的 Google Gemini API 轮询代理。它将多个 Gemini API Key 汇聚成一个单一的、兼容 OpenAI 格式的 API 端点，并提供了负载均衡、自动故障切换和强大的 Web 管理后台。

[![Go Version](https://img.shields.io/badge/go-1.23%2B-blue.svg)](https://golang.org/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

---

## ✨ 核心功能

*   **OpenAI 格式兼容**: 完全兼容 OpenAI 的 `/v1/chat/completions` 流式接口，可无缝接入各类第三方应用。
*   **API Key 轮询池**: 将您所有的 Gemini API Key 添加到池中，程序会自动进行负载均衡，随机选择一个可用 Key 处理请求。
*   **自动故障切换**: 当某个 Key 因为额度耗尽、被封禁或遇到 429 速率限制时，系统会自动尝试下一个可用 Key，对用户透明。
*   **智能速率限制处理**: 自动识别 `429 (Too Many Requests)` 错误，并临时禁用相关 Key 一段可配置的时间，避免 Key 被永久封禁。
*   **强大的 Web 管理后台**:
    *   **仪表盘**: 集中管理所有 API Keys（启用/禁用）。
    *   **批量操作**: 支持批量添加、删除、校验 Keys。
    *   **自动验证**: 新增 Key 时自动验证其有效性，无效 Key 会被自动禁用。
    *   **定期扫描**: 后台定时任务，自动扫描并禁用所有失效的 Key。
    *   **系统设置**: 在 Web 界面中直接修改 `.env` 配置文件，并支持热重载（部分配置需重启生效）。
*   **灵活的数据库支持**: 支持 SQLite (开箱即用) 和 MySQL，方便生产环境部署。
*   **配置热重载**: 大部分配置（如 Admin Key, Polling Key, 重试策略等）修改后可立即生效，无需重启服务。
*   **访问控制**: 可为公共 API 端点设置独立的访问密钥（Bearer Token）。

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

然后，编辑 `.env` 文件。以下是所有配置项的说明：

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

# --- 数据库配置 (二选一) ---
# 数据库驱动，可选值为: "sqlite3" 或 "mysql" (更改后需要重启程序)
DB_DRIVER=sqlite3

# 如果使用 "sqlite3"
SQLITE_PATH=./data.db

# 如果使用 "mysql" (请确保下面的信息正确)
MYSQL_HOST=127.0.0.1
MYSQL_PORT=3306
MYSQL_DBNAME=gemini_proxy
MYSQL_USER=root
MYSQL_PASSWORD=your_mysql_password
```
### 4. Docker部署
确保您已经准备好了 `.env` 配置文件。`Dockerfile` 会将项目根目录下的 `.env` 文件复制到镜像中。
```bash
docker build -t gemini-polling .
```
```bash
docker run -d \
  --name gemini-polling-container \
  -p 8080:8080 \
  -v ./data.db:/app/data.db \
  gemini-polling
```


### 5. 编译并运行

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
  访问 /v1 路径认证:     Bearer Token (或 无)
=========================================================
```

## ⚙️ 后台管理

### 1. 登录
![admin](./Images/login.png)

访问 `http://localhost:8080/admin/login.html`，输入您在 `.env` 文件中设置的 `ADMIN_API_KEY` 进行登录。

### 2. Key 管理

![admin](./Images/admin.png)

在 "Key 管理" 页面，您可以：
*   **批量添加 Key**: 在文本框中每行输入一个 Gemini API Key，然后点击 "开始批量添加"。
*   **查看 Key 列表**: 分页查看所有 "已启用" 或 "已禁用" 的 Key。
*   **校验 Key**: 点击单个 Key 后的 "校验" 按钮，或勾选多个 Key 后点击 "校验选中"，可立即验证其有效性。
*   **删除 Key**: 单个删除或批量删除。
*   **扫描所有 Key**: 点击 "扫描所有启用Key" 会触发一个后台任务，全面检查所有已启用的 Key，并自动禁用失效的 Key。

### 3. 系统设置

![admin](./Images/setting.png)
在 "系统设置" 页面，您可以直接修改 `.env` 文件中的配置项。
*   **热重载**: 大部分配置（如 API Keys, 重试策略）修改并保存后会立即生效。
*   **重启提示**: `服务器端口` 和 `数据库配置` 等底层设置修改后，需要您**手动重启程序**才能生效。

## 🔌 API 使用

API 端点与 OpenAI 的 `chat/completions` 接口兼容。

*   **URL**: `http://<your-server-address>/v1/chat/completions`
*   **Method**: `POST`
*   **Auth**: `Authorization: Bearer <POLLING_API_KEY>` (如果在 `.env` 中设置了 `POLLING_API_KEY`)
*   **Body**: JSON 格式，与 OpenAI 请求体一致。**必须**包含 `stream: true`。

### Curl 示例

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your_optional_public_api_key" \
  -d '{
    "model": "gemini-pro",
    "messages": [
      {
        "role": "user",
        "content": "你好，请介绍一下你自己"
      }
    ],
    "stream": true,
    "temperature": 0.7
  }'
```

### 响应格式 (SSE)

响应是 Server-Sent Events (SSE) 流，每一块内容都是一个 `data:` 事件。

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":17000000,"model":"gemini-pro","choices":[{"index":0,"delta":{"content":"你好！"},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":17000000,"model":"gemini-pro","choices":[{"index":0,"delta":{"content":"我是一个"},"finish_reason":null}]}

...

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","created":17000000,"model":"gemini-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

## 📂 项目结构

```
.
├── config/              # 配置管理与 .env 文件处理
├── handler/             # Gin 的 HTTP 请求处理器
├── middleware/          # Gin 中间件（如认证）
├── model/               # 数据库模型 (GORM) 和 API 数据结构
├── service/             # 核心业务逻辑（如 Gemini 请求、Key 扫描）
├── static/              # 存放前端 HTML/CSS/JS 文件
├── storage/             # 数据库交互层 (GORM)
├── .env.example         # 配置文件示例
├── go.mod               # Go 模块文件
├── main.go              # 程序入口
└── README.md            # 项目说明文档
```

## 🤝 贡献

欢迎提交 Pull Requests 或 Issues 来改进此项目！

## 📄 许可证

本项目基于 [MIT License](LICENSE) 开源。
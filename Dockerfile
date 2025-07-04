# --- Stage 1: Build ---
# 使用官方的 Go 镜像作为构建环境
FROM golang:1.23-alpine3.21 AS builder

# 安装 SQLite 开发依赖
RUN apk add --no-cache gcc musl-dev

# 设置工作目录
WORKDIR /app

# 复制 go.mod 和 go.sum 文件
COPY go.mod go.sum ./

# 国内环境换源
# RUN go env -w GO111MODULE=on
# RUN go env -w GOPROXY=https://goproxy.cn,direct

# 下载依赖项
# 这一步会被缓存，除非 go.mod/go.sum 发生变化
RUN go mod download

# 复制项目的其余源代码
COPY . .

# 编译应用
# -o /app/gemini-polling 指定输出文件
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /app/gemini-polling .

# --- Stage 2: Final ---
# 使用一个非常小的基础镜像，比如 alpine
FROM alpine:latest

# 设置工作目录
WORKDIR /app

# 从 builder 阶段复制编译好的二进制文件
COPY --from=builder /app/gemini-polling /app/

# 复制静态文件 (Web 管理后台)
COPY static/ ./static/

# 复制 .env 配置文件
# 注意：在生产环境中，更推荐使用 Docker 的 secrets 或环境变量来管理敏感信息
# 但为了方便，这里我们直接复制 .env 文件
COPY .env ./.env

# 暴露应用程序在 .env 中配置的端口 (默认为 8080)
# 这只是元数据，实际端口映射在 `docker run` 或 `docker-compose` 中完成
EXPOSE 8080

VOLUME /app/data

# 容器启动时运行的命令
# 程序会读取同目录下的 .env 和 static 文件夹
ENTRYPOINT ["/app/gemini-polling"]

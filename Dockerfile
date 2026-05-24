# 构建阶段
FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum* ./
RUN go mod download 2>/dev/null || true

COPY . .
RUN go build -ldflags="-s -w" -o mirror-proxy ./cmd/main.go

# 运行阶段
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /app

# 创建必要目录
RUN mkdir -p web/static cache

# 复制编译后的二进制文件和静态资源
COPY --from=builder /app/mirror-proxy .
COPY --from=builder /app/web/static ./web/static

# 暴露默认端口
EXPOSE 8080

VOLUME ["/app/cache"]

ENTRYPOINT ["./mirror-proxy"]

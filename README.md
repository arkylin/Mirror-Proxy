# Mirror Proxy - 镜像代理服务

Go 开发的远程代理软件，支持加速下载 Docker Hub、GHCR、GitHub 资源。带网页管理后台，可独立分配带鉴权的代理链接。

## 功能

- **Docker Hub 代理** - 加速 Docker 镜像拉取
- **GHCR 代理** - 加速 GitHub Container Registry 镜像
- **GitHub 代理** - 加速 GitHub 资源、Release 下载
- **网页管理后台** - 创建/管理代理链接
- **独立鉴权** - 每个链接有独立的 `ID` + `Token`
- **速率限制** - 按链接配置请求频率限制
- **Basic Auth** - 管理后台带密码保护

## 快速开始

```bash
# 编译
go build -o mirror-proxy ./cmd/main.go

# 运行（默认 :8080）
./mirror-proxy

# 自定义端口
LISTEN_ADDR=:9090 ./mirror-proxy
```

管理后台: http://localhost:8080/admin/
- 用户名: `admin`
- 密码: `admin123`

首次运行自动生成 `config.json`，可修改账号密码。

## Docker 部署

### 使用 Docker Compose（推荐）

无需克隆仓库，一条命令即可完成部署。

**1. 创建 `docker-compose.yml`**

```bash
cat > docker-compose.yml << 'EOF'
services:
  mirror-proxy:
    image: ghcr.io/arkylin/mirror-proxy:latest
    container_name: mirror-proxy
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ./config.json:/app/config.json
      - ./cache:/app/cache
    environment:
      - TZ=Asia/Shanghai
EOF
```

**2. 启动服务**

```bash
docker-compose up -d
```

首次启动会自动创建 `config.json`（默认账号 `admin` / `admin123`）。

**3. 常用命令**

```bash
# 查看日志
docker-compose logs -f

# 查看运行状态
docker-compose ps

# 停止服务
docker-compose down

# 重启服务
docker-compose restart

# 更新到最新镜像
docker-compose pull && docker-compose up -d
```

**4. 管理后台**

访问 `http://<服务器IP>:8080/admin/`，默认账号：
- 用户名：`admin`
- 密码：`admin123`

> 建议首次登录后立即在 **系统设置** 中修改密码。

---

### 使用 Docker 直接运行

```bash
# 拉取并运行（首次运行自动生成 config.json）
docker run -d \
  --name mirror-proxy \
  -p 8080:8080 \
  -v $(pwd)/config.json:/app/config.json \
  -v $(pwd)/cache:/app/cache \
  --restart unless-stopped \
  ghcr.io/arkylin/mirror-proxy:latest
```

---

### 自行构建镜像

如需本地构建：

```bash
# 克隆仓库后执行
docker build -t mirror-proxy .
docker run -d --name mirror-proxy -p 8080:8080 mirror-proxy
```

## 使用方式

### 1. Docker Hub / GHCR（daemon.json 方式）

创建类型为 `docker` 或 `ghcr` 的链接，获取代理地址：

```
https://yourdomain.com/{linkID}/{token}/
```

编辑 `/etc/docker/daemon.json`：

```json
{
  "registry-mirrors": ["https://yourdomain.com/{linkID}/{token}/"]
}
```

重启 Docker：

```bash
systemctl restart docker
```

之后正常使用即可：

```bash
docker pull nginx
docker pull ghcr.io/owner/repo:tag
```

### 2. GitHub 资源

创建类型为 `github` 的链接：

```bash
curl -O https://yourdomain.com/{linkID}/{token}/github.com/user/repo/releases/download/v1.0/app.tar.gz
```

## 工作原理

代理只做两件事：

1. **路径鉴权** - 验证 URL 中的 `linkID/token`，通过后去掉前缀
2. **原样转发** - 用 `httputil.ReverseProxy` 转发请求到上游（Docker Hub / GHCR / GitHub）

对于公开镜像，Docker Hub 返回 401 后，Docker 客户端**自己去 auth.docker.io 获取 token**，不需要代理参与。拿到 token 后再次请求代理，代理把 `Authorization` header 原样转发给 Docker Hub 即可。

```
Client → 代理 GET /linkID/token/v2/library/nginx/manifests/latest
              验证 linkID/token，去掉前缀
              → 转发给 registry-1.docker.io

Client ← 代理 ← Docker Hub 返回 401 + WWW-Authenticate
         （原样返回，Docker 客户端自己去 auth.docker.io 拿 token）

Client → 代理 GET /linkID/token/v2/... + Authorization: Bearer xxx
              验证 linkID/token，去掉前缀
              → 带 Authorization 转发给 Docker Hub

Client ← 代理 ← Docker Hub 返回镜像数据
```

## 目录结构

```
mirror-proxy/
├── cmd/
│   └── main.go              # 入口，路由注册
├── internal/
│   ├── config/              # 配置读写（config.json）
│   ├── auth/                # linkID/token 鉴权 + 速率限制
│   ├── proxy/               # 反向代理（Docker Hub / GHCR / GitHub）
│   ├── admin/               # 管理后台 REST API
│   └── middleware/          # CORS、Basic Auth、Logger
├── web/
│   └── static/
│       └── index.html       # 管理后台页面
├── config.json              # 配置文件（自动创建）
└── go.mod
```

## 配置说明

`config.json`：

```json
{
  "listen_addr": ":8080",
  "admin_user": "admin",
  "admin_pass": "your-password",
  "links": {
    "xjhdnkcusbnsjc": {
      "id": "xjhdnkcusbnsjc",
      "name": "Docker Hub 代理",
      "token": "a1b2c3d4...",
      "type": "docker",
      "enabled": true,
      "rate_limit": 100
    }
  }
}
```

| 字段 | 说明 |
|------|------|
| `listen_addr` | 监听地址，默认 `:8080` |
| `admin_user` / `admin_pass` | 管理后台账号密码 |
| `links` | 代理链接，每个链接有独立 ID + Token |
| `type` | `docker` / `ghcr` / `github` |
| `rate_limit` | 每分钟请求数限制，0 为不限 |
| `enabled` | 是否启用 |

## Nginx 反向代理配置（HTTPS）

```nginx
server {
    listen 443 ssl;
    server_name yourdomain.com;

    ssl_certificate /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Docker 需要大文件上传/下载
        client_max_body_size 0;
        proxy_buffering off;
        proxy_request_buffering off;
    }
}
```

## GitHub Actions 自动发布

项目已配置 GitHub Actions Workflow（`.github/workflows/ghcr.yml`），自动构建多架构镜像并推送到 GHCR。

**触发条件：**
- Push 到 `main` / `master` 分支 → 构建并推送 `latest` 标签
- Push `v*` 标签 → 构建并推送语义化版本标签（如 `v1.2.3`、`v1.2`、`v1`）

**使用方法：**

1. 确保仓库 **Settings → Actions → General → Workflow permissions** 中勾选 **Read and write permissions**
2. 提交代码并推送标签：
   ```bash
   git tag v1.0.0
   git push origin v1.0.0
   ```
3. 在仓库 **Packages** 页面查看已发布的镜像

**支持的架构：** `linux/amd64`, `linux/arm64`

## 防火墙 / 安全建议

- 外网部署建议用 Nginx + HTTPS
- 可配合防火墙限制只有特定 IP 访问管理后台和 `/api/`
- 定期更换 token（管理后台支持一键重置）
- 每个用户/团队分配独立链接，方便追溯和管控

# 管理后台前端构建与部署

TunnelMesh 管理后台是 `web/` 下的 Vue 3 + Vite 应用。前端 API 使用相对路径
`/api/`，WebSocket 使用相对路径 `/ws/`，因此生产环境必须让静态文件和这些
路径使用同一个 HTTPS 域名，或由同一个反向代理转发。

## 本地构建

要求 Node.js 22（与 Dockerfile 的 `NODE_VERSION` 一致）和 npm：

```bash
cd web
npm ci
npm test -- --run
npm run build
```

构建产物位于 `web/dist/`。提交或发布前检查：

```bash
test -f web/dist/index.html
find web/dist -type f -print | sort
```

不要提交 `web/node_modules/` 或本地缓存；依赖版本以 `package-lock.json` 为准。

## Server 内嵌模式（默认）

Server 使用 Go `embed` 提供管理后台静态文件。构建 Server 前，必须先生成
`web/dist/`，再同步到 `internal/server/web_dist/`：

```bash
cd web
npm ci
npm test -- --run
npm run build
cd ..
rm -rf internal/server/web_dist
mkdir -p internal/server/web_dist
cp -a web/dist/. internal/server/web_dist/
GODEBUG=gotypesalias=1 GOEXPERIMENT=aliastypeparams go build ./cmd/tunnelmesh-server
```

Dockerfile 已在多阶段构建中自动完成上述同步。生产发布必须确认嵌入目录来自
本次构建，不能使用旧的本地 bundle：

```bash
docker build --build-arg APP=server -t tunnelmesh:server .
```

该模式下 Nginx 只需要反代到 Server，配置见 [Nginx 推荐配置](nginx.md)。

## Nginx 独立静态文件模式

独立托管适合前端发布节奏独立于 Server 的场景。将 `web/dist/` 发布到只读目录，
例如 `/var/www/tunnelmesh/`，并让 Nginx 在同一 HTTPS 虚拟主机中优先反代 API、
WebSocket 和健康检查：

下面示例假设已定义 `upstream tunnelmesh_server` 和
`map $http_upgrade $connection_upgrade`；两项定义可直接复用
[Nginx 推荐配置](nginx.md)开头的内容。

```nginx
server {
    listen 443 ssl http2;
    server_name admin.tunnel.example.com;
    root /var/www/tunnelmesh/current;

    location ^~ /api/ {
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Authorization $http_authorization;
    }

    location = /ws/agent {
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header Authorization $http_authorization;
        proxy_buffering off;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }
    location = /ws/client {
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header Authorization $http_authorization;
        proxy_buffering off;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }
    location = /health/live { proxy_pass http://tunnelmesh_server; }
    location = /health/ready { proxy_pass http://tunnelmesh_server; }

    location = /index.html {
        add_header Cache-Control "no-cache" always;
    }
    location /assets/ {
        expires 1y;
        add_header Cache-Control "public, max-age=31536000, immutable" always;
        try_files $uri =404;
    }

    # Vue Router history fallback must be last.
    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

实际部署时，`/ws/agent` 和 `/ws/client` 还必须补齐 HTTP/1.1、Upgrade、
`Authorization`、超时和 `proxy_buffering off` 设置；可直接复用
[`nginx.md`](nginx.md) 中的 WebSocket location。不要把静态文件虚拟主机和托管
隧道域名混用，否则 `location /` 的 SPA fallback 会吞掉本应进入 Server 的托管
路由。推荐使用单独的 `admin.tunnel.example.com`，并将它加入
`security.allowed_hosts`/`security.allowed_origins`（如需 WebSocket 管理连接）。

发布静态文件时使用临时目录和原子切换，避免用户看到半套 bundle：

```bash
release_dir=/var/www/tunnelmesh/releases/2026-09-08
install -d -m 0755 "$release_dir"
cp -a web/dist/. "$release_dir/"
ln -sfn "$release_dir" /var/www/tunnelmesh/current
nginx -t && systemctl reload nginx
```

将 Nginx `root` 指向 `/var/www/tunnelmesh/current`，并确保 Nginx 用户至少拥有
静态文件和目录的读取权限。旧 release 在确认回滚窗口结束后再清理。

## 缓存与验证

后台支持简体中文/英文，首次语言取浏览器 `navigator.languages`，手动切换写入 `tunnelmesh_locale` localStorage。创建/重置子账号的临时密码仅在内存弹窗中存在，关闭弹窗或离开页面会清理。

- `index.html` 使用 `Cache-Control: no-cache` 或较短 TTL，确保发布后能发现新资源。
- Vite 生成的带 hash 的 `assets/*` 可以使用 `Cache-Control: public, max-age=31536000, immutable`。
- `/api/`、`/ws/`、`/health/` 和 `/metrics` 不应套用静态资源缓存。

```bash
nginx -t
curl -fsSI https://admin.tunnel.example.com/
curl -fsS https://admin.tunnel.example.com/health/live
curl -fsS https://admin.tunnel.example.com/health/ready
```

前端改动的最小质量门禁是：`npm test -- --run`、`npm run build`、Server 构建、
Nginx 配置检查，以及登录页、API 请求和 Agent/Client WebSocket 的冒烟验证。

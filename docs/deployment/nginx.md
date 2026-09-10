# TunnelMesh Nginx 推荐配置

本文适用于 Nginx 作为公网 HTTPS/WSS 入口，TunnelMesh Server 运行在本机 `127.0.0.1:8080`。公网只开放 80/443；Agent、Client 和 SSH over WebSocket 都通过 HTTPS/WSS 复用入口。

管理后台前端有两种部署方式：默认由 Server 内嵌 `internal/server/web_dist` 提供，
或由 Nginx 独立托管 `web/dist`。独立静态文件的构建、发布和 `try_files` 配置见
[管理后台前端构建与部署](frontend.md)；下面的配置适用于 Server 内嵌模式，
也可作为独立静态模式的 API/WSS 反代部分。

## 推荐配置

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

limit_req_zone $binary_remote_addr zone=tunnelmesh_api:10m rate=20r/s;
limit_conn_zone $binary_remote_addr zone=tunnelmesh_ws:10m;

upstream tunnelmesh_server {
    server 127.0.0.1:8080;
    keepalive 32;
}

server {
    listen 80;
    server_name tunnel.example.com
        "~^tm-[a-z0-9](?:[a-z0-9-]{0,58}[a-z0-9])?\.tunnel\.example\.com$"
        "~^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?-(?:[0-9]{1,3}-){4}[0-9]{1,5}\.apps\.example\.com$";
    return 308 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name tunnel.example.com
        "~^tm-[a-z0-9](?:[a-z0-9-]{0,58}[a-z0-9])?\.tunnel\.example\.com$"
        "~^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?-(?:[0-9]{1,3}-){4}[0-9]{1,5}\.apps\.example\.com$";

    ssl_certificate     /etc/letsencrypt/live/tunnel.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/tunnel.example.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:TLS:10m;

    client_max_body_size 2m;
    proxy_read_timeout 180s;
    proxy_send_timeout 180s;
    proxy_connect_timeout 5s;

    # API must precede SPA fallback.
    location ^~ /api/ {
        limit_req zone=tunnelmesh_api burst=40 nodelay;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header Authorization $http_authorization;
    }

    # Agent and Client authenticated WebSockets.
    location = /ws/agent {
        limit_conn tunnelmesh_ws 100;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }

    location = /ws/client {
        limit_conn tunnelmesh_ws 100;
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        proxy_set_header Origin $http_origin;
        proxy_set_header Authorization $http_authorization;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_buffering off;
        proxy_read_timeout 180s;
        proxy_send_timeout 180s;
    }

    # SSH/websocat binary bridge. Keep exact path if it is enabled.
    location = /ws/tcp {
        limit_conn tunnelmesh_ws 100;
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

    location = /health/live {
        proxy_pass http://tunnelmesh_server;
        proxy_set_header Host $host;
    }

    location = /health/ready {
        proxy_pass http://tunnelmesh_server;
        proxy_set_header Host $host;
    }

    # Prefer binding /metrics to an internal management address. If it must
    # pass through Nginx, protect it with network ACL or mTLS/basic auth.
    location = /metrics {
        allow 10.0.0.0/8;
        allow 192.168.0.0/16;
        deny all;
        proxy_pass http://tunnelmesh_server;
        proxy_set_header Host $host;
    }

    # SPA fallback comes last and must not swallow /api/ or /ws/*.
    location / {
        proxy_pass http://tunnelmesh_server;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
    }
}
```

## 关键约束

- `proxy_http_version 1.1`、`Upgrade`、`Connection`、`proxy_buffering off` 是 WebSocket 必需项。
- 30 秒心跳下，`proxy_read_timeout` 建议至少 180 秒，避免短暂网络抖动被 Nginx 提前断开。
- 必须透传 `Authorization` 和 `Origin`；不要把 token 放到 URL query 或 Cookie。
- `/api/`、`/ws/agent`、`/ws/client`、`/ws/tcp` 使用精确或 `^~` location，优先于 SPA fallback。
- `/metrics` 不建议暴露公网；优先让 Prometheus 访问 Server 内网管理地址。
- 显式泛域名只允许单层 `tm-<name>.tunnel.example.com`；动态域名使用 `<agent>-<a>-<b>-<c>-<d>-<port>.<server.dynamic_suffix>`。Nginx 正则中的后缀必须与 `server.dynamic_suffix`、wildcard DNS 和证书一致；正则只做域名形状筛选，IP 八位组范围、端口范围、危险地址和 Agent 策略由 Server 再次校验。
- 泛域名只解决 HTTP/HTTPS/WSS 路由，不提供公网 UDP 监听。
- 修改配置后先执行 `nginx -t`，再 reload；证书轮换必须验证 WSS 和 API 登录。

## 验证命令

```bash
nginx -t
curl -fsS https://tunnel.example.com/health/live
curl -fsS https://tunnel.example.com/health/ready
curl -fsS https://tunnel.example.com/metrics
websocat --binary -H='Authorization: Bearer <client-service-token>' \
  wss://tunnel.example.com/ws/client
```

不要把真实 token、证书私钥或生产域名凭据写入配置仓库。

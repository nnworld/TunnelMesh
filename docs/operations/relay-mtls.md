# Relay mTLS 证书生成与配置

本文说明如何为 Server 节点间 relay 生成 CA、节点证书和私钥，并配置到 `server.yaml`。生产环境建议使用 mTLS；`ca/cert/key` 全部省略时为明文模式，只适合受控内网。

## 安全模型

每个 Server 节点使用：

- 同一个 relay CA；
- 自己独立的节点证书和私钥；
- 同一个 server-node service token（fleet token 或显式允许列表）；
- 唯一且稳定的 `node.id`。

mTLS 模式下，relay 会同时校验：

1. 证书链由 `server.relay.ca` 信任；
2. 对端证书 SAN 精确包含调用方声称的 `node.id`；
3. 对端证书包含所有节点共同的 `server.relay.server_name`，用于 TLS 主机名校验；
4. server-node token 类型、scope、节点启用状态、逻辑删除状态和 epoch。

明文模式省略传输层证书校验，但仍执行第 4 项。它不提供传输加密，也无法用证书证明节点身份；只能在网络 ACL、主机安全和审计边界都已受控的环境使用。

## 使用脚本签发

下面的第 1–8 节是逐步的手工流程，用于理解每个字段为什么必须这样填，以及在签发机上接入自己的
Secret Manager。日常签发直接用脚本，它执行的就是同一套 openssl 命令和同一组扩展要求：

```bash
# 节点 ID 必须等于 node.id / TUNNELMESH_NODE_ID，会原样写入证书 SAN。
# node.id 是自动生成的时候，先执行 init-node-id 再签发。
./scripts/gen-relay-certs.sh --server-name relay.internal.example.com \
  server-1 server-2
```

输出布局（`deploy/certs/` 已 gitignore）：

| 路径 | 内容 | 权限 | 分发范围 |
| --- | --- | --- | --- |
| `relay/relay-ca.pem` | CA 证书 | 0644 | 所有 Server 节点 |
| `relay/<node-id>/relay.pem` | 节点证书 | 0644 | 仅该节点 |
| `relay/<node-id>/relay-key.pem` | 节点私钥 | 0600 | 仅该节点 |
| `private/relay-ca-key.pem` | CA 私钥 | 0600，目录 0700 | 只留在签发机 |

CA 私钥刻意放在 `relay/` 之外，这样只挂载自己节点目录的容器读不到它。脚本行为：

- 已存在的 CA 一律复用，新增节点直接重跑并追加节点 ID 即可；
- 节点证书已存在时跳过，`--force` 只重签命令行上列出的节点，绝不替换 CA；
- 发现 CA 证书与私钥只剩其一时直接失败，避免用错 CA 签发；
- 每张证书签发后立即 `openssl verify`，并校验 SAN 同时包含节点 ID 和共同 `server_name`，
  不满足就失败退出，而不是等到 Server 启动才报错；
- 节点 ID 必须是小写 DNS label，否则不能作为 `subjectAltName=DNS:` 值，脚本会拒绝。

轮换 CA 属于第 10 节的手工流程，脚本不提供该能力：删除 CA 会让所有已签发节点证书失效。

脚本只在签发机本地生成材料，不负责分发。把证书和私钥安装到目标节点时仍按第 6 节设置属主与
权限：CA 与节点证书 `0644` 可读，节点私钥 `root:tunnelmesh` 且 `0640`，因为 systemd 单元以
`User=tunnelmesh`/`Group=tunnelmesh` 运行并在 `ProtectSystem=strict` 下只读取这些路径。

## 1. 创建证书目录

以下命令在运维机上执行，实际路径可按部署调整：

```bash
sudo install -d -m 0755 /etc/tunnelmesh/certs
```

后续示例统一使用：

```text
/etc/tunnelmesh/certs/relay-ca.pem
/etc/tunnelmesh/certs/server-1-relay.pem
/etc/tunnelmesh/certs/server-1-relay-key.pem
```

## 2. 生成 relay CA

生成 CA 私钥：

```bash
openssl genpkey \
  -algorithm EC \
  -pkeyopt ec_paramgen_curve:P-256 \
  -out relay-ca-key.pem
```

生成自签 CA 证书：

```bash
openssl req \
  -x509 \
  -new \
  -sha256 \
  -days 3650 \
  -key relay-ca-key.pem \
  -subj "/C=CN/O=TunnelMesh/CN=TunnelMesh Relay CA" \
  -out relay-ca.pem \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,keyCertSign,cRLSign"
```

要求：

- `basicConstraints` 必须是 `critical,CA:TRUE`；
- `keyUsage` 必须包含 `keyCertSign` 和 `cRLSign`；
- CA 私钥只保存在签发机或 Secret Manager，不复制到 Server 节点。

## 3. 初始化并读取 `node.id`

每个节点先初始化自己的稳定身份：

```bash
sudo /usr/local/bin/tunnelmesh-server \
  --config /etc/tunnelmesh/server.yaml \
  init-node-id
```

读取最终 `node.id`：

```bash
sudo grep -A3 '^node:' /etc/tunnelmesh/server.yaml
```

示例输出：

```yaml
node:
  id: server-3f8a2e1b7d64c5a90b1e2f3c4d5e6f70
```

记录该值。节点证书 SAN 必须精确包含它，不能用通配符代替。

## 4. 生成节点私钥和 CSR

将 `<NODE_ID>` 替换为上一步读取的完整节点 ID：

```bash
NODE_ID='server-3f8a2e1b7d64c5a90b1e2f3c4d5e6f70'
NODE_NAME='server-1'

openssl genpkey \
  -algorithm EC \
  -pkeyopt ec_paramgen_curve:P-256 \
  -out "${NODE_NAME}-relay-key.pem"

openssl req \
  -new \
  -sha256 \
  -key "${NODE_NAME}-relay-key.pem" \
  -subj "/CN=${NODE_NAME}-relay" \
  -out "${NODE_NAME}-relay.csr"
```

## 5. 签发节点证书

创建扩展文件。`RELAY_SERVER_NAME` 是所有节点共同的 TLS ServerName，示例使用 `relay.internal.example.com`：

```bash
RELAY_SERVER_NAME='relay.internal.example.com'

cat > "${NODE_NAME}-relay.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth,clientAuth
subjectAltName=DNS:${NODE_ID},DNS:${RELAY_SERVER_NAME}
EOF
```

签发证书：

```bash
openssl x509 \
  -req \
  -in "${NODE_NAME}-relay.csr" \
  -CA relay-ca.pem \
  -CAkey relay-ca-key.pem \
  -CAcreateserial \
  -days 365 \
  -sha256 \
  -extfile "${NODE_NAME}-relay.ext" \
  -out "${NODE_NAME}-relay.pem"
```

要求：

- `subjectAltName` 必须包含最终 `node.id`；
- `subjectAltName` 必须包含所有节点共同的 `RELAY_SERVER_NAME`；
- `extendedKeyUsage` 必须同时包含 `serverAuth` 和 `clientAuth`，因为 relay 证书既用于服务端验证，也用于客户端 mTLS；
- 每个节点必须重新生成私钥和 CSR，不共享私钥或证书。

## 6. 安装证书和私钥

将 CA 和节点证书复制到目标 Server：

```bash
sudo install \
  -o root -g root -m 0644 \
  relay-ca.pem /etc/tunnelmesh/certs/relay-ca.pem

sudo install \
  -o root -g root -m 0644 \
  "${NODE_NAME}-relay.pem" \
  "/etc/tunnelmesh/certs/${NODE_NAME}-relay.pem"

sudo install \
  -o root -g tunnelmesh -m 0640 \
  "${NODE_NAME}-relay-key.pem" \
  "/etc/tunnelmesh/certs/${NODE_NAME}-relay-key.pem"
```

权限要求：

- CA 和证书可被所有用户读取；
- 私钥仅 root 和 `tunnelmesh` 组可读；
- 不把 CA 私钥、节点私钥或证书提交到 Git；
- 不把证书私钥写入环境变量。

## 7. 配置 `server.yaml`

mTLS 模式必须同时填写 `ca/cert/key/server_name`：

```yaml
server:
  relay:
    enabled: true
    listen: 0.0.0.0:9443
    # 可省略；省略时自动使用本机可用 IP 和 listen 端口。
    endpoint: server-1.internal.example.com:9443
    ca: /etc/tunnelmesh/certs/relay-ca.pem
    cert: /etc/tunnelmesh/certs/server-1-relay.pem
    key: /etc/tunnelmesh/certs/server-1-relay-key.pem
    server_name: relay.internal.example.com
    # 生产环境建议通过 TUNNELMESH_SERVER_RELAY_NODE_TOKEN 注入。
    node_token: ""
```

字段说明：

- `listen`：本进程 relay gRPC 监听地址；
- `endpoint`：注册到数据库、供其他 Server 访问的广播地址；
- `ca`：验证对端节点证书的 CA 证书；
- `cert`：本节点 relay 证书；
- `key`：本节点 relay 私钥；
- `server_name`：出站 relay 连接校验对端证书时使用的 TLS ServerName；
- `node_token`：server-node service token。

`endpoint` 为空时的推导规则：

1. 读取 `listen` 的端口；
2. 如果 `listen` 是具体 IP，直接使用该 IP；
3. 如果 `listen` 是 `0.0.0.0`、`[::]` 或空 host，选择本机第一个可用的非 loopback、非 link-local 地址，优先 IPv4；
4. 推导结果只写入进程内有效配置，不回写 YAML。

多网卡、容器 NAT 或跨网段部署时，自动推导可能选择错误地址；应显式配置 `endpoint`。

## 8. 校验证书

验证证书链：

```bash
openssl verify \
  -CAfile /etc/tunnelmesh/certs/relay-ca.pem \
  /etc/tunnelmesh/certs/server-1-relay.pem
```

检查 SAN 和 EKU：

```bash
openssl x509 \
  -in /etc/tunnelmesh/certs/server-1-relay.pem \
  -noout \
  -text \
  | grep -A2 'Subject Alternative Name'

openssl x509 \
  -in /etc/tunnelmesh/certs/server-1-relay.pem \
  -noout \
  -text \
  | grep -A1 'Extended Key Usage'
```

预期：

- `Subject Alternative Name` 同时包含本节点 `node.id` 和 `server_name`；
- `Extended Key Usage` 包含 `TLS Web Server Authentication` 和 `TLS Web Client Authentication`。

校验配置：

```bash
sudo -u tunnelmesh \
  /usr/local/bin/tunnelmesh-server \
  --config /etc/tunnelmesh/server.yaml \
  check-config
```

重启并观察：

```bash
sudo systemctl restart tunnelmesh-server
sudo journalctl -u tunnelmesh-server -n 100 --no-pager
```

## 9. 明文模式

如果只在受控内网运行，可以省略 `ca/cert/key/server_name`：

```yaml
server:
  relay:
    enabled: true
    listen: 0.0.0.0:9443
    endpoint: ""
    node_token: ""
```

明文模式仍会校验：

- server-node token；
- token scope；
- 节点启用和未逻辑删除状态；
- epoch。

明文模式不会提供：

- 传输加密；
- 防窃听；
- 防中间人篡改；
- 证书级节点身份证明。

不要在跨机房、公网、不可信容器网络或未隔离网段使用明文模式。

## 10. 轮换与回滚

### 轮换节点证书

1. 在签发机保留旧 CA 和旧证书；
2. 生成新节点私钥、CSR 和证书；
3. 先替换一个非核心 Server 的 `cert/key`；
4. 执行 `check-config` 并重启；
5. 验证该节点与其他节点的 relay 调用；
6. 逐台滚动替换其他节点；
7. 所有节点完成后再清理旧证书文件。

### 轮换 CA

1. 生成新 CA；
2. 用新 CA 重新签发所有节点证书；
3. 先将新 CA 和新节点证书部署到一台 Server；
4. 确认该节点与其他旧 CA 节点的互通要求，必要时维护双 CA 过渡窗口；
5. 逐台切换所有节点；
6. 全部完成后移除旧 CA。

### 回滚

1. 停止当前 Server；
2. 恢复旧 `relay-ca.pem`、节点证书和私钥；
3. 确认文件路径、owner 和权限不变；
4. 执行 `check-config`；
5. 重启 Server；
6. 验证 `/servers` 页面节点状态和跨节点连接查询。

证书轮换不改变 `node.id`、数据库 epoch 或 server-node token。如果同时轮换 token，先完成证书轮换并验证，再单独轮换 token，避免两个变量同时失败。

# 单点登录与两步验证

本文覆盖管理后台的企业身份能力：OIDC 单点登录（SSO）、TOTP 两步验证（MFA）与一次性恢复码、
可撤销的受信任设备，以及全局认证策略。面向两类读者——配置身份提供商与策略的管理员，和使用
两步验证、管理自己设备的普通用户。

配置键的含义、默认值和取值范围见[配置说明](../operations/configuration.md#管理台身份认证sso--mfa--受信任设备)；
数据库升级与回滚见 [Schema 升级与回滚](../operations/schema-upgrades.md#v13-to-v14)；
指标与告警见[可观测性](../operations/observability.md#身份认证sso--mfa--受信任设备)。

## 前置条件

启用 SSO 或 MFA 之前必须确认三件事，否则相关操作会 fail-closed 而不是静默降级：

1. **注入 `TUNNELMESH_TOKEN_ENCRYPTION_KEY`。** OIDC `client_secret`、TOTP 共享密钥，以及每一次
   登录产生的 `auth_challenges` payload（OIDC state、nonce、PKCE verifier 和回调后的 login ticket）
   都用这把 AES-GCM 密钥加密后落盘。未配置时 Server 仍能启动、密码登录和已绑定账号的 MFA 校验
   也照常工作，但 MFA 绑定、带 client secret 的 OIDC 提供商创建，以及**所有 OIDC 登录**（authorize
   需要创建加密的 state challenge）都会返回 `503`，`data.error=secret_storage_unavailable`。
   集群中所有 Server 节点必须配置完全一致的密钥和 key id，否则一个节点签发的 challenge 或 ticket
   在另一个节点上无法解密。
2. **配置 `security.allowed_origins` 或 `security.allowed_hosts`。** OIDC 回调地址的白名单由这两项
   推导：`allowed_origins` 直接使用，`allowed_hosts` 展开成 `https://<host>`。两者都为空时，
   任何 `redirectUri` 都会被拒绝为“不在允许的 base 之内”，无法注册提供商。
3. **数据库已升级到 Schema v14。** `auth_settings`、`oidc_providers`、`user_identities`、`user_mfa`、
   `user_recovery_codes`、`user_devices`、`auth_challenges`、`auth_login_attempts` 八张表必须存在。
   版本不是 14 或缺表时 Server 在启动阶段直接失败（`schema version mismatch` /
   `schema is missing required table <name>`），不会带着半个身份层对外服务。
   `503 identity_services_unavailable` 只出现在把 API 当作库嵌入、却没有装配身份容器的场景。

## 权威来源：数据库而不是配置文件

`security.auth` 里的策略项只是**首次启动时的种子**。`auth_settings` 行创建之后，数据库就是唯一
权威来源：

- `session_token_ttl`、`device_trust.enabled`、`device_trust.bypass_mfa` 只在种子时写入一次；
  `mfa_mode` 固定以 `disabled` 起播，保证升级后的部署行为与升级前完全一致，直到管理员显式打开。
- 管理员改过策略之后，再修改配置文件或环境变量都不会生效，重新部署也不会把数据库里的决定覆盖回去。
- 设备有效期和单账号设备上限**没有配置键**，只能改 `auth_settings`。
- OIDC 提供商的 issuer、client id、client secret、role mapping 全部存在 `oidc_providers` 表，
  配置文件里没有任何 per-provider 键。
- TOTP 的 `issuer`、`digits`、`period` 在绑定那一刻就固化进 otpauth URL；改配置只影响之后的新绑定。

这条规则带来一个运维上的好处：**关闭功能不需要发布**。把 `auth_settings.mfa_mode` 改成 `disabled`
并停用所有 OIDC 提供商，就能在不重启、不改 Schema、不回滚二进制的前提下全量下线两步验证与 SSO，
步骤见 [Schema 升级与回滚](../operations/schema-upgrades.md#v13-to-v14) 的“Feature-level rollback
without a deploy”。

## 管理员：配置 OIDC 单点登录

后台入口是左侧菜单的**单点登录**（路由 `/sso-providers`，仅管理员可见）。同一组操作也可以用
`/api/v1/sso/providers` 完成；所有写操作都必须携带 `Idempotency-Key`。

### 第 1 步：在 IdP 侧注册应用并填写精确回调地址

回调地址格式是固定的，`<provider>` 就是提供商的 `name`：

```text
https://<对外域名>/api/v1/auth/oidc/<provider>/callback
```

例如 name 为 `okta`、对外域名是 `tunnel.example.com` 时，必须逐字符填写：

```text
https://tunnel.example.com/api/v1/auth/oidc/okta/callback
```

这个地址必须是 `https`，必须落在 `security.allowed_origins` / `allowed_hosts` 推导出的 base 之内，
并且要与 IdP 应用里登记的 redirect URI **完全一致**——多一个斜杠、大小写不同或写成内网地址都会在
IdP 侧或本平台校验时被拒绝。经过 Nginx 反代时填写的是公网域名，不是 `127.0.0.1:8080`。

`name` 的规则是 `^[a-z0-9][a-z0-9-]{1,62}$`（小写字母数字开头，只允许小写字母、数字和连字符，
2–63 字符）。它是回调路径的一部分，**创建后不可修改**；改名等于新建一个提供商。

登录页渲染 SSO 按钮时走的是另一个只读端点：

```text
GET /api/v1/auth/oidc/providers        # 列出 publicListed 且 enabled 的提供商
GET /api/v1/auth/oidc/<provider>/authorize   # 302 跳转到 IdP
GET /api/v1/auth/oidc/<provider>/callback    # IdP 回调
POST /api/v1/auth/oidc/exchange        # 用一次性 ticket 换取会话
```

`security.auth.oidc.public_providers: false` 时第一个端点返回 `404`（不是 `403`），登录页不再列出
按钮，用户必须直接知道 `authorize` 地址才能发起 SSO。

### 第 2 步：创建提供商

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 是 | URL 安全 slug，规则见上；创建后不可改 |
| `displayName` | 否 | 登录页按钮上显示的名称 |
| `issuer` | 是 | 必须是绝对 `https` URL，且必须与 id_token 的 `iss` claim 一致（仅末尾斜杠会被归一化）。内网 IdP 的私有地址是允许的 |
| `clientId` | 是 | IdP 分配的 client id |
| `clientSecret` | 否 | 留空表示公共客户端，只用 PKCE。填写后以 AES-GCM 密文存储，接口只回 `hasSecret`，任何读取路径都不返回明文；PATCH 时留空表示“保持已存的 secret 不变” |
| `scopes` | 是 | 必须包含 `openid`；存储时去重并把 `openid` 排在首位 |
| `redirectUri` | 是 | 精确回调地址，规则见上 |
| `authorizationEndpoint`、`tokenEndpoint`、`userinfoEndpoint`、`jwksUri` | 否 | 端点覆盖。填写后优先于 discovery，用于 discovery 文档在本网络不可达的 IdP；每项必须是绝对 `http(s)` URL |
| `idTokenAlgs` | 否 | 允许的签名算法白名单，默认 `["RS256"]`。只接受 `RS256/384/512`、`PS256/384/512`、`ES256/384/512`、`EdDSA` |
| `usernameClaim` | 否 | 用户名来源 claim，默认 `preferred_username`，最长 64 字符 |
| `roleMappings` | 否 | 有序的 claim → role 规则，见下节 |
| `defaultRole` | 否 | `admin` 或 `user`，默认 `user`；所有映射都不匹配时使用 |
| `authoritativeRoles` | 否 | 默认 `true`：每次登录都按 IdP 的 claim 改写本平台角色 |
| `autoCreateUsers` | 否 | 默认 `true`：首次登录的 subject 自动建号（JIT 置备） |
| `fetchUserinfo` | 否 | 默认 `false`：是否在 token 交换后额外调用 userinfo 端点补齐 claim |
| `publicListed` | 否 | 默认 `true`：是否出现在登录页的 SSO 按钮列表 |
| `enabled` | 否 | 默认 `true`。关闭后 authorize 与 callback 返回 `404 oidc_provider_not_found` |

`id_token` 的签名算法白名单里**不可能出现 `none` 或任何 `HS*`**：这两类在保存提供商时就被拒绝，
在校验 token 时再拒绝一次，没有任何配置项能打开它们。原因是 HMAC 的密钥就是 client secret，
任何能读到 `oidc_providers` 行的人也持有该密钥，接受 `HS*` 等于把“可读的配置”变成“可伪造 token 的能力”。

创建后建议立刻执行连通性测试：

```bash
curl -sS -X POST "https://tunnel.example.com/api/v1/sso/providers/${PROVIDER_ID}/test" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H 'Idempotency-Key: sso-provider-okta-test-001'
```

该接口对真实 IdP 发起一次 discovery 与 JWKS 拉取并返回结果，不改变任何配置。它按“变更”处理
（要求 `Idempotency-Key`、写审计），因为它产生了一次必须可追责的出站请求。测试会先失效该 issuer
的 discovery 缓存，所以重试看到的是新结果。

### 第 3 步：配置角色与组映射

`roleMappings` 是**有序**列表，第一条命中的规则生效；`role` 只能是 `admin` 或 `user`。

```json
{
  "roleMappings": [
    {"claim": "groups", "value": "tunnelmesh-admins", "role": "admin"},
    {"claim": "groups", "value": "tunnelmesh-users",  "role": "user"}
  ],
  "defaultRole": "user"
}
```

映射语义：

- claim 值可以是字符串，也可以是字符串数组（`groups` 这类多值 claim 按成员匹配）。
- claim 在 id_token 里不存在时**跳过这条规则**，不算错误——不同 IdP 释放的组 claim 名称不同，
  缺失只意味着“不匹配”。
- 规则里的 `role` 不是 `admin`/`user` 时直接报 `oidc_role_mapping_invalid`，即使后面的规则本来能命中。
  这是刻意的：静默忽略会让管理员以为某条规则在生效。
- 所有规则都不匹配时使用 `defaultRole`。
- 需要释放 `groups` 时记得把它加进 `scopes`，或在 IdP 侧配置进 id_token，否则 claim 不会出现在 token 里。
- `fetchUserinfo: true` 时 userinfo 端点返回的 claim 也参与映射，用于只在 userinfo 里释放组的 IdP。

`authoritativeRoles: true`（默认）时每次登录都会把账号角色改写成映射结果，IdP 是唯一权威；
设为 `false` 则只在首次置备时赋角色，之后由本平台管理员维护。有一条硬性保护：当改写会把
**最后一个在职管理员**降级时，登录以 `last_admin_protected` 失败，不会把整个部署锁死。

### 第 4 步：理解用户置备

`autoCreateUsers: true` 时，首次登录的 `(provider, sub)` 组合会自动创建账号：`auth_source='oidc'`、
无本地密码哈希，只能通过该 IdP 登录。账号名按以下顺序解析：`usernameClaim` 指定的 claim →
`email` claim → 由 provider 与 `sub` 派生的兜底名，并统一消毒到平台用户名规则
（`[A-Za-z0-9._-]`，3–64 字符）；邮箱里的 `@` 会被替换成 `_`，映射是确定性的，同一地址永远解析到
同一账号。解析不出合法用户名时登录以 `oidc_claim_missing` 失败。

一个已有本地密码的账号用同一用户名完成 SSO 登录后，会变成 `auth_source='mixed'`，密码登录与
SSO 登录都可用；解除全部外部关联后自动退回 `local`。

`autoCreateUsers: false` 时，未预先创建的 subject 登录会得到 `403 oidc_user_not_provisioned`。
这适合“账号必须由管理员显式开通”的场景。

### 提供商管理接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/sso/providers` | cursor 分页列出提供商；只返回 `hasSecret`，不返回 secret |
| `POST` | `/api/v1/sso/providers` | 创建；需要 `Idempotency-Key` |
| `GET` | `/api/v1/sso/providers/{id}` | 详情 |
| `PATCH` / `PUT` | `/api/v1/sso/providers/{id}` | 更新；需要 `Idempotency-Key`。布尔项是指针语义，省略的字段保持原值，不会被静默改成 `false` |
| `DELETE` | `/api/v1/sso/providers/{id}` | 删除；需要 `Idempotency-Key`。仍有账号关联该提供商时返回 `409 oidc_provider_in_use` |
| `POST` | `/api/v1/sso/providers/{id}/test` | discovery + JWKS 连通性测试；需要 `Idempotency-Key` |

## 管理员：认证策略

后台入口在**单点登录**页的策略区，接口是 `GET /api/v1/auth/policy` 与 `PUT /api/v1/auth/policy`
（仅管理员，PUT 需要 `Idempotency-Key`）。策略写入 `auth_settings` 单行，并记录哪些字段发生了变化。

| 字段 | 取值范围 | 默认（种子） | 说明 |
| --- | --- | --- | --- |
| `mfaMode` | `disabled` / `optional` / `required` | `disabled` | 全局两步验证策略，见下 |
| `deviceTrustEnabled` | bool | `true` | 是否允许签发受信任设备 |
| `deviceTrustTtlSeconds` | 3600–7776000（1 小时–90 天） | `2592000`（30 天） | 受信任设备有效期 |
| `allowTrustedDeviceBypass` | bool | `true` | 未过期的受信任设备是否跳过二次验证 |
| `maxTrustedDevices` | 1–100 | `10` | 单账号最多同时保有多少台受信任设备 |
| `sessionTokenTtlSeconds` | ≥ 0 | `0` | 管理台 token 有效期；`0` 表示不过期（升级前的历史行为） |

`mfaMode` 的三种语义：

- `disabled`：全局不要求二次验证。**但**单个账号的 `users.mfa_required=1` 仍然生效——per-account
  要求是下限而不是建议。
- `optional`：已绑定的账号必须过二次验证，未绑定的账号可以只凭密码登录。这是推荐的过渡档位。
- `required`：所有账号都必须过二次验证；未绑定的账号登录后会被引导去绑定，绑定完成前拿不到会话。

一条校验规则值得注意：`allowTrustedDeviceBypass: true` 要求 `deviceTrustEnabled: true`，否则
`PUT` 返回 `400 auth_policy_invalid`。一个关闭了设备信任却仍存着 bypass 的策略是无效的，
但它会在管理员重新打开设备信任的那一刻静默授予绕过，因此在写入时就被拒绝。

### 强制单个账号启用 MFA

给某个账号单独上锁（例如管理员账号、离职风险账号），用子账号的状态接口：

```bash
curl -sS -X PATCH "https://tunnel.example.com/api/v1/users/${USER_ID}" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"mfaRequired": true}'
```

该操作写审计（`account.mfa_required` / `account.mfa_optional`），且不能作用于 `admin` 角色的账号
（返回 `403 admin_account_protected`）——管理员账号请用全局 `mfaMode: required` 覆盖。
`disabled` 与 `mfaRequired` 至少提供一个，否则返回 `400`。

被强制的账号如果还没绑定验证器，登录后会拿到 `mfaRequired: true` 的响应和 `methods: ["totp"]`
（不含 `recovery`，因为它还没有恢复码），控制台据此把它引导到绑定流程而不是死路。

### 管理员侧的身份管理接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/users/{id}/mfa` | 查看该账号的 MFA 状态与剩余恢复码数量 |
| `POST` | `/api/v1/users/{id}/mfa/reset` | **清除**该账号的第二因子和全部受信任设备；需要 `Idempotency-Key`。这是验证器丢失后的标准恢复动作 |
| `GET` | `/api/v1/users/{id}/devices` | 列出该账号的受信任设备（不标记“当前设备”） |
| `DELETE` | `/api/v1/users/{id}/devices/{deviceId}` | 撤销一台设备；需要 `Idempotency-Key`。笔记本丢失时的应急动作 |
| `GET` | `/api/v1/users/{id}/identities` | 列出该账号的外部身份关联 |
| `DELETE` | `/api/v1/users/{id}/identities/{identityId}` | 解除一条关联；需要 `Idempotency-Key`。无密码账号的最后一条关联不能解除（`409 identity_required_for_login`），否则会把同事锁在门外 |

用户 ID 永远来自路径而不是请求体，管理员不会被诱导去操作 URL 里没有点名的账号。

## 用户：绑定两步验证

后台入口是右上角头像菜单的**安全设置**（路由 `/account/security`）。

1. 点击“启用两步验证”。本地密码账号必须先输入**当前密码**；只通过 SSO 登录的账号（无本地密码）
   可以留空。这一步是防止被劫持的会话把攻击者的验证器绑成持久后门。
2. 页面返回一次性绑定信息：`secret`（Base32 密钥）、`otpauthUrl`、`recoveryCodes` 和 `expiresAt`。
   用验证器 App 扫描 `otpauthUrl` 对应的二维码，或手动输入 `secret`。链接形如：

   ```text
   otpauth://totp/<issuer>:<username>?algorithm=SHA1&digits=6&issuer=<issuer>&period=30&secret=<BASE32>
   ```

   标签里的 `issuer` 与用户名都经过百分号编码，所以 `security.auth.mfa.issuer` 里不允许出现冒号；
   `secret` 统一大写，`algorithm` 固定为 `SHA1`，`digits` 与 `period` 来自 `security.auth.mfa`。
3. 绑定信息有效期 **15 分钟**（`pending` 状态的过期时间由服务端固定，不在配置里）。超时后重新点击
   “重新生成绑定信息”即可。
4. 输入验证器上的 6 位动态码点击“激活两步验证”。激活成功后状态从 `pending` 变为 `enabled`。
5. **立刻离线保存恢复码。** 恢复码以 `tmrc-` 开头，共 20 位 Base32 字符，数量由
   `security.auth.mfa.recovery_codes` 决定（默认 10 个）。它们只在绑定和重新生成时返回一次，
   响应带 `Cache-Control: no-store`，数据库里只保存 SHA-256 摘要——**任何人都无法再次读出明文**，
   包括管理员。请存进密码管理器或打印后放入保险柜，不要截图留在相册、不要贴进聊天工具或工单。

每个恢复码**只能使用一次**，用过即作废。用恢复码登录后响应会带 `remainingRecoveryCodes`；
用完最后一个时还会带 `recoveryCodesExhausted: true`——这是唯一会提示“绕过手段已耗尽”的时机，
请立刻重新生成。

重新生成恢复码需要输入一个当前有效的动态码（确认是本人操作），生成后**旧恢复码立即全部失效**。

关闭两步验证同样需要输入一个当前有效的动态码或恢复码，并且会**同时撤销该账号的全部受信任设备**
——设备是在“第二因子存在”的前提下被信任的，因子消失后这份信任也必须消失。当策略是
`mfaMode: required` 或该账号被管理员单独强制时，关闭会失败并返回 `409 mfa_required_by_policy`。

## 用户：受信任设备

登录时勾选“信任此设备”，服务端会签发一个长期凭据并写入 `HttpOnly` cookie
（默认名 `tm_device`，默认 `Secure`、`SameSite=Lax`）。在有效期内，该浏览器登录时可以跳过二次验证
——前提是策略里 `allowTrustedDeviceBypass` 仍为开启。

安全设置页的“受信任设备”区可以看到设备名称、登录 IP、浏览器 User-Agent、信任时间、最近使用时间、
到期时间，以及哪一台是“当前设备”。可以重命名和撤销。

关于这份凭据需要知道的事实：

- 数据库只保存 token 的 SHA-256 摘要，明文只通过 cookie 下发一次，任何接口都不回显。
- cookie 是 `HttpOnly` 的，控制台里的脚本注入读不到它。
- cookie 的 `Max-Age` 取“配置 TTL”与“数据库里该设备行的剩余有效期”中的较小值，所以 cookie
  永远不会比授权它的那一行活得更久。
- 撤销是即时的：撤销当前正在使用的设备会同时清掉浏览器里的 cookie。
- 达到 `maxTrustedDevices` 上限时，最久未使用的一台活跃设备会被自动撤销后再签发新设备（按
  `last_seen_at` 排序，从未再次出现过的设备回退到 `trusted_at`），登录不会失败。
- 管理员在策略里关闭 `deviceTrustEnabled` 会**立刻**让所有绕过失效，不需要等 cookie 过期，
  也不需要改 token 或会话——因为每次登录都会重新读一遍数据库里的策略。
- 有效期到期后，行由后台清理器在保留期结束后删除，过期设备不会被列出，也不会被接受。

对应的自助接口：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `GET` | `/api/v1/auth/devices` | 列出自己的受信任设备，标记 `current` |
| `PATCH` | `/api/v1/auth/devices/{id}` | 重命名 |
| `DELETE` | `/api/v1/auth/devices/{id}` | 撤销 |
| `GET` | `/api/v1/auth/identities` | 列出自己关联的外部身份 |
| `DELETE` | `/api/v1/auth/identities/{id}` | 解除一条关联；无密码账号的最后一条不能解除 |

## 用户：关联与解除单点登录

安全设置页的“已关联的单点登录账号”区展示 provider、subject、账号、关联时间和最近登录时间。

- 已有本地密码的账号，用 SSO 登录一次即自动建立关联，`auth_source` 变为 `mixed`，此后密码和
  SSO 两条路都能登录。
- 只通过 SSO 创建的账号（`auth_source='oidc'`，无本地密码）**不能解除最后一条关联**，否则账号将
  没有任何可用凭据；尝试解除返回 `409 identity_required_for_login`。

## 登录流程

### 密码 + 两步验证

下图为简洁只画 `data` 的内容，外层信封见本节末尾。

```text
POST /api/v1/auth/login   {"username","password","trustDevice"}
  ├─ 200 data={"token","user","deviceTrusted"}                          # 策略不要求二次验证
  ├─ 200 data={"mfaRequired":true,"challengeId","methods","expiresAt"}   # 需要二次验证
  ├─ 401 data.error="invalid_credentials"                              # 凭据错误
  └─ 429 data.error="login_throttled"，响应头带 Retry-After 秒数        # 该 username+IP 桶已被封禁

POST /api/v1/auth/mfa/verify   {"challengeId","code","trustDevice"}
  └─ 200 data={"token","user","deviceTrusted"[,"recoveryCodesExhausted":true]}
```

`code` 字段同时接受 6 位动态码和 `tmrc-` 开头的恢复码，服务端按前缀区分。不触发 MFA 时登录响应的
`data` 仍然包含升级前的 `token` 与 `user` 两个字段，只额外增加 `deviceTrusted`（以及恢复码用尽时的
`recoveryCodesExhausted`），因此忽略未知字段的已有脚本和客户端无需改动。

有一处**行为变化**需要注意：外层信封始终是 `{ "code": <HTTP 状态码>, "msg": <HTTP 状态文本>,
"data": ... }`，机器可读的错误码在 `data.error`。凭据失败时 `data.error` 从升级前的散文式
`invalid credentials` 改成了稳定错误码 `invalid_credentials`；按字符串精确匹配旧文案的客户端需要跟着
调整，改成匹配 `data.error`。

限流按 `SHA-256(lower(username) + "|" + clientIP)` 分桶，在 `security.auth.login_throttle.window`
内失败 `max_attempts` 次后封禁 `block` 时长。**触发封禁的那次请求本身仍返回普通的 `401
invalid_credentials`**，只有下一次请求才会得到 `429`，这样攻击者无法用响应差异探测封禁边界。
所有凭据失败都收敛成同一个 `invalid_credentials`，无法用来枚举账号是否存在。

### 单点登录

```text
浏览器 → GET /api/v1/auth/oidc/<provider>/authorize
          服务端生成 state / nonce / PKCE(S256) verifier，加密存进一条 auth_challenges 行，
          并用 challenge id 本身作为 state（它已经是 192 位随机值），302 跳转到 IdP
       ← IdP 认证 → GET /api/v1/auth/oidc/<provider>/callback?code&state
          交换 code → 校验 id_token → 置备/更新账号 → 消费 state
          ├─ 需要二次验证：302 → /login?mfa=<challengeId>
          └─ 不需要：302 → /login?ticket=<一次性 ticket>
浏览器 → POST /api/v1/auth/oidc/exchange {"ticket","trustDevice"} → 200 data={"token","user",...}
```

设计上有几个刻意的约束值得运维知道：

- **state、nonce、PKCE verifier 存在数据库而不是进程内存**，所以集群里任意节点都能完成回调，
  登录不必粘在同一台 Server 上。
- `state` 的 `max_attempts` 是 1：state 被回调消费一次，第二次呈现同一个 state 必然失败。
- 回调只在置备成功之后才消费 state，所以一次瞬时的数据库错误在 TTL 内仍可重试。
- **ticket 通过 POST 交换而不是 GET**，避免这个 bearer 凭据落进浏览器历史、Referer 头或代理
  access log。ticket 有效期上限被硬性限制在 300 秒（默认 60 秒）。
- 回调**只会**重定向到一个固定相对路径 `/login`，且值经过 URL 转义。提供商配置和 IdP 都无法
  影响浏览器最终去哪，因此不存在经由回调的开放重定向。
- IdP 侧返回 `error` 参数时，本平台只回稳定的 `oidc_provider_error`，**不回显 `error_description`**
  ——那是 IdP 可控文本，不能变成本源上的反射内容。
- PKCE 的 `code_challenge_method` 固定为 `S256`，`grant_type` 固定为 `authorization_code`，都不可配置。

一次成功的 IdP 断言只是**一个**因子：账号策略会在回调后再次生效，`mfaMode: required` 或
per-account 强制的账号即使 SSO 成功也仍要过二次验证。

## 端点速查

不需要会话：

| 方法 | 路径 |
| --- | --- |
| `POST` | `/api/v1/auth/login` |
| `POST` | `/api/v1/auth/mfa/verify` |
| `POST` | `/api/v1/auth/oidc/exchange` |
| `GET` | `/api/v1/auth/oidc/providers` |
| `GET` | `/api/v1/auth/oidc/{provider}/authorize` |
| `GET` | `/api/v1/auth/oidc/{provider}/callback` |

需要会话（本人）：

| 方法 | 路径 |
| --- | --- |
| `GET` | `/api/v1/auth/me` |
| `PUT` | `/api/v1/auth/password` |
| `GET` / `DELETE` | `/api/v1/auth/mfa` |
| `POST` | `/api/v1/auth/mfa/enroll`、`/api/v1/auth/mfa/enable`、`/api/v1/auth/mfa/recovery-codes` |
| `GET` | `/api/v1/auth/devices` |
| `PATCH` / `DELETE` | `/api/v1/auth/devices/{id}` |
| `GET` | `/api/v1/auth/identities` |
| `DELETE` | `/api/v1/auth/identities/{id}` |

需要 `admin` 角色：`/api/v1/auth/policy`、`/api/v1/sso/providers*`、`/api/v1/users/{id}/mfa*`、
`/api/v1/users/{id}/devices*`、`/api/v1/users/{id}/identities*`。

所有响应都是统一信封 `{ "code": <HTTP 状态码>, "msg": <HTTP 状态文本>, "data": ... }`；机器可读的
稳定错误码在 `data.error`，`msg` 只是 HTTP 状态文本，不要用它做分支判断。列表接口使用 cursor 分页。
受信任设备与外部身份的列表由策略天然限界（最多 `maxTrustedDevices` 台设备、每个提供商最多一条关联），
因此单页即完整，`nextCursor` 为空、`hasMore` 为 `false`。

## 故障排查

`data.error` 是稳定错误码，客户端应按它分支而不是解析文案。下表按状态码归类。

| HTTP | `data.error` | 含义与处理 |
| --- | --- | --- |
| `400` | `idempotency_key_required` | 管理类写操作缺少 `Idempotency-Key`，或该头超过 255 字符 |
| `400` | `auth_policy_invalid` | 策略字段越界：`deviceTrustTtlSeconds` 必须在 3600–7776000，`maxTrustedDevices` 必须在 1–100，`sessionTokenTtlSeconds` 不能为负，或 `allowTrustedDeviceBypass` 与 `deviceTrustEnabled=false` 同时出现 |
| `400` | `oidc_provider_invalid` | 提供商字段非法：`name` 不匹配 `^[a-z0-9][a-z0-9-]{1,62}$`、`issuer`/`redirectUri` 不是绝对 `https` URL、`redirectUri` 不在允许的 base 内、`scopes` 缺 `openid`、`defaultRole` 不是 `admin`/`user`，或尝试修改不可变的 `name` |
| `400` | `oidc_role_mapping_invalid` | `roleMappings[].role` 不是 `admin` 或 `user` |
| `400` | `current_password_required` | 本地密码账号绑定 MFA 时没有提供当前密码 |
| `400` | `oidc_state_invalid` | state 缺失、过期、已被消费，或与 provider 不匹配。让用户重新从登录页发起 SSO |
| `400` | `oidc_provider_error` | IdP 在回调里返回了 `error`（用户取消、应用未授权等）。查看 IdP 侧日志 |
| `401` | `invalid_credentials` | 用户名或密码错误、账号被禁用或已删除、账号没有本地密码。**所有凭据失败都收敛到这一个码**，不能用来判断账号是否存在 |
| `401` | `login_ticket_invalid` | 一次性 ticket 过期、已被交换，或对应账号已不可用。重新发起 SSO |
| `401` | `mfa_challenge_invalid` | challengeId 缺失、过期或已消费。回到登录页重新开始 |
| `401` | `mfa_attempts_exceeded` | 该 challenge 的尝试次数用尽（默认 5 次）。重新开始登录 |
| `401` | `mfa_code_invalid` | 动态码或恢复码不正确。检查验证器时钟；确认输入的是当前步的码 |
| `401` | `mfa_code_reused` | 同一个 TOTP 时间步被重复使用（重放保护，由 `user_mfa.last_used_step` 保证）。等下一步再试 |
| `401` | `mfa_not_enrolled` | 账号没有可用的 MFA 绑定。先去安全设置页绑定 |
| `401` | `oidc_id_token_invalid` / `oidc_unsupported_algorithm` / `oidc_no_matching_key` / `oidc_claim_missing` | id_token 校验失败：签名无效、`iss`/`aud`/`exp`/`nonce` 不匹配、算法不在白名单（或 IdP 用了 `none`/`HS*`）、JWKS 里没有匹配的 `kid`、缺少 `sub`，或解析不出合法用户名 |
| `403` | `forbidden` | 非管理员调用了管理员接口，或普通用户操作了不属于自己的资源 |
| `403` | `current_password_invalid` | 绑定 MFA 或改密码时提供的当前密码不正确 |
| `403` | `admin_account_protected` | 试图通过子账号接口修改 `admin` 角色账号（包括设置 `mfaRequired`） |
| `403` | `last_admin_protected` | 操作会导致最后一个在职管理员失去权限或被降级 |
| `403` | `oidc_user_not_provisioned` | 提供商的 `autoCreateUsers` 为 `false` 且该 subject 没有对应账号。请管理员先建号 |
| `404` | `oidc_provider_not_found` / `oidc_provider_disabled` | 提供商名不存在或已停用。注意 `public_providers: false` 时 `GET /api/v1/auth/oidc/providers` 也返回 `404` |
| `404` | `device_not_found` / `identity_not_found` / `account_not_found` | 目标不存在。刻意用 `404` 而不是 `403`，这样标识符不能被用来探测哪些账号有外部关联或受信任浏览器 |
| `409` | `mfa_required_by_policy` | 策略要求 MFA（全局 `required` 或 per-account 强制），无法关闭 |
| `409` | `mfa_not_confirmed` | 绑定还是 `pending` 状态，需要先用一个动态码激活 |
| `409` | `mfa_already_enabled` | 已经激活过，不能重复激活 |
| `409` | `device_trust_disabled` | 策略已关闭设备信任，不能签发受信任设备 |
| `409` | `identity_required_for_login` | 试图解除无密码账号的最后一条外部关联 |
| `409` | `oidc_provider_in_use` | 仍有账号关联该提供商，不能删除。先停用（`enabled=false`）或解除关联 |
| `409` | `conflict` | `(provider, subject)` 唯一约束冲突，通常是并发登录造成的重复置备。重试即可 |
| `429` | `login_throttled` | username+IP 桶被封禁，响应带 `Retry-After` 秒数 |
| `502` | `oidc_discovery_failed` / `oidc_jwks_fetch_failed` / `oidc_token_exchange_failed` | IdP 不可达或行为异常。检查出网、`oidc.http_timeout`、issuer 拼写，并用提供商 test 接口复现 |
| `503` | `secret_storage_unavailable` | `TUNNELMESH_TOKEN_ENCRYPTION_KEY` 未配置或不可用。注入密钥后重试；系统不会退化成明文存储 |
| `503` | `identity_services_unavailable` | 身份服务未装配或 Schema 未升级到 v14。检查 `schema_meta.version` |
| `500` | `internal_error` | 未归类错误。带上 trace id 查看 Server 日志 |

排查顺序建议：`/metrics` 的 `tunnelmesh_auth_oidc_step_total{result="error"}` 按 `step` 分组定位
坏在哪一段 → 提供商 `test` 接口复现 discovery/JWKS → 审计日志（`auth.login.*`、`auth.mfa.*`、
`auth.device.*`、`auth.oidc.*`、`auth.policy.update`）确认账号侧发生了什么。更多通用排障见
[故障排查](../operations/troubleshooting.md)。

## 锁死与恢复

按“谁被锁在外面”分三种情况：

**普通用户丢了验证器，但还有恢复码。** 在登录页的二次验证输入框里直接输入任意一个未使用的
`tmrc-` 恢复码即可登录，登录后到安全设置页重新生成恢复码并重新绑定验证器。

**普通用户丢了验证器，也没有恢复码。** 请管理员调用 `POST /api/v1/users/{id}/mfa/reset`
（需要 `Idempotency-Key`）。该操作会清除第二因子**并撤销该账号的全部受信任设备**——这是刻意的
破坏性动作：账号必须重新绑定，而重新绑定是确认操作者确实是账号本人的唯一方式。用户随后按
[用户：绑定两步验证](#用户绑定两步验证) 重新走一遍。

**所有管理员都被锁在外面**（管理员账号的验证器全部丢失，或 `mfaMode: required` 之后没人能登录）。
不要试图从 HTTP 接口恢复——平台不提供匿名恢复接口。走既有的管理员引导/恢复流程，在 Server 主机上
用同一份配置和数据库执行：

```bash
tunnelmesh-server --config /etc/tunnelmesh/server.yaml admin regenerate-credentials --confirm
```

命令先持有进程级恢复互斥锁，再在一个数据库事务里完成全部写入——两者合起来就是代码注释里说的
“跨进程锁路径”——随后撤销该管理员账号上所有未撤销的 token（管理台会话与 service token 都在同一张
表里，一并失效）、生成新的高强度凭据、写审计，并把用户名和密码**只输出到命令的 stdout**
（不进日志、不进 HTTP 响应）。用新凭据登录后，再对其他管理员执行 `mfa/reset`，或临时把
`auth_settings.mfa_mode` 改回 `disabled` 争取处置时间。凭据全部遗失但数据库里已存在管理员时不要用
`admin bootstrap`（它会拒绝执行）；反复执行 bootstrap 只会掩盖“连错数据库”这类真实问题。

完整的命令语义、输出处理和注意事项见[故障排查](../operations/troubleshooting.md)与
[Server 管理后台使用指南](server-admin.md#登录与首次凭据)，此处不重复。

## 审计动作

身份链路写入以下审计动作，可在**审计日志**页按动作过滤：

| 动作 | 触发时机 |
| --- | --- |
| `auth.login.success` | 登录成功；详情含登录方式、客户端 IP、是否使用受信任设备 |
| `auth.login.failure` | 密码错误、二次验证码错误或尝试次数用尽；详情含方式、IP 和原因 |
| `auth.login.throttled` | 命中限流封禁；资源是限流桶 |
| `auth.login.mfa_required` | 密码或 SSO 通过但还欠一个二次验证 |
| `auth.mfa.enroll` / `auth.mfa.enable` / `auth.mfa.disable` | 生成绑定信息 / 激活 / 关闭（详情含被撤销的设备数） |
| `auth.mfa.verify` | 一次第二因子校验通过 |
| `auth.mfa.recovery_regenerate` | 重新生成恢复码（详情只含数量） |
| `auth.mfa.reset` | 管理员清除某账号的第二因子 |
| `auth.device.trust` / `auth.device.revoke` / `auth.device.revoke_all` | 签发 / 撤销一台 / 撤销全部受信任设备 |
| `auth.identity.unlink` | 解除一条外部身份关联 |
| `auth.oidc.provision` | SSO 登录置备或更新了账号 |
| `auth.oidc.provider.create` / `.update` / `.delete` / `.test` | 提供商增删改与连通性测试 |
| `auth.policy.update` | 修改全局认证策略；详情含变化字段名与新值 |
| `account.mfa_required` / `account.mfa_optional` | 管理员对单个账号强制或取消强制 MFA |

审计行**不含**任何秘密：不写 challenge id、login ticket、device token、TOTP 密钥、恢复码明文、
client secret，也不写 IdP 返回的 `error_description`。提供商更新只记录哪些字段名发生了变化，
不记录 secret 值。

## 明确不支持

以下能力刻意不在本期范围内，请勿据此设计流程：ICMP、TUN/L2 VPN、P2P NAT 穿透、任意远程命令执行。
SSH 支持仅限既有的 stdio/WebSocket 代理链路，不会扩展成通用命令执行接口。WebAuthn/Passkey、
短信与邮件 OTP、SCIM 用户同步、SAML 也尚未实现；当前唯一的联合登录协议是 OIDC，唯一的第二因子
是 TOTP 加一次性恢复码。

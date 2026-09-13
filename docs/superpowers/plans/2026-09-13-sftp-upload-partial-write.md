# WebSFTP 上传失败修复实施计划

状态：补记计划（原始实现已完成）

补记说明：本计划对应一次线上缺陷止损。用户报告 WebSFTP 页面上传文件失败，页面只给出通用文案“文件传输失败”。按 AGENTS.md 的紧急修复条款，先定位与修复，再补记本文。文中的红灯证据来自实现过程中的真实观察，未重新复现；绿灯与门禁结果在补记时重新执行，命令与结果见第 7 节。

## 1. 目标

1. WebSFTP 上传在单文件限制（1 GiB）内必须逐字节完整落盘，不因文件大小或链路速度退化。
2. 上传失败要有可区分的错误语义，不能把通道死亡与写入停滞压成同一句“文件传输失败”。
3. 在仓库内 E2E 固化一条 SFTP 上传的逐字节回归检查，防止再次退化。

## 2. 非目标

- 不改变 ZMODEM 协议栈位置（仍在浏览器），不改 `sz`/`rz` 行为。
- 不新增协议 frame 类型，不调整第十轮已通告的窗口常量。
- 不改 Agent 侧流控（第十轮已确认 Agent 侧行为正确）。
- 不引入服务端文件 API：Server 仍只转发字节，不落盘、不解析、不统计文件内容。

## 3. 根因与证据

三个缺陷同族：批量写入路径上把“部分完成”当成“完成”或“致命”。其中 3.1 是用户线上会话真正被杀死的原因，3.2 是用户看到的那句文案，3.3 是同族的潜在缺陷。三条都是既有缺陷，不是第十轮流控改造引入的回归。

### 3.1 Server 中继的 WINDOW_UPDATE 镜像队列会杀死上传流（生产根因）

`internal/server/agent_relay_transport.go` 的 `handleAgentFrameGeneration` 在 `FrameWindowUpdate` 分支先把额度加到 `sendState`，再把同一帧镜像进 `controlCh`（容量 16）供 `ReadControl` 消费者使用。镜像写入原本是 `select` 加 `default: stream.fail(protocol.ErrWindowExhausted)`。

WebSSH/SFTP 桥接只调用 `Read`，从不消费 control 队列，因此上传累计超过 16 次窗口更新即溢出，整条 stream 被判死，桥接关闭，sshd 看到连接断开，前端显示“SFTP 通道已关闭”。

证据：

- 临时 bridge 诊断输出 `first=protocol: flow-control window exhausted`（诊断埋点在修复后已全部移除，`grep -rn "\[diag\]"` 无结果）。
- 目标主机上的文件截断在 768 KiB，而当时已消耗约 16 × 128 KiB ≈ 2 MiB 额度，符合 sshd 落盘滞后于额度的预期。
- 下载方向不受影响：Server→Agent 的额度经 `WriteControl` 主动发送，不经过 `controlCh`，这解释了“只有上传会死”。

### 3.2 浏览器 `libssh2_sftp_write` 短写被当成完成（用户看到的文案）

SSH 会话是非阻塞的，`libssh2_sftp_write` 在通道窗口或隧道拥塞时返回**已消费字节数**（正值但小于请求长度），而不是 `EAGAIN`。`retrySFTP` 只重试 `EAGAIN`，`uploadFile` 把短写视为致命错误，于是前端弹出通用“文件传输失败”。文件越大、链路越慢，失败越确定。

证据：用户截图为“文件传输失败”；仓库内 E2E 复现时 3 MiB 载荷落盘 sha256 不匹配，截断点与 3.1 一致。

### 3.3 浏览器 `libssh2_channel_write` 短写丢弃终端字节（潜在缺陷）

同一非阻塞语义适用于 shell 写入路径：短写后的剩余字节被直接丢弃，长粘贴会静默丢字符，任何一层都不报错。与 3.2 同源，一并修复。

## 4. 架构决策

1. **额度权威源唯一**：`sendState` 是发送额度的唯一权威来源，`controlCh` 只是给 `ReadControl` 消费者的**尽力而为镜像**；镜像满即丢弃，不改变流状态。这样“消费者是否轮询 control 队列”不再影响正确性，也消除了同一个事实的两条相互矛盾的路径。
2. **短写按偏移补写，而不是整块重试**：以 `pointer + written` 继续写剩余字节。整块重试会把已消费的字节再写一遍，破坏字节流语义。
3. **停滞必须有界**：零进度累计超过 `writeStallLimit`（5 s，步进 `writeRetryDelay` = 10 ms）即抛错，避免死隧道把重试循环变成忙等；错误文案带上 `written/total`，便于与“远端返回 SFTP 错误”区分。
4. **回归防线放在 E2E**：单元测试能证明补写循环正确，但只有跨 Server + Agent + 真实 sshd 的 E2E 才能证明 3.1 不复发，因此新增检查 9b 并断言落盘 sha256。

## 5. 文件清单

产品代码：

- `internal/server/agent_relay_transport.go`：`FrameWindowUpdate` 分支的镜像写入改为尽力而为（空 `default` 分支并注明原因），额度仍以 `sendState` 为准。
- `web/src/webssh/ssh-client.ts`：SFTP handle `write` 与 shell `write` 改为 `while (written < chunk.length)` 补写循环，加零进度停滞保护；新增常量 `writeRetryDelay = 10`、`writeStallLimit = 5000`。
- `web/dist/`、`internal/server/web_dist/`：前端重新构建并同步的嵌入产物。二者被 `.gitignore` 忽略、不属于源码 diff，但发布前必须由 `npm run build` + `rsync` 重新生成，否则 Server 二进制里嵌的仍是旧前端（`./scripts/verify-web-embed.sh` 用于校验一致性）。

测试：

- `internal/server/agent_relay_transport_test.go`：新增 `TestAgentRelayStreamSurvivesUndrainedWindowUpdates`（40 条无人消费的 WINDOW_UPDATE 后 `Write` 必须成功，且不得出现 `RESET`）。
- `internal/server/session_test.go`：`fakeTransport` 增加非阻塞取帧 `tryReceive()`，供上面的断言使用。
- `web/src/webssh/ssh-client.spec.ts`：新增 3 个用例（SFTP 短写补写、SFTP 零进度停滞、shell 短写补写）。
- `test/e2e/webssh/run.mjs`：新增检查 9b `SFTP upload writes the file byte-for-byte`（真实 file picker 上传，等待列表出现该行，比对落盘 sha256，并断言没有错误告警）。
- `test/e2e/webssh/lib/fixtures.mjs`：生成确定性载荷 `sftp-upload.bin`，返回 `sftpUploadPath`。
- `test/e2e/webssh/lib/stack.mjs`：新增 `TM_E2E_SFTP_UPLOAD_BYTES`（默认 3 MiB）；向 throwaway SSH 主机注入 `TM_SSHHOST_SFTP_READONLY=0`（SFTP 根目录是 harness 自有的临时目录；手工运行该主机时仍默认只读）。
- `test/e2e/webssh/sshhost/main.go`：SFTP 只读开关。
- `test/e2e/webssh/README.md`：检查表新增 9b 行。

文档：

- `docs/operations/troubleshooting.md`：新增“WebSFTP 上传失败”，区分两种提示文案、截断特征与处理步骤。
- `docs/user-guide/server-admin.md`：SFTP 段落补充非阻塞短写语义、5 秒停滞判定与两种错误提示的区分。
- `docs/pull-requests/2026-09-12-admin-webssh-sftp.md`：新增第十一轮小节与 Reviewer 关注点。
- `docs/superpowers/plans/2026-09-12-webssh-bulk-transfer-flow-control.md`：更正第十轮收尾时的 E2E 与前端用例计数，并指向本计划。
- 本文件。

## 6. TDD 步骤

红灯（实现期间观察，未在补记时重跑）：

1. `go test ./internal/server -run TestAgentRelayStreamSurvivesUndrainedWindowUpdates`：修复前 `Write` 返回 `protocol: flow-control window exhausted`，与生产错误一致。
2. `npx vitest run src/webssh/ssh-client.spec.ts`：短写补写的两个用例失败（短写后不再发起第二次写入，断言的偏移与长度不成立）；停滞用例在修复前不会抛错。
3. `node test/e2e/webssh/run.mjs`：检查 9b 失败，`match=false`，落盘 768 KiB。

最小实现：3.1 改为空 `default` 分支并写明“镜像非权威”；3.2/3.3 改为补写循环加停滞保护。未做任何超出目标的额外重构。

补充断言：停滞路径、零字节写入、`RESET` 不应出现、上传后列表与磁盘一致。

绿灯：三处全部通过，命令与结果见第 7 节。

## 7. 验证命令与结果（补记时重新执行）

Go：

```bash
go test ./... -count=1
go test -race ./internal/server ./internal/agent -count=1 -timeout 25m
go vet ./...
gofmt -l internal/ cmd/ test/
git diff --check
```

结果：全部包 ok；`-race` 下 `internal/server` 427.1 s、`internal/agent` 4.3 s 通过；`go vet`、`gofmt -l`、`git diff --check` 无输出。全量 `-race` 需要 `-timeout 30m`，见 `docs/README.md`。

前端：

```bash
cd web && npm test -- --run && npm run build
rsync -a --delete web/dist/ internal/server/web_dist/ && ./scripts/verify-web-embed.sh
```

结果：30 文件 / 222 用例通过（本轮 +3）；构建成功；`web/dist and internal/server/web_dist match`。`npx vue-tsc --noEmit` 仍是既有的 177 个错误，未新增（非门禁）。

E2E：

```bash
TM_E2E_DIR=/tmp/tm-e2e-final node test/e2e/webssh/run.mjs
```

结果：15/15 PASS，三个二进制由本次运行从当前源码重新构建。其中 `SFTP upload writes the file byte-for-byte :: listed=true match=true noError=true`：落盘 3145728 字节，sha256 与 fixture 完全一致（`b5a2ef7cd80d6391de99097533247ae4b02cd1273fdf36ca91eef99a259a36af`）；`sz` 4 MiB 下载与 `rz` 1.5 MiB 上传同样逐字节校验通过，`browser console stayed clean` 通过。

诊断埋点清理：`grep -rn "\[diag\]" internal/ web/src test/` 无结果。

## 8. 发布与回滚注意事项

- Server、Agent、Web 产物必须同版本发布：3.1 的修复只在 Server 侧，只刷新前端 bundle 无效；3.2/3.3 只在浏览器侧，只升级 Server 也不会消除短写截断。
- 无 Schema 变更、无配置项变更、无 API 契约变更，因此没有数据库迁移与配置回滚动作。
- 回滚方式即回退应用版本：Server 回退会重新引入 control 镜像 kill switch，大文件上传会再次在约 2 MiB 额度处断开；前端回退会重新引入短写截断。回滚前先确认没有正在进行的批量传输。
- 回滚后如需临时规避：改用终端 `sz` 下载方向传输（不经过 `controlCh`），或把单个文件切分到 1 MiB 以内分批上传。

# Client 元数据刷新去重实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development.

**Goal:** 把“元数据未变化也每 30 秒重发 2.9 KB”的常驻开销降下来，同时保留管理台看到较新 `reported_at` 的能力。

**背景:** 2026-11-19 现场 60 秒抓包显示，`client_metadata_update` 205 帧 / 602 KB，占客户端出向的 43%。`ClientMetadataCollector.Snapshot` 已经维护 `Revision`，内容不变时 revision 不变；只有 `ReportedAt` 每次不同（`sameClientMetadata` 已忽略该字段）。

**Architecture:** 只在 `Session.sendClientMetadata` 的 `FRAME_CLIENT_METADATA_UPDATE` 路径做去重：revision 未变且距上次成功发送不足保留周期时直接返回，不发帧；超过 5 分钟发一次作为低频续期。`CLIENT_HELLO` 永远发送，保证新会话/重连后服务端立即拿到完整元数据。

**Tech Stack:** Go 标准库（`sync`、`time`），仅改 `internal/client/session.go`；不新增配置、协议帧和 Schema。

## Global Constraints

- 不改变 `CLIENT_HELLO` 语义：每个新会话至少发送一次完整元数据。
- 去重判断基于 collector 的 `Revision`，不比较 `ReportedAt`。
- 状态读写必须走 `Session.mu`，`-race` 覆盖并发调用。
- 保留周期常量固定 5 分钟，先不做成配置项（YAGNI）。
- 未获授权不执行 commit/push/merge。

## 精确文件清单

| 文件 | 变更 |
| --- | --- |
| `internal/client/session.go` | 新增 `clientMetadataKeepalive`、`metadataSentRevision`、`metadataSentAt`；Update 路径去重。 |
| `internal/client/session_test.go` | 新增去重与续期测试。 |
| `docs/superpowers/plans/2026-11-19-client-metadata-refresh-dedup.md` | 本计划。 |
| `docs/superpowers/plans/README.md` | 由 `scripts/gen_doc_index.py` 重新生成。 |

## Task 1: 元数据更新去重

- [x] 写 `TestSessionMetadataUpdateSkipsUnchangedWithinKeepalive`：会话 `Start()` 发送 `CLIENT_HELLO` 后，连续调用两次 `sendClientMetadata(FrameClientMetadataUpdate)`，断言通道中没有新增帧。
- [x] 写 `TestSessionMetadataUpdateResendsAfterKeepalive`：把 `metadataSentAt` 回拨到 5 分钟前，调用 Update，断言收到一帧 `CLIENT_METADATA_UPDATE`。
- [x] 运行 `go test ./internal/client -run 'TestSessionMetadataUpdate' -count=1`，预期失败（当前每次都发送）。
- [x] 最小实现：Update 前加 revision + 时间双重判断；成功发送 hello/update 后记录 revision 与时间。
- [x] 再运行同一条测试，预期通过。

## 验证命令

```bash
go test ./internal/client ./internal/observability -count=1
go test -race ./internal/client -count=1
go vet ./...
git diff --check
```

## 现场验证

重启后连续抓 60 秒 `tunnelmesh_client_frame_bytes_total{type="client_metadata_update"}`：应从约 76 KB/分钟降到不超过 13 会话 × 1 次续期；稳态通常为 0。

## 回滚注意事项

删除去重判断即可恢复 30 秒无条件重发；无 Schema、配置或协议兼容影响。现场回滚使用安装时保留的 `/usr/local/bin/tunnelmesh-client.bak-<timestamp>`。

## 实施结果（2026-11-19）

- 红灯：`session.metadataSentAt undefined`、`undefined: clientMetadataKeepalive`。
- 绿灯：`go test ./internal/client -run 'TestSessionMetadataUpdate' -count=1` 通过；`go test ./internal/client ./internal/observability -count=1`、`go test -race ./internal/client -count=1`、`go vet ./...`、`git diff --check` 通过。
- 现场部署后 60 秒：`client_metadata_update` 增量为 0 B（此前约 76 KB/分钟）；出向线路字节从 369,406 B 降至 288,632 B（-21.9%）。
- 新会话仍各发送 1 次 `CLIENT_HELLO`（13 会话 × 2,938 B），并收到 13 次 `CLIENT_METADATA_ACK`；进程 RSS 约 28 MB，`NRestarts=0`。

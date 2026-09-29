# 还原发行管理页各平台下载地址实施计划（补记计划）

> 补记计划：本项变更的原始实现已完成，本文按已验证事实回填，不虚构当时的红灯输出（新增用例的红灯是本次真实记录）。

## 1. 目标

- 恢复 `da994b3 feat(web): simplify release management page` 删除的“各平台发行包”区块：压缩包名、下载入口、SHA256 校验命令与复制按钮。
- 恢复使用服务端下发的 `releaseUrl` 打开对应 tag，替换此前写死在包里的 `https://github.com/nnworld/TunnelMesh/releases`。
- 源码构建（`version=dev`）没有已发布的发行包，页面必须说明这一点，而不是摆出一组必然 404 的链接。

## 2. 为什么现在可以还原

- 删除发生时发布链路还没跑通；现在 `release.yml` 会在 `vMAJOR.MINOR.PATCH` tag 上执行 `build-release.sh` 并 `gh release create dist/<version>/*`，`v1.3.0` 已带 6 个平台归档 + `SHA256SUMS` + `manifest.json`，命名与服务端 `newDownloadInfo` 拼接结果逐字一致。
- 服务端从未删除数据：`GET /api/v1/downloads` 仍返回 `assets[]`（`internal/server/download_api.go`），OpenAPI 的 `DownloadInfo` 也仍包含它。被删的只有展示层。

## 3. 架构决策

- **单一数据源**：所有地址（tag 页、每个归档、SHA256SUMS、manifest）都由服务端按 `downloads.github_repository` + 构建版本拼接，前端不内置任何仓库域名。写死的 GitHub 地址会破坏镜像仓库与 GitHub Enterprise 部署，而 `docs/operations/configuration.md` 已把该键描述成可覆盖项。
- **发布性判断放在视图层的展示语义里**：`published` 只决定要不要提示，不隐藏归档列表——归档名对自构建产物对齐仍有价值。不改服务端契约，避免为此引入新的 API 字段与迁移面。
- **测试分层**：行为用真实挂载用例覆盖（`downloads-view.spec.ts`），源码文本用例（`views.spec.ts`）只保留“不得内置仓库域名”这条静态约束。

## 4. 技术栈与规格引用

- Vue 3 `<script setup>` + Element Plus（`el-alert`/`el-tag`/`el-button`），i18n 双语言包必须键一致（`web/src/tests/i18n.spec.ts` 强制）。
- 接口契约：`GET /api/v1/downloads`（`docs/api/openapi.yaml` 的 `/api/v1/downloads` 与 `DownloadInfoEnvelope`），本轮无接口变更。
- 发布契约：`docs/deployment/binary-release.md`。

## 5. 全局约束

- 仅管理员可见（`handleDownloads` 403 前置），不新增凭据、不代理 GitHub、不缓存发行文件。
- 不改 `agents`/任何表结构、不提升 `SchemaVersion`、不动协议 frame。
- 复制的是校验命令文本，不含凭据。

## 6. 文件清单

- `web/src/views/Downloads.vue`：恢复 `.assets` 区块、`checksumCommand`/`copyChecksum`、`releaseUrl` 链接、`published` 计算属性与 `not-published` 提示与样式。
- `web/src/i18n/messages/zh-CN.ts`、`web/src/i18n/messages/en-US.ts`：恢复 `platform/archive/download/checksumCommand/copied/copyFailed` 与原 `description/loadFailed/empty` 文案，新增 `notPublished`。
- `web/src/tests/downloads-view.spec.ts`（新增）：5 条挂载用例。
- `web/src/tests/views.spec.ts`：文本断言还原为“存在各平台下载面”，并断言源码不含 `https://github.com/`。
- `docs/user-guide/server-admin.md`：发行管理段重写（含 dev 构建说明与配置来源）。

## 7. 任务间接口

- `DownloadAsset = { platform, archive, url }`、`DownloadInfo.assets: DownloadAsset[]`（`web/src/api/client.ts` 未改）。
- 校验命令模板固定为 `curl -fsSL <checksumUrl> | grep '<archive>' | sha256sum -c -`，与删除前一致，避免运维脚本口径漂移。

## 8. TDD 步骤与结果

1. 先写 `downloads-view.spec.ts` 5 条用例 → 全部失败：`Cannot read properties of undefined (reading 'path')`（脚手架缺 `vue-router` mock，属用例自身问题，补齐后仍为功能缺失）→ 真实红：`.asset` 数量为 0、无 `a.download`、无校验命令按钮、无 `notPublished`。
2. 还原视图与双语言包 → 5 passed。
3. 全量前端：`views.spec.ts` 的旧断言（要求源码不含 `asset.url`）转红 → 按还原后的口径改写，并把“不内置仓库域名”保留为静态约束。
4. `npm test -- --run` → 41 文件 / 352 用例通过。

## 9. 验证命令

```bash
cd web && npm test -- --run && npm run build
cd .. && bash scripts/verify-web-embed.sh
git diff --check
```

无 Go 代码改动，故不需要 Go 门禁（`go vet ./...` 与 `go test ./...` 在合并前门禁仍会执行）。

## 10. 回滚

- 前端与文档可直接 `git revert`：数据始终来自服务端既有字段，无状态、无迁移、无配置变更。
- 回滚后重新退回“只显示版本信息与 SHA256SUMS/manifest 链接”的形态，不影响任何接口调用方。

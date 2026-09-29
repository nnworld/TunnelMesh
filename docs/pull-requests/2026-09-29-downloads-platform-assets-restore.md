# 还原发行管理页各平台下载地址

## Title

`fix(web): restore per-platform release download links`

## Target branch

`main`

## 摘要

`da994b3` 在简化发行管理页时删掉了各平台发行包区块（压缩包名、下载入口、SHA256 校验命令与复制按钮），并把打开 GitHub Release 的地址写死为 `nnworld/TunnelMesh`。服务端 `GET /api/v1/downloads` 一直仍在返回 `assets[]`，发布链路现在也确实产出 6 个平台归档，因此恢复展示层，并让所有地址重新回到“由服务端按配置拼接”的单一来源。

## 用户影响

- 管理员在后台直接看到 `linux-amd64/arm64`、`darwin-amd64/arm64`、`windows-amd64/arm64` 六张卡片，可点下载、可复制 `curl ... | sha256sum -c -` 校验命令，与 `docs/deployment/binary-release.md` 的回滚步骤重新对得上。
- “打开 GitHub Release”回到服务端 `releaseUrl`，指向当前构建对应的 tag；镜像仓库或 GitHub Enterprise 部署不再被引导到上游仓库。
- 源码构建（`version=dev`）会看到一条提示：该版本没有已发布的发行包；归档名仍列出，便于与自构建产物对齐。

## API、Schema 与配置影响

- 无 API/Schema/配置变更：`assets[]`、`releaseUrl` 均为既有响应字段，OpenAPI 的 `DownloadInfo` 未改动；无 DDL、无 `SchemaVersion` 变化。
- 前端行为变更：不再在包里内置仓库域名，`downloads.github_repository` 成为唯一来源。

## 安全与授权影响

- 页面仍仅管理员可见（`handleDownloads` 的 403 前置未变）。
- 复制内容是校验命令文本；不引入 GitHub 凭据、不代理发行文件、不新增日志字段。

## 测试证据

红灯（新增用例，实现前）：

- `npm test -- --run src/tests/downloads-view.spec.ts` → 5 条全失败：`.asset` 数量为 0、缺少 `a.download`、缺少校验命令按钮、无 `notPublished` 提示（先前的 `Cannot read properties of undefined (reading 'path')` 是用例脚手架缺 `vue-router` mock，补齐后仍是功能缺失）。

转绿与连带：

- 还原视图与双语言包后 5 passed。
- 旧文本用例 `views.spec.ts` 断言源码不得出现 `asset.url`/`checksumCommand`（它锁的就是删除态），按还原后口径改写，并保留“源码不得含 `https://github.com/`”这条静态约束。
- `docs/user-guide/server-admin.md` 中“页面不再展示各平台下载”的表述与 `docs/operations/configuration.md` 对 `downloads.github_repository` 的说明相互矛盾，已随本次统一。

## 发布步骤

1. 合并后发布 Server（控制台产物随二进制 embed）。
2. 用一个已发布 tag 的构建核对六张卡片的链接可下载且校验命令可用；再用源码构建确认 `dev` 提示出现。

## 回滚步骤

- `git revert` 本次合并即可：无数据、无接口、无配置影响，退回“只显示版本信息与 SHA256SUMS/manifest”的形态。

## Reviewer 关注点

- `published` 用 `^v\d+\.\d+\.\d+$` 判断，与 `release.yml` 的版本校验一致；若未来允许预发布 tag（`v1.4.0-rc1`），此处与发布流水线需一起放宽。
- 归档列表在 `dev` 构建下仍然展示（只是加提示）。若产品更希望隐藏必然 404 的链接，可改为不渲染 `.assets`，属交互取舍。
- 服务端 `newDownloadInfo` 仍假定归档命名契约（`tunnelmesh-<version>-<platform>`）；这与 `scripts/build-release.sh` 是两处并行的事实，真正的单一来源应由发布契约生成，属后续独立项。

## 集成状态

- 实施计划（补记）：`docs/superpowers/plans/2026-09-29-downloads-platform-assets-restore.md`。
- `npm test -- --run` → 41 文件 / 352 用例通过（新增 `downloads-view.spec.ts` 5 条）；`npm run build` + `bash scripts/verify-web-embed.sh` → `web/dist and internal/server/web_dist match`。
- 无 Go 源码改动，仍复核：`go vet ./...` 无输出；`go test ./internal/server ./internal/storage -count=1` → ok（58.1s / 2.2s）；`git diff --check` 干净。
- 事实核对：`gh release view v1.3.0` 的资产为 6 个平台归档 + `SHA256SUMS` + `manifest.json`，命名与 `newDownloadInfo` 拼接结果一致（`tunnelmesh-v1.3.0-<platform>.tar.gz|.zip`）。

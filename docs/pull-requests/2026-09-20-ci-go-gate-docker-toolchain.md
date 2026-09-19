# CI Go 门禁与 Docker 构建阶段工具链对齐

## 标题

`ci: enforce the Go build/vet/test gate and align the image build stage with go.mod`

## 目标分支

`main`

## 关联记录

无实施计划与设计规格（`AGENTS.md`「Plan 与 PR 要求」范围内的 chore/ci 变更，不涉及功能、接口或
Schema）。两个问题都由 VPN 网关 Task 0 分支的整分支评审顺带发现，记录在该分支的
`docs/pull-requests/2026-09-19-vpn-task0-feasibility.md`「评审顺带发现的仓库既有问题」小节
（该分支尚未合并，故此处不做相对链接以免出现死链）。

## 摘要

补齐两处早已存在、与仓库自身规范不一致的基础设施缺口：

1. **CI 从来没有 Go 门禁。** `.github/workflows/ci.yml` 的 `build-test` job 在 `setup-go` 之后只跑
   `./scripts/verify-web-embed.sh`，没有任何 `go build` / `go vet` / `go test` 步骤——即 Go 代码
   编译失败、vet 报警或测试全红时，PR 依然是绿的。这与 `AGENTS.md` §13「CI 质量门禁：lint +
   覆盖率 + build，未通过禁止合并」直接冲突。现补上 `go build ./...`、`go vet ./...`、
   `go test ./... -count=1` 三步。
2. **镜像构建阶段的 Go 版本落后于 `go.mod`。** `Dockerfile` 第 3 行是 `ARG GO_VERSION=1.23`，
   而 `go.mod` 声明 `go 1.26.0`。`golang:1.23-bookworm` 最后一次更新是 2025-08-13；在默认
   `GOTOOLCHAIN=auto` 下它会在构建中途联网下载 1.26 工具链（慢、且把构建结果依赖网络与上游
   可用性），一旦被设为 `GOTOOLCHAIN=local` 就直接失败。改为 `ARG GO_VERSION=1.26`
   （Docker Hub `golang:1.26-bookworm` 存在，最近更新 2026-09-19），并加注释说明该 ARG 必须与
   `go.mod` 的 go 指令保持同步。

`docs/development/testing.md` 新增「CI 门禁范围」小节，写明 CI 实际执行哪些命令、Go 步骤为什么
必须排在 `npm run build` 之后，以及 race 为什么不进 CI。

## 用户影响

- 运行时零影响：不改任何产品代码、配置项、API、Schema 或前端行为。
- 对贡献者：PR 现在会真正跑 Go 编译、vet 与测试，红了的 PR 不能再合并；`build-test` job 增加约
  1 分钟（本机实测 `go build ./...` + `go vet ./...` + `go test ./... -count=1` 合计约 50 秒）。
- 对部署者：`docker build` 拉取的基础镜像从 `golang:1.23-bookworm` 变为 `golang:1.26-bookworm`
  （约 297 MB 层，与旧镜像同量级），构建不再中途下载工具链；产物镜像（distroless runtime）不变。
  `--build-arg APP=server|agent|client` 与 `--build-arg GO_VERSION=...` 的对外接口都不变。

## API、Schema 与配置影响

- API：无。`docs/api/openapi.yaml` 未改动。
- Schema：无。`migrations/` 与 `SchemaVersion`（14）未改动。
- 配置：无新增配置项，`go.mod`/`go.sum` 未改动。唯一的"配置"变化是 `Dockerfile` 的构建参数
  默认值 `GO_VERSION` 1.23 → 1.26；显式传 `--build-arg GO_VERSION=` 的调用方行为不变。
- CI 拓扑：`build-test` job 步骤 7 → 10，`packaging` job 未改动，无新增 job、无新增 secret、
  `permissions: contents: read` 保持不变。

## 安全与授权影响

- 不引入任何新权限：workflow 仍是 `permissions: contents: read`，新步骤只运行 Go 工具链，
  不需要 `id-token: write`、不需要额外 secret、不上传产物。
- 门禁本身是安全改进：此前 Go 侧的任何回归（含鉴权、注入、越权相关测试）都不会挡住合并。
- 基础镜像从落后一年的 `1.23-bookworm`（最后更新 2025-08-13）换到当前维护的 `1.26-bookworm`
  （更新 2026-09-19），减少构建阶段的已知 CVE 暴露面；runtime 阶段仍是
  `gcr.io/distroless/static-debian12:nonroot`，未改动。
- 未改动 `.dockerignore`（`test/` 仍会进入构建上下文）与根模块路径，两项都在本 PR 范围之外。

## 测试证据

本分支只改 workflow、Dockerfile 与文档，不含 Go 代码改动，因此门禁命令在与 `main` 同一份 Go 代码
的工作树上执行：

- `go build ./...` — 通过
- `go vet ./...` — 干净
- `go test ./... -count=1` — 全部包通过（本机 47 秒；`internal/server` 39.3s 为最慢包）
- `go test -race ./... -count=1` — 全部包通过，无 data race。本机 wall 约 10 分钟，
  最慢两包为 `internal/server` 441s 与 `internal/auth` 113s（未显式放宽 timeout，
  `internal/server` 已接近 Go 默认的单包 10 分钟上限）
- `ruby -ryaml` 解析 `.github/workflows/ci.yml` — 通过，`build-test` 步骤数 10，顺序正确
  （Go 步骤位于 `npm run build` 之后、`verify-web-embed.sh` 之前）
- `git diff --check` — 干净
- `python3 scripts/gen_doc_index.py` — 幂等，仅本记录进入 `docs/pull-requests/README.md` 索引

未执行的验证与原因：

- **`docker build` 未执行**：本环境无任何容器运行时（docker/podman/colima/lima/nerdctl 均不存在）。
  替代验证为 Docker Hub tag 存在性核验（`golang:1.26-bookworm`，`last_updated` 2026-09-19）与
  Dockerfile 静态审查；`ARG`/`FROM` 结构未变，只改了默认值。合并后第一次 `packaging`/发布构建
  即是真实验证点。
- **覆盖率门禁未加入**：`AGENTS.md` §13 提到覆盖率，但仓库当前没有任何覆盖率基线或阈值策略，
  凭空设定阈值会造成大面积红灯或形同虚设。需要单独决策后再加，本 PR 不猜。
- **race 未进 CI**：本机实测 `go test -race ./...` 需约 10 分钟（`internal/server` 单包 441s，
  已接近 Go 默认的单包 10 分钟上限），而 GitHub 共享 runner 的 CPU 弱于本机、
  `docs/development/testing.md` 也记录了整仓需 `-timeout 30m`，进 CI 会把 `build-test` 推到
  20-30 分钟并显著提高超时红灯率。按 `AGENTS.md` §13 的门禁定义（lint + 覆盖率 + build）与
  「必须执行的验证」的分工，race 保留为提交前本地门禁，并在 `testing.md` 新增小节写明这一取舍，
  避免后来者误以为 CI 已覆盖。`testing.md` 同时记录了压缩 race 成本的方向（给测试 fixture 注入
  低成本 Argon2 参数），那项落地后再把 race 纳入 CI 才有意义。

## 发布步骤

1. 合并即可，无需任何部署动作、无需重启进程、无需迁移。
2. 合并后观察下一次 PR 的 `build-test` job：确认三个 Go 步骤执行且通过，job 总时长增加约 1 分钟。
3. 下一次镜像构建（`make docker-server` 或 `docs/deployment/docker.md` 中的 `docker build`）会拉取
   `golang:1.26-bookworm`；如部署方在 CI 中缓存了基础镜像，需要刷新缓存。
4. 阶段 1 的 VPN 工作若把 `go.mod` 抬到 `go 1.26.3`，本 ARG（1.26）依然满足，无需再改。

## 回滚步骤

- `git revert` 合并提交即可，5 分钟内完成：CI 回到没有 Go 门禁的状态，Dockerfile 回到
  `GO_VERSION=1.23`。无数据、无 Schema、无配置状态需要补偿。
- 若只是 race/测试在新环境下超时或偶发红灯，优先用 `git revert` 摘掉单个 `- run:` 步骤，
  不要整条回滚，避免连 build/vet 门禁一起丢掉。
- 回滚后执行 `python3 scripts/gen_doc_index.py` 以保持索引一致。

## Reviewer 关注点

- `.github/workflows/ci.yml`：Go 步骤的位置。必须排在 `cd web && npm run build` 之后——
  `internal/server/web_dist` 不入库，`internal/server/web_test.go` 会断言嵌入内容非空且含
  `index.html`，顺序颠倒会让 `go build`/`go test` 直接失败。
- `.github/workflows/ci.yml`：确认没有把 `-race` 塞进来（成本理由见 `docs/development/testing.md`
  的「race 测试的超时要求」），也没有偷偷放宽 `permissions`。
- `Dockerfile`：`ARG GO_VERSION=1.26` 与 `go.mod` 的 `go 1.26.0` 是否一致，以及新增注释是否表达清楚
  "两者必须同步"这一耦合（VPN 阶段 1 会再次抬升 go 指令）。
- `docs/development/testing.md`：新增小节是否与 `AGENTS.md`「必须执行的验证」冲突——它不改变任何
  本地门禁要求，只说明 CI 覆盖了其中哪几条。
- 刻意不在本 PR 范围内的两项既有问题：`.dockerignore` 未排除 `test/`；根模块路径
  `github.com/tunnelmesh/tunnelmesh` 与远端 `nnworld/TunnelMesh` 不一致。

## 集成状态

改动、本地验证与文档同步已在 `codex/ci-go-gate-and-docker-go-version` 上完成。PR 复审与合并待进行；
合并后需要在下一个 PR 上确认 CI 三个 Go 步骤实际生效。

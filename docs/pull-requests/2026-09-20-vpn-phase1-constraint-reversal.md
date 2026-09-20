# VPN 网关 阶段 1：约束反转与 ADR 0002

## 标题

`docs: finish the vpn constraint reversal and record the pr`

本记录覆盖分支上的全部提交（计划 → 守卫测试 → ADR 0002 → `AGENTS.md` → 双语 README → 中文活文档 →
英文活文档 → 社区文档 → 收口），而不只是最后一个提交。

## 目标分支

`main`

本分支 stacked 在 `codex/vpn-task0-feasibility-plan` 之上：Task 0 的 PR 合并进 `main` 后，必须先
`git rebase --onto main codex/vpn-task0-feasibility-plan` 再更新 PR，否则 diff 会重复包含 Task 0 的产物。

## 关联记录

- 实施计划：[VPN 网关 阶段 1：约束反转与 ADR 0002 Implementation Plan](../superpowers/plans/2026-09-20-vpn-phase1-constraint-reversal.md)
- 设计规格：[内嵌 VPN 网关（WireGuard）设计](../superpowers/specs/2026-09-19-embedded-vpn-gateway-design.md)（§15 阶段 1、§16 文档交付、§17 已决策记录）
- 决策载体：[ADR 0002: Open a public UDP ingress for an embedded VPN gateway](../architecture/adr/0002-public-ingress-and-embedded-vpn.md)（`Status: Accepted`）
- 前置证据：[VPN 网关 Task 0：设计规格与可行性验证 spike](2026-09-19-vpn-task0-feasibility.md) 与 `test/spike/vpn-task0/REPORT.md`

## 摘要

阶段 1 解除「公网入口只使用 HTTP/HTTPS/WebSocket、Server 不监听公网 UDP、继续不实现 ICMP 与
TUN/L2 VPN」这组工程约束，使规格 §15 的阶段 3-8 可以合法落地。**本阶段不写一行产品代码**：
交付物是一个决策载体（ADR 0002）、一份修订后的工程规范（`AGENTS.md`）、一套改写后的活文档，
以及一个把「约束反转」变成长效门禁的守卫测试。

四类产物：

1. **ADR 0002（新增，150 行）**：唯一的决策载体。6 项决策（一个公网 UDP 端口、内存态 TUN over
   gVisor netstack、`AGENTS.md:148` 部分反转、策略执行留在 Server 用户态、重依赖用
   `//go:build vpn` 隔离、不引入第四个二进制）+ 后果 + 4 个被拒绝的替代方案。P2P NAT traversal
   与任意远程命令执行的禁令**保留**。
2. **`scripts/doc_claims_test.go`（新增，171 行）**：24 条禁用短语扫描 5 个活文档根
   （`AGENTS.md`、`README.md`、`README.zh-CN.md`、`docs/`、`deploy/`），按**行**计数违规；
   时点记录目录（`docs/superpowers/`、`docs/pull-requests/`、`docs/architecture/adr/`）按
   `docs/development/documentation.md`「时点记录不可改写」排除。另有
   `TestADR0002RecordsTheConstraintReversal` 断言决策载体本身诚实（`Status: Accepted`、链接规格与
   Task 0 报告、保留两项禁令）。
3. **`AGENTS.md` 两处修订**：`:11` 从绝对禁令改为「以 HTTP/HTTPS/WebSocket 为主；启用 VPN 网关时
   额外监听一个公网 UDP 端口」；`:148` 拆成两行——禁令只留 P2P NAT traversal 与 RCE，新增一行
   写明 VPN 数据面的实现边界（内存态 TUN、不开 `/dev/net/tun`、不要 `CAP_NET_ADMIN`、不转发 L2 帧、
   ICMP 只支持 echo、`//go:build vpn` 隔离、`server.vpn.enabled=false` 时零 VPN 资源）。
4. **21 个活文档的 29 处主张改写**：全局主张反转为「已批准、分阶段实施中、当前版本尚未提供」并链接
   ADR 0002；仍然为真的局部主张**限定范围**而不是删除（例如 `publish` 不把 `tcp`/`udp` 作为公网监听
   协议、tp-* 入口不新增公网监听端口、动态域名复用既有 80/443、SOCKS5 只作为 Client 本地入口）。
   `docs/architecture/overview.md` 新增「内嵌 VPN 网关（已批准，实施中）」小节与目标链路图；
   `docs/community/roadmap.md` 把该特性收入 Next(planned) 并改写 not-planned 清单。

## 用户影响

- **运行时零影响。** 没有产品代码、配置项、API、Schema、前端或部署产物变更；已部署的 Server / Agent /
  Client 行为完全不变，不需要重启、迁移或注入任何环境变量。
- 对使用者的影响只有文档措辞：所有活文档统一为「公网入口以 HTTP/HTTPS/WSS 为主，内嵌 VPN 网关已批准、
  实施中、当前版本尚未提供」。**没有任何一份文档宣称 VPN 已可用**，因此不会产生新的
  「文档承诺了但产品没有」落差；能力文档（`docs/user-guide/vpn.md`、`docs/deployment/vpn-gateway.md`、
  `docs/operations/vpn.md`）按规格 §16 随阶段 4/6/8 的实现同批交付。
- 对贡献者的影响：`go test ./scripts/` 从此多一道文档门禁。若有人重新写下被 ADR 0002 反转的绝对主张，
  测试直接红灯并打印文件、行号、命中短语与该行原文。短语清单是权威清单，**不得为了让测试变绿而删短语**；
  确需调整必须先改清单、在提交信息里说明理由并同步更新计划。
- 本阶段**不解锁**任何对外表述：ADR 0002 明确「两项证据门禁未关闭前，no-root / no-`CAP_NET_ADMIN`
  与 ICMP 相关主张不得对外宣称已验证」。

## API、Schema 与配置影响

- **API**：无。`docs/api/openapi.yaml` 未改动。规格 §9 的 VPN 管理接口属阶段 4/6。
- **Schema**：无。`migrations/` 未改动，`internal/storage/db.go:23` 的 `SchemaVersion` 仍为 14。
  规格 §7 的 v15 只是草案。
- **配置**：无新增配置项，`docs/operations/configuration.md` 与 `docs/operations/config-examples.md`
  未改动；`server.vpn.*` 与 `TUNNELMESH_VPN_*` 属阶段 4。
- **依赖**：根 `go.mod` / `go.sum` 零改动（`git status --porcelain go.mod go.sum` 为空）。守卫测试只用
  标准库（`io/fs`、`os`、`path/filepath`、`strings`、`testing`）。gVisor 与 wireguard-go 属阶段 3，
  届时必须同步抬高 `Dockerfile` 的 `ARG GO_VERSION`。
- **发布产物体积**：不变（无代码进入二进制）。
- **文档索引**：`docs/pull-requests/README.md`、`docs/architecture/adr/README.md`、
  `docs/superpowers/plans/README.md`、`docs/superpowers/specs/README.md` 由
  `scripts/gen_doc_index.py` 重新生成，未手工编辑；ADR 与 PR 记录的交叉引用已建立关联。

## 安全与授权影响

- **仓库内无任何密钥材料。** 本阶段只改 Markdown 与一个只读扫描测试；分支 diff 未引入私钥块、
  client secret、口令或恢复码。
- **公网攻击面的变化由 ADR 承载，而非本阶段代码**：ADR 0002 记录「公网面增加一个 UDP 端口」的后果与
  必需的控制（握手洪泛、未注册公钥、丢包类别的计数器/限流/告警；节点私钥只由环境变量注入；peer 私钥
  sealed 存储并复用 service token 的 confirm + audit + `no-store` reveal 路径）。这些控制在阶段 3-6 实现。
- **权限边界收紧而非放宽**：策略执行留在 Server 用户态并包装既有 `routing.Policy`（单一数据源），
  Agent 不新增特权、不维护防火墙规则、内网路由表不改；Server 不打开 `/dev/net/tun`、不要求
  `CAP_NET_ADMIN`。L2 以太网帧永不转发。
- **授权影响**：`AGENTS.md` 是仓库级工程规范，本次修订等同于放宽「Server 可以持有的公网监听能力」与
「Server 可以依赖的技术栈」两条约束。修订在实现前完成并经用户确认（`AGENTS.md`「Plan 与 PR 要求」），
  不是实现后的追认。
- **保留的禁令**：P2P NAT traversal、任意远程命令执行、SSH 扩展为通用命令执行 API 仍被禁止；
  Server 不主动构造 ICMP echo 以外的 ICMP 类型。

## 测试证据

守卫测试的红灯基线与逐任务收敛全部实测（在 `git worktree` 检出的对应提交上复跑，非估算）：

| 提交 | 内容 | `TestLiveDocs…` 违规行数 |
| --- | --- | --- |
| `0854464` | 实施计划 | 测试尚未加入（0） |
| `5de6979` | T1 守卫测试 | **29**（红灯基线：21 个文件、41 条短语命中） |
| `6c488be` | T2 ADR 0002 | 29（ADR 目录被排除，数字不变即为预期） |
| `1a2e100` | T3 `AGENTS.md` | 27 |
| `f5086df` | T4 双语 README | 22 |
| `47a6895` | T5 中文活文档 | 9 |
| `6230448` | T6 英文活文档 | 6 |
| `d38d8b5` | T7 社区文档 | 1 |
| HEAD | T8 `docs/README.md` + 索引 + 本记录 | **0** |

- 红灯基线实测输出末行：`29 live-document line(s) across the repo still assert a reversed constraint`；
  逐行 `t.Errorf` 命中 21 个唯一文件、41 条短语（同一行常被中英文短语各命中一次，故按行计数为 29）。
- 绿灯实测输出：`ok github.com/tunnelmesh/tunnelmesh/scripts`；`-v` 下
  `--- PASS: TestLiveDocsDoNotAssertReversedConstraints`、`--- PASS: TestADR0002RecordsTheConstraintReversal`。
- 仓库门禁：`go build ./...` 通过；`go vet ./...` 干净；`go test ./... -count=1` 22 个包全 `ok`、
  无 `FAIL`（含 `internal/server` 62.9s、`scripts` 2.0s）；`go test -race ./... -timeout 30m -count=1`
  全绿（22 个包 `ok`，无 `DATA RACE`、无 `FAIL`）；`git diff --check` 干净（含工作区与
  `codex/vpn-task0-feasibility-plan..HEAD`）。
- 文档门禁：`python3 scripts/gen_doc_index.py` 幂等，连续执行两次第二次无 diff；
  时点记录零改写核验通过——`git diff --name-status codex/vpn-task0-feasibility-plan..HEAD --
  docs/superpowers docs/pull-requests docs/architecture/adr` 只出现 `A`（计划、ADR 0002、本记录）与
  生成索引的 `M`（`adr/README.md`、`pull-requests/README.md`、`superpowers/plans/README.md`、
  `superpowers/specs/README.md`），**没有任何既有 spec/plan/PR 记录被 `M`**；
  链接可达性核验通过——本阶段改动的 25 个 Markdown 文件共 318 条相对 `.md` 链接全部可达、0 条死链
  （含 ADR、规格、计划、Task 0 报告与 PR 记录互链）；核验排除代码块与行内代码里的示意链接写法，
  因此计划正文中 `[ADR 0002](../architecture/adr/…)` 这类「从 `docs/<subdir>/` 出发」的示例不计为死链。
- 未执行的验证与原因：前端未改动（`web/` 零 diff）→ `npm test -- --run` 与 `npm run build` 不适用，
  `internal/server/web_dist` 已存在故 embed 断言可跑；无 Docker/Compose 改动 → 容器验证不适用；
  Linux 非 root `--cap-drop=ALL` 复跑与 Task 5（非特权 ICMP datagram socket）取证**仍未执行**，
  它们是阶段 7 的门禁、不是本阶段的门禁，ADR 0002 已把它们记为「两项未关闭的证据门禁」。
- 守卫测试的已知局限（写进测试注释）：子串匹配故意做成脆弱，但**抓不到换一种说法的同一主张**，
  因此它补充评审而不是替代评审。

## 发布步骤

1. 合并即可：无迁移、无配置、无二进制变化，不需要重启任何进程，不需要通知运维放行任何端口
   （VPN 端口在阶段 4 才真正监听）。
2. 合并后执行 `python3 scripts/gen_doc_index.py` 校验索引一致（若 main 上有其它 PR 记录并行合入）。
3. 阶段 3 开工前必须先关闭 ADR 0002 记录的两项证据门禁：非 root `--cap-drop=ALL` Linux 容器复跑
   Task 2-4 取证 capability；Task 5 在 Linux 补跑并回填 ICMP id 改写实测值。在此之前不得对外宣称
   「无需 root / 无需 `CAP_NET_ADMIN`」已验证，也不得启动阶段 7（Agent ICMP）。
4. 阶段 3 引入 gVisor 时同步、显式抬高 `Dockerfile` 的 `ARG GO_VERSION`（Task 0 已记录的既有落后项）。
5. 阶段 4 上线 VPN 监听端口时，发布说明必须写明：云安全组与主机防火墙需单独放行该 UDP 端口，
   它不经 Nginx/OpenResty；止损路径是 `server.vpn.enabled=false` + 重启（5 分钟内可完成）。

## 回滚步骤

- `git revert` 本阶段提交即可完全回滚：只涉及 Markdown 与一个测试文件，没有 Schema、配置、
  二进制或运行时状态需要补偿，不需要数据库备份、不需要停机，5 分钟内可完成。
- 回滚文档改写时**必须一并回滚 `scripts/doc_claims_test.go`**，否则活文档重新出现禁用短语会让 CI 红灯。
- 回滚后执行 `python3 scripts/gen_doc_index.py` 重新生成四份索引，避免索引指向已删除的记录。
- **ADR 编号永不复用**（`docs/architecture/adr/README.md` 约定）。若决策被推翻，不得删除 ADR 0002，
  而应新增一条 ADR 并把 0002 的 `Status` 改为 `Superseded by NNNN`；
  `TestADR0002RecordsTheConstraintReversal` 断言 `- Status: Accepted`，届时必须同步调整该测试，
  调整理由写进新 ADR。
- 阶段 3-8 全部以 ADR 0002 为前提，回滚即冻结后续所有阶段；Task 0 的结论与 spike 产物不受影响。

## Reviewer 关注点

- **措辞是否构成虚假承诺**：29 处改写必须逐条读成「已批准 / 实施中 / 当前版本尚未提供」，
  不能读成「已支持」。重点看 `README.md:15`、`README.zh-CN.md:14`、`docs/architecture/overview.md`
  新增小节、`docs/community/comparison.md` 的 Public ingress 行与 Honest trade-offs 段、
  `docs/community/roadmap.md` 的 Next(planned) 条目。
- **限定范围而不是删除**：以下主张在 VPN 落地后依然为真，必须保留且只限定范围——tp-* 入口不新增
  公网监听端口（`docs/user-guide/http-proxy-entry.md`）、动态域名复用既有 80/443
  （`docs/user-guide/server-admin.md`、`docs/user-guide/managed-http-route.md`）、SOCKS5 只是 Client
  本地入口（`docs/user-guide/client.md`）、泛域名只解决 HTTP/HTTPS/WSS 路由
  （`docs/deployment/nginx.md`、`docs/deployment/openresty-proxy-entry.md`）。
- **双语一致性**：`docs/user-guide/sso-and-mfa.md:507` 与 `docs/en/user-guide/sso-and-mfa.md:569`
  是同一段落的中英版本，语义必须等价（`documentation.md` 规定语义冲突以中文深度文档为准，
  本处要求两边一致）。`README.md` 与 `README.zh-CN.md` 同理。
- **守卫测试的扫描范围**：`liveDocRoots` 含 `deploy/`，`immutableRecordDirs` 排除三类时点记录，
  `skipDirs` 跳过 `node_modules`、`web_dist`、`.git`。若未来新增顶层文档目录，必须同步加进
  `liveDocRoots`，否则新目录的主张不受门禁保护。
- **ADR 与计划的一致性**：ADR 0002 的 6 项决策必须与规格 §17 已决策记录、Task 0 报告的
  9 项「必须回流到阶段 1 的设计修正」对齐；其中「首个 SYN 必被丢弃 → 首连延迟含一次客户端 RTO」
  已写进 ADR 后果，阶段 4 必须给出取舍或 listener 预热方案。
- **本阶段刻意不做的范围**（规格 §16 列在整个特性清单里，但描述的是尚不存在的配置键与接口）：
  `docs/api/openapi.yaml`、`docs/operations/configuration.md`、`docs/operations/config-examples.md`、
  `deploy/README.md`、`deploy/grafana/README.md`，以及三份能力文档；`.dockerignore`、根模块路径、
  CI 门禁等既有问题由其它分支处理。

## 集成状态

阶段 1 的全部 9 个任务已在 `codex/vpn-phase1-constraint-reversal` 上完成并推送，守卫测试从红灯 29
收敛到绿灯 0，仓库与文档门禁全绿。PR 复审与合并待进行。阶段 2（Task 0 可行性验证）已完成；
阶段 3-8（`internal/vpn/` 数据面、管理 API 与 Schema v15、前端、Agent ICMP、发布打包、运维文档）
尚未开工，且以本 PR 合并为前提。

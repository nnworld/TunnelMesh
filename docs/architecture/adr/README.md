# 架构决策记录（ADR）

<!-- 本文件由 scripts/gen_doc_index.py 生成，请勿手工编辑。 -->

重大架构变更必须留下 ADR，记录决策背景、决策内容、替代方案和迁移影响（见
[AGENTS.md](../../../AGENTS.md)「代码评审与演进」）。

## 约定

- 文件名 `NNNN-<short-title>.md`，四位编号单调递增、永不复用。
- 已接受的 ADR 不修改正文；决策被推翻时新增一条 ADR，并把旧条目 Status 改为 `Superseded by NNNN`。
- Status 取值：`Proposed`、`Accepted`、`Deprecated`、`Superseded by NNNN`。

## ADR 清单

| 编号 | 标题 | 状态 | 关联记录 |
| --- | --- | --- | --- |
| 0001 | [Separate scoped service credentials from management sessions](0001-scoped-service-tokens.md) | Accepted | — |
| 0002 | [数据面窗口与 credit 不变量](0002-dataplane-window-credit-invariants.md) | Accepted | [计划：Client receive-window credit timing fix p…](../../superpowers/plans/2026-09-24-client-receive-window-credit.md)<br>[计划：Client WINDOW_UPDATE 泄漏修复](../../superpowers/plans/2026-09-24-client-window-update-leak.md)<br>[计划：Agent / Server / Client 数据面流控与阻塞隔离加固](../../superpowers/plans/2026-09-24-dataplane-flow-control-hardening.md)<br>[PR：Client receive-window credit timing fix](../../pull-requests/2026-09-24-client-receive-window-credit.md)<br>[PR：Client WINDOW_UPDATE 泄漏修复](../../pull-requests/2026-09-24-client-window-update-leak.md)<br>[PR：Agent / Server / Client 数据面流控与阻塞隔离加固](../../pull-requests/2026-09-24-dataplane-flow-control-hardening.md) |

## 新增 ADR 的步骤

1. 复制现有 ADR 的结构，使用下一个可用编号。
2. 在正文中链接触发该决策的实施计划或设计规格。
3. 执行 `python3 scripts/gen_doc_index.py` 重新生成本索引。

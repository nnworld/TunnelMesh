# 开发文档

面向贡献者。工程规范（架构约束、分层、数据库与迁移策略、TDD、Plan 与 PR 要求、Git 规范、
必须执行的验证）的权威来源是 [AGENTS.md](../../AGENTS.md)；本目录只保存执行细节和索引入口，
不复制规则正文。

## 本目录

- [测试与验证](testing.md)：三层验证的覆盖范围、`-race` 超时要求、嵌入产物校验、MySQL/etcd 验证条件
- [文档规范](documentation.md)：目录职责、命名约定、索引维护、时点记录不可改写原则、链接与敏感信息约定

## 变更记录索引

| 记录类型 | 索引 | 命名 |
| --- | --- | --- |
| 实施计划 | [plans/README.md](../superpowers/plans/README.md) | `YYYY-MM-DD-<feature-name>.md` |
| 设计规格 | [specs/README.md](../superpowers/specs/README.md) | `YYYY-MM-DD-<feature-name>-design.md` |
| PR 描述记录 | [pull-requests/README.md](../pull-requests/README.md) | `YYYY-MM-DD-<feature-name>.md` |
| 架构决策记录 | [adr/README.md](../architecture/adr/README.md) | `NNNN-<short-title>.md` |

四份索引由脚本生成，新增记录后执行：

```bash
python3 scripts/gen_doc_index.py
```

## 相关文档

- [项目完整性清单](../operations/completeness-checklist.md)：已交付能力与显式 deferred 项
- [Schema 升级与回滚](../operations/schema-upgrades.md)：版本升级路径与止损步骤
- [OpenAPI](../api/openapi.yaml)：管理 API 契约
- [WebSSH 浏览器端到端测试](../../test/e2e/webssh/README.md)：真实 Chrome + Server/Agent/SSH 主机
- [tp-* 代理入口 OpenResty 端到端测试](../../test/e2e/proxy-entry/README.md)：真实 OpenResty 容器 + 内部入口替身
- [架构概览](../architecture/overview.md)、[集群架构](../architecture/cluster.md)

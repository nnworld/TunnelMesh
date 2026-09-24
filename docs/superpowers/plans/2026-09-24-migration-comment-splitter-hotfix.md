# 迁移脚本注释分号解析紧急修复计划

状态：紧急修复（用户已要求继续；生产已出现启动失败）

## 1. 事故现象

生产执行 `v0014_to_v0015` 时失败：

```text
apply migration v0014_to_v0015: Error 1064 (42000):
You have an error in your SQL syntax; ...
near 'every later write filters on `connection_epoch = <true token>` and
-- then match'
```

`schema_meta` 未推进到 15，Server 进程退出，服务不可用。

## 2. 根因

`applySchemaStatements` 使用 `strings.Split(script, ";")`，没有识别 SQL 注释或字符串：
- `migrations/incremental/v0014_to_v0015/mysql.sql:7` 的注释包含 `insert; every later write...`。
- 分割后，第二段以注释正文 ` every later write...` 开头，不再是合法 SQL，因此 MySQL 返回 1064。
- 第一条 `ALTER TABLE` 位于该非法片段之后，尚未执行；`schema_meta` 仍为 14。

这不是迁移语义错误，而是执行器把注释里的分号当成语句边界。

## 3. 目标

1. `applySchemaStatements` 能正确忽略 `--`、`#`、`/* ... */` 注释中的分号。
2. 字符串与引用标识符中的分号不会被误切分。
3. 已发布的 `v0014_to_v0015` 脚本不改，生产 retry 可以直接成功。
4. 添加回归测试，防止未来任何迁移注释再触发同类事故。

## 4. 非目标

- 不修改已发布增量脚本。
- 不新增 Schema 版本；这是执行器缺陷，不是 Schema 语义缺陷。
- 不引入第三方 SQL parser。

## 5. 设计

新增 `splitSQLStatements(script string) []string`：
- 逐字符扫描。
- 注释状态：`--` 到行尾、`#` 到行尾、`/* ... */`。
- 引用状态：单引号、双引号、反引号，支持 `''` 转义。
- 注释内容不进入语句；语句仍按 `;` 切分。
- 空语句跳过。

`applySchemaStatements` 改用该函数。

## 6. 文件清单

- `internal/storage/db.go`
- `internal/storage/db_test.go`
- `docs/superpowers/plans/2026-09-24-migration-comment-splitter-hotfix.md`
- `docs/pull-requests/2026-09-24-migration-comment-splitter-hotfix.md`
- 文档索引重新生成

## 7. TDD

1. 新增失败测试：
   - `v0014_to_v0015/mysql.sql` 解析结果必须只有两条 ALTER。
   - 注释分号不得进入语句。
   - 字符串/引用中的分号不得切分。
2. 实现 `splitSQLStatements`。
3. 通过聚焦测试、完整 Go 测试、race、vet、diff check。

## 8. 验证

```bash
go test ./internal/storage/ -run 'SQLStatement|Migration' -count=1
go test ./... -count=1
go test -race ./...
go vet ./...
git diff --check
```

## 9. 止损与回滚

生产当前应先回退旧版 Server 包；`schema_meta` 仍为 14，可直接启动。修复后重新部署新版并 retry 迁移。

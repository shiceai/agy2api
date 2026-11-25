# 数据库迁移

迁移脚本编译时嵌入二进制，服务启动时按文件名顺序自动执行，不需要单独运行迁移命令。

## 规则

- 文件名为 `YYYYMMDD_NNN_description.sql`：同日序号从 `001` 起递增，描述用小写 snake_case。
- 执行过的文件记录在 `schema_migrations` 表，每个文件只执行一次。已提交的迁移不得改名、修改或删除，只能追加新文件。
- 每个文件在一个事务里执行，失败时整体回滚，服务拒绝启动。SQLite 里不能放进事务的语句（如 `VACUUM`、修改 `journal_mode`）不要写进迁移。
- 每个迁移围绕一个能独立验证的目标；大批量数据回填单独成文件。

## 表结构约定

为将来迁到 PostgreSQL 留余地：

- 使用 `STRICT` 表，让类型检查接近 PostgreSQL。
- 时间存 Unix 毫秒整数；JSON 存文本，在 Go 里解析；IP 存文本。
- 防重复只靠唯一约束加 `ON CONFLICT`，不"先查再插"。
- 表结构只由这里的 SQL 定义，不用 ORM 的 AutoMigrate。

## 验证

新增迁移后执行：

```bash
go test ./ddl/ ./internal/store/
```

检查文件命名，并在空库上执行全部迁移、重复启动不报错。

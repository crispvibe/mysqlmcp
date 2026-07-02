# MySQLMCP

**简体中文** | [English](README_EN.md)

给 AI 用的 MySQL 数据库管理工具。让 AI 直接帮你建表、改字段、查数据、删数据、管事务——不用手写 SQL。

单文件 Go 二进制，不需要 Node.js 或 Python。基于 [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql) 二次开发。

## 安装

```bash
git clone https://github.com/crispvibe/mysqlmcp.git
cd mysqlmcp
go build -o go-mcp-mysql .
```

把编译出来的 `go-mcp-mysql` 放到你想放的位置，比如 `/usr/local/bin/` 或 `~/bin/`。

## 配置

在你的 AI 客户端（Cursor、Windsurf、Claude Desktop 等）的 MCP 配置里加上：

```json
{
  "mcpServers": {
    "mysql": {
      "command": "go-mcp-mysql",
      "args": [
        "--host", "localhost",
        "--user", "root",
        "--pass", "yourpassword",
        "--port", "3306",
        "--db", "yourdb"
      ]
    }
  }
}
```

或者用 DSN：

```json
{
  "mcpServers": {
    "mysql": {
      "command": "go-mcp-mysql",
      "args": [
        "--dsn", "root:password@tcp(localhost:3306)/mydb?parseTime=true&loc=Local"
      ]
    }
  }
}
```

> `command` 写二进制的完整路径，比如 `/Users/you/bin/go-mcp-mysql`。

### 可选参数

| 参数 | 作用 |
|---|---|
| `--read-only` | 只读模式，只能查不能改 |
| `--with-explain-check` | 执行前先看查询计划，拦住全表扫描 |

### 让 AI 帮你配置

直接跟 AI 说：

> 帮我配置一个 MySQL MCP。仓库地址 https://github.com/crispvibe/mysqlmcp ，拉下来编译，数据库 IP 是 xxx，端口 3306，用户名 xxx，密码 xxx，库名 xxx。

AI 会自己拉仓库、编译、写配置。

## 能干什么

**看家底**：列库列表、查表结构、查字段、查索引、查约束、查表大小行数、查数据库配置和状态、查当前在跑的查询

**查数据**：跑 SELECT，最多 1000 行，超了会提醒加条件缩小范围

**改数据**：插入（只能 INSERT）、更新（必须带 WHERE）、删除（必须带 WHERE）

**改表结构**：建库删库、建表删表清空表、加改删字段、加删索引、跑 DDL、批量跑多条 SQL

**事务**：开事务 → 读写 → 提交或回滚，5 分钟没提交自动回滚

**运维**：杀掉卡住的查询

## 安全

- UPDATE/DELETE 不带 WHERE 会被拦，防止误删全表
- INSERT 工具里混进 DELETE/UPDATE 会被拦
- 查询结果超 1000 行自动截断并提醒
- 有只读模式，适合生产环境

## 工具列表（31 个）

**查询**：`list_database` `list_table` `show_create_table` `show_columns` `show_index` `show_constraints` `show_table_status` `show_processlist` `show_variables` `show_status` `explain` `read_query`

**改数据**：`write_query` `update_query` `delete_query`

**改结构**：`create_database` `drop_database` `create_table` `alter_table` `drop_table` `truncate_table` `create_index` `drop_index` `execute_ddl` `execute_migration`

**事务**：`begin_transaction` `transaction_read_query` `transaction_exec_query` `commit_transaction` `rollback_transaction`

**运维**：`kill_query`

## 相比原项目提升了什么

原项目 [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql) 只有 9 个工具，没有事务、没有索引管理、没有运维诊断、没有安全护栏，连接管理也有几个会崩溃的 bug。

### 工具数量：9 → 31

| 类别 | 原项目 | 本项目 | 新增 |
|---|---|---|---|
| 查表结构 | `desc_table` 1 个 | `show_create_table` / `show_columns` / `show_index` / `show_constraints` / `show_table_status` | +4 |
| 查数据 | `read_query` | `read_query` + `explain` | +1 |
| 改数据 | `write_query` / `update_query` / `delete_query` | 同上 + 护栏 | 0 |
| 改结构 | `create_table` / `alter_table` | + `create_database` / `drop_database` / `drop_table` / `truncate_table` / `create_index` / `drop_index` / `execute_ddl` / `execute_migration` | +8 |
| 事务 | 无 | `begin` / `read` / `exec` / `commit` / `rollback` | +5 |
| 运维 | 无 | `show_processlist` / `kill_query` / `show_variables` / `show_status` | +4 |

### 修了的 bug

- **连不上时卡死 75 秒**：原项目 DSN 没设超时，连不上要等操作系统 TCP 超时（约 75 秒）。本项目改成 10 秒。
- **死连接不回收**：原项目连接池没有生命周期管理，断了不清理越攒越多。本项目加了自动回收（5 分钟最长寿命、2 分钟空闲回收）。
- **并发时偶发崩溃**：原项目全局数据库变量没锁，并发初始化会竞争。本项目加了互斥锁。
- **EXPLAIN 遇到空值会崩**：原项目解析 EXPLAIN 时 `select_type` 为 NULL 会空指针 panic。本项目加了空值检查。
- **参数缺失会崩**：原项目三个写操作用裸类型断言取参数，缺失或类型不对会 panic。本项目改成安全检查。
- **错误被掩盖**：原项目 EXPLAIN 的 `rows.Err()` 检查位置不对，查询中途出错时真实错误被吞掉。本项目修正了检查顺序。

### 加的安全护栏

- **INSERT 工具只允许 INSERT**：原项目 `write_query` 什么语句都能跑。本项目验证语句类型，混进 DELETE/UPDATE/DROP 会被拦。
- **UPDATE/DELETE 必须带 WHERE**：原项目只是描述里提醒，不强制。本项目代码层面拦截，全表操作要显式传 `allow_all=true`。
- **查询结果上限 1000 行**：原项目查大表会把全部结果塞给 AI，可能撑爆内存。本项目限 1000 行，超了截断并输出警告。

### 其他改进

- `alter_table` 放开了 DROP COLUMN 限制
- `SHOW VARIABLES LIKE` / `SHOW STATUS LIKE` 用字符串拼接替代参数化（MySQL 不支持这俩参数化）
- 事务 5 分钟自动回滚，防止 AI 忘了关事务锁表

## 开源协议

MIT

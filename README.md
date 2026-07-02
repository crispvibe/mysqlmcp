<div align="center">

# MySQLMCP

**简体中文** | [English](README_EN.md)

给 AI 用的 MySQL 数据库管理工具。

[![Go Reference](https://pkg.go.dev/badge/github.com/crispvibe/mysqlmcp.svg)](https://pkg.go.dev/github.com/crispvibe/mysqlmcp)
[![Go Report Card](https://goreportcard.com/badge/github.com/crispvibe/mysqlmcp)](https://goreportcard.com/report/github.com/crispvibe/mysqlmcp)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/crispvibe/mysqlmcp?style=social)](https://github.com/crispvibe/mysqlmcp/stargazers)

*觉得有用的话，点个 ⭐ 支持一下～*

</div>

---

## 这是什么

一句话：让 AI 帮你管 MySQL 数据库。

你跟 AI 说"帮我建个用户表"、"查一下会员表结构"、"把过期的订单删掉"，AI 就直接干了——建表、改字段、查数据、删数据、跑事务，都不用你手写 SQL。

装上就能用，单文件，不需要装 Node.js 或 Python。

基于 [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql) 改的，修了一堆 bug，加了一些新功能。

## 能干什么

**看家底**
- 列出所有数据库、所有表
- 查看表结构：建表语句、字段、索引、约束
- 查看表有多大、多少行、什么引擎
- 查看数据库当前的配置和运行状态
- 查看当前有哪些查询在跑

**查数据**
- 跑 SELECT 查数据，最多返回 1000 行，超了会提醒你加条件缩小范围

**改数据**
- 插入：只能 INSERT，混进 DELETE 会被拦
- 更新：必须带 WHERE，全表更新要显式确认
- 删除：必须带 WHERE，全表删除要显式确认

**改表结构**
- 建库、删库
- 建表、删表、清空表
- 加字段、改字段、删字段、加索引、删索引
- 跑 DDL、批量跑多条 SQL

**事务**
- 开事务 → 读写 → 提交或回滚
- 5 分钟没提交自动回滚，防止忘了关事务锁表

**运维**
- 杀掉卡住的查询

## 安全设计

- 删库删表这种操作不会被拦，但 AI 会先跟你确认
- UPDATE 和 DELETE 不带 WHERE 会被拦，防止误删全表
- INSERT 工具里混进其他语句会被拦
- 查询结果超 1000 行自动截断，防止撑爆
- 有只读模式，开了之后只能查不能改，适合生产环境

## 安装

### 下载二进制

去 [Releases](https://github.com/crispvibe/mysqlmcp/releases) 下载对应平台的文件，放进 `$PATH`。

### 源码编译

```bash
go install -v github.com/crispvibe/mysqlmcp@latest
```

或者：

```bash
git clone https://github.com/crispvibe/mysqlmcp.git
cd mysqlmcp
go build -o go-mcp-mysql .
```

## 配置

### 方式一：填参数

```json
{
  "mcpServers": {
    "mysql": {
      "command": "go-mcp-mysql",
      "args": [
        "--host", "localhost",
        "--user", "root",
        "--pass", "password",
        "--port", "3306",
        "--db", "mydb"
      ]
    }
  }
}
```

### 方式二：用 DSN

```json
{
  "mcpServers": {
    "mysql": {
      "command": "go-mcp-mysql",
      "args": [
        "--dsn", "username:password@tcp(localhost:3306)/mydb?parseTime=true&loc=Local"
      ]
    }
  }
}
```

> 二进制不在 `$PATH` 里就把 `command` 写成完整路径。

### 可选参数

| 参数 | 作用 |
|---|---|
| `--read-only` | 只读模式，只能查不能改 |
| `--with-explain-check` | 执行前先看查询计划，拦住全表扫描 |

## 工具列表

**查询（只读）**：`list_database` / `list_table` / `show_create_table` / `show_columns` / `show_index` / `show_constraints` / `show_table_status` / `show_processlist` / `show_variables` / `show_status` / `explain` / `read_query`

**改数据**：`write_query`（INSERT）/ `update_query` / `delete_query`

**改结构**：`create_database` / `drop_database` / `create_table` / `alter_table` / `drop_table` / `truncate_table` / `create_index` / `drop_index` / `execute_ddl` / `execute_migration`

**事务**：`begin_transaction` / `transaction_read_query` / `transaction_exec_query` / `commit_transaction` / `rollback_transaction`

**运维**：`kill_query`

## 相比原项目提升了什么

原项目 [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql) 只有 9 个工具，没有事务、没有索引管理、没有运维诊断、没有安全护栏，连接管理也有几个会导致崩溃的 bug。这个版本在原项目基础上做了以下提升：

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

原项目里有几个会导致进程崩溃或卡死的问题：

- **连不上时卡死 75 秒**：原项目 DSN 没设超时，连不上数据库时要等到操作系统 TCP 超时（约 75 秒）才报错。本项目改成 10 秒超时。
- **死连接不回收**：原项目连接池没有生命周期管理，连接断了也不会被清理，越攒越多直到耗尽。本项目加了连接池自动回收（5 分钟最长寿命、2 分钟空闲回收）。
- **并发时偶发崩溃**：原项目全局数据库连接变量没有锁，多个请求同时初始化时会竞争。本项目加了互斥锁。
- **EXPLAIN 遇到空值会崩**：原项目解析 EXPLAIN 结果时，如果 `select_type` 字段是 NULL 会触发空指针 panic。本项目加了空值检查。
- **参数缺失会崩**：原项目三个写操作工具用裸类型断言取参数，参数缺失或类型不对会 panic。本项目改成了安全检查，参数不对会报错而不是崩。
- **错误被掩盖**：原项目 EXPLAIN 的 `rows.Err()` 检查位置不对，查询中途出错时真实错误会被吞掉。本项目修正了检查顺序。

### 加的安全护栏

原项目的写操作没有任何防护，AI 一不小心就能删全表：

- **INSERT 工具只允许 INSERT**：原项目的 `write_query` 名字叫 write 但什么语句都能跑。本项目验证语句类型，混进 DELETE/UPDATE/DROP 会被拦。
- **UPDATE/DELETE 必须带 WHERE**：原项目只是描述里提醒 AI 加 WHERE，但不强制。本项目代码层面拦截，不带 WHERE 直接报错，全表操作要显式传 `allow_all=true`。
- **查询结果上限 1000 行**：原项目查大表会把全部结果塞给 AI，可能撑爆内存或卡死通信管道。本项目限 1000 行，超了自动截断并在结果末尾输出警告，AI 能看到被截断了。

### 其他改进

- `alter_table` 原项目描述里禁止删字段，本项目放开了，支持完整的 ADD/MODIFY/CHANGE/DROP COLUMN
- `SHOW VARIABLES LIKE` 和 `SHOW STATUS LIKE` 用字符串拼接而不是参数化（MySQL 不支持这俩语句参数化）
- 事务 5 分钟自动回滚，防止 AI 开了事务忘了关导致锁表

## 贡献者

<a href="https://github.com/crispvibe"><img src="https://avatars.githubusercontent.com/u/198243778?s=80&v=4" width="56" alt="Anna (@crispvibe)" /></a>

[**@crispvibe**](https://github.com/crispvibe)（Anna）· 维护者

原项目作者 [**@Zhwt**](https://github.com/Zhwt)

欢迎提 Issue 和 PR。

## 开源协议

[MIT](LICENSE) © 2025 Zhwt, 2026 禾屿科技

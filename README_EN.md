<div align="center">

# MySQLMCP

[简体中文](README.md) | **English**

A MySQL database management tool for AI.

[![Go Reference](https://pkg.go.dev/badge/github.com/crispvibe/mysqlmcp.svg)](https://pkg.go.dev/github.com/crispvibe/mysqlmcp)
[![Go Report Card](https://goreportcard.com/badge/github.com/crispvibe/mysqlmcp)](https://goreportcard.com/report/github.com/crispvibe/mysqlmcp)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/crispvibe/mysqlmcp?style=social)](https://github.com/crispvibe/mysqlmcp/stargazers)

*Found it useful? Tap ⭐ to show support.*

</div>

---

## What is this

In one sentence: let AI manage your MySQL database.

You tell AI "create a users table", "show me the members table structure", "delete expired orders" — and AI does it directly. Creating tables, altering columns, querying data, deleting rows, running transactions — no need to write SQL yourself.

Single binary, no Node.js or Python required.

Forked from [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql) with bug fixes and new features.

## What it can do

**Inspect**
- List all databases and tables
- Show table structure: CREATE statement, columns, indexes, constraints
- Show table size, row count, engine
- Show database config and runtime status
- Show running queries

**Query**
- Run SELECT, capped at 1000 rows — warns you to add WHERE if truncated

**Modify data**
- Insert: only INSERT allowed, mixing in DELETE is blocked
- Update: WHERE required by default, full-table needs explicit confirmation
- Delete: WHERE required by default, full-table needs explicit confirmation

**Modify schema**
- Create/drop databases and tables
- Truncate tables
- Add/modify/drop columns, add/drop indexes
- Run DDL, run batch SQL

**Transactions**
- Begin → read/write → commit or rollback
- Auto-rollback after 5 minutes if you forget to commit

**Operations**
- Kill stuck queries

## Safety

- Destructive operations (drop database/table) aren't blocked, but AI will confirm with you first
- UPDATE and DELETE without WHERE are blocked to prevent accidental full-table wipes
- Non-INSERT statements in the INSERT tool are blocked
- Query results over 1000 rows are truncated with a warning
- Read-only mode available for production environments

## Installation

### Download binary

Grab the latest [release](https://github.com/crispvibe/mysqlmcp/releases) for your platform, put it in `$PATH`.

### Build from source

```bash
go install -v github.com/crispvibe/mysqlmcp@latest
```

Or:

```bash
git clone https://github.com/crispvibe/mysqlmcp.git
cd mysqlmcp
go build -o go-mcp-mysql .
```

## Configuration

### Option A: arguments

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

### Option B: DSN

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

> If the binary is not in `$PATH`, use the full path in `command`.

### Optional flags

| Flag | Effect |
|---|---|
| `--read-only` | Read-only mode, no modifications allowed |
| `--with-explain-check` | Check query plan before executing, block full-table scans |

## Tools

**Query (read-only)**: `list_database` / `list_table` / `show_create_table` / `show_columns` / `show_index` / `show_constraints` / `show_table_status` / `show_processlist` / `show_variables` / `show_status` / `explain` / `read_query`

**Data modification**: `write_query` (INSERT) / `update_query` / `delete_query`

**Schema changes**: `create_database` / `drop_database` / `create_table` / `alter_table` / `drop_table` / `truncate_table` / `create_index` / `drop_index` / `execute_ddl` / `execute_migration`

**Transactions**: `begin_transaction` / `transaction_read_query` / `transaction_exec_query` / `commit_transaction` / `rollback_transaction`

**Operations**: `kill_query`

## What's improved over the original

The original [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql) had only 9 tools — no transactions, no index management, no operations diagnostics, no safety guardrails, and a few bugs that could crash the process. This version adds:

### Tools: 9 → 31

| Category | Original | This fork | New |
|---|---|---|---|
| Schema inspection | `desc_table` only | `show_create_table` / `show_columns` / `show_index` / `show_constraints` / `show_table_status` | +4 |
| Query | `read_query` | `read_query` + `explain` | +1 |
| Data modification | `write_query` / `update_query` / `delete_query` | same + guardrails | 0 |
| Schema changes | `create_table` / `alter_table` | + `create_database` / `drop_database` / `drop_table` / `truncate_table` / `create_index` / `drop_index` / `execute_ddl` / `execute_migration` | +8 |
| Transactions | none | `begin` / `read` / `exec` / `commit` / `rollback` | +5 |
| Operations | none | `show_processlist` / `kill_query` / `show_variables` / `show_status` | +4 |

### Bug fixes

The original had several issues that could crash or hang the process:

- **75s hang on dead server**: original DSN had no timeout, so connecting to a dead server waited for OS TCP timeout (~75s). This fork: 10s timeout.
- **Dead connections not recycled**: original connection pool had no lifecycle management, dead connections piled up. This fork: auto-recycle (5min max lifetime, 2min idle timeout).
- **Concurrent crash**: original global DB variable had no mutex, concurrent initialization caused race condition. This fork: added mutex.
- **EXPLAIN crash on NULL**: original panicked when `select_type` was NULL in EXPLAIN output. This fork: null check added.
- **Crash on missing args**: original used naked type assertions for arguments, panicking if missing or wrong type. This fork: safe checks, returns error instead.
- **Errors swallowed**: original `rows.Err()` check was in wrong position, masking real errors when query failed mid-way. This fork: fixed check order.

### Safety guardrails

The original had zero protection on write operations — one AI mistake could wipe a whole table:

- **INSERT tool only allows INSERT**: original `write_query` accepted any statement despite its name. This fork validates statement type, blocks DELETE/UPDATE/DROP.
- **UPDATE/DELETE require WHERE**: original only mentioned WHERE in the description, didn't enforce it. This fork blocks at code level, requires explicit `allow_all=true` for full-table operations.
- **Query results capped at 1000 rows**: original returned everything, risking OOM or pipe deadlock on large tables. This fork caps at 1000 rows with truncation warning visible to AI.

### Other improvements

- `alter_table` now supports full ADD/MODIFY/CHANGE/DROP COLUMN (original description forbade dropping columns)
- `SHOW VARIABLES LIKE` and `SHOW STATUS LIKE` use string concatenation instead of parameterization (MySQL doesn't support parameterization for these)
- Transactions auto-rollback after 5 minutes to prevent AI forgetting to close them and locking tables

## Contributors

<a href="https://github.com/crispvibe"><img src="https://avatars.githubusercontent.com/u/198243778?s=80&v=4" width="56" alt="Anna (@crispvibe)" /></a>

[**@crispvibe**](https://github.com/crispvibe) (Anna) · Maintainer

Original author [**@Zhwt**](https://github.com/Zhwt)

Issues and PRs welcome.

## License

[MIT](LICENSE) © 2025 Zhwt, 2026 禾屿科技

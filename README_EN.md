# MySQLMCP

[简体中文](README.md) | **English**

A MySQL database management tool for AI. Let AI create tables, alter columns, query data, delete rows, run transactions — no SQL writing needed.

Single Go binary, no Node.js or Python required. Forked from [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql).

## Install

```bash
git clone https://github.com/crispvibe/mysqlmcp.git
cd mysqlmcp
go build -o go-mcp-mysql .
```

Put the `go-mcp-mysql` binary wherever you want, e.g. `/usr/local/bin/` or `~/bin/`.

## Configuration

Add this to your AI client's MCP config (Cursor, Windsurf, Claude Desktop, etc.):

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

Or use DSN:

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

> Use the full path in `command`, e.g. `/Users/you/bin/go-mcp-mysql`.

### Optional flags

| Flag | Effect |
|---|---|
| `--read-only` | Read-only mode, no modifications |
| `--with-explain-check` | Check query plan before executing, block full-table scans |

### Let AI set it up for you

Just tell AI:

> Set up a MySQL MCP for me. Repo: https://github.com/crispvibe/mysqlmcp . Clone and build it. Database host xxx, port 3306, user xxx, password xxx, database xxx.

AI will clone, build, and write the config for you.

## What it can do

**Inspect**: list databases/tables, show table structure, columns, indexes, constraints, table size, database config, running queries

**Query**: run SELECT, capped at 1000 rows, warns you to narrow down if truncated

**Modify data**: insert (INSERT only), update (WHERE required), delete (WHERE required)

**Modify schema**: create/drop databases and tables, truncate, add/modify/drop columns, add/drop indexes, run DDL, run batch SQL

**Transactions**: begin → read/write → commit or rollback, auto-rollback after 5 minutes

**Operations**: kill stuck queries

## Safety

- UPDATE/DELETE without WHERE are blocked
- Non-INSERT statements in the INSERT tool are blocked
- Query results over 1000 rows are truncated with warning
- Read-only mode available for production

## Tools (31)

**Query**: `list_database` `list_table` `show_create_table` `show_columns` `show_index` `show_constraints` `show_table_status` `show_processlist` `show_variables` `show_status` `explain` `read_query`

**Data**: `write_query` `update_query` `delete_query`

**Schema**: `create_database` `drop_database` `create_table` `alter_table` `drop_table` `truncate_table` `create_index` `drop_index` `execute_ddl` `execute_migration`

**Transactions**: `begin_transaction` `transaction_read_query` `transaction_exec_query` `commit_transaction` `rollback_transaction`

**Operations**: `kill_query`

## What's improved over the original

The original [Zhwt/go-mcp-mysql](https://github.com/Zhwt/go-mcp-mysql) had 9 tools — no transactions, no index management, no operations, no safety guardrails, and bugs that could crash the process.

### Tools: 9 → 31

| Category | Original | This fork | New |
|---|---|---|---|
| Schema | `desc_table` only | `show_create_table` / `show_columns` / `show_index` / `show_constraints` / `show_table_status` | +4 |
| Query | `read_query` | `read_query` + `explain` | +1 |
| Data | `write_query` / `update_query` / `delete_query` | same + guardrails | 0 |
| Schema changes | `create_table` / `alter_table` | + `create_database` / `drop_database` / `drop_table` / `truncate_table` / `create_index` / `drop_index` / `execute_ddl` / `execute_migration` | +8 |
| Transactions | none | `begin` / `read` / `exec` / `commit` / `rollback` | +5 |
| Operations | none | `show_processlist` / `kill_query` / `show_variables` / `show_status` | +4 |

### Bug fixes

- **75s hang on dead server**: original DSN had no timeout. This fork: 10s.
- **Dead connections not recycled**: original pool had no lifecycle management. This fork: auto-recycle.
- **Concurrent crash**: original global DB variable had no mutex. This fork: added mutex.
- **EXPLAIN crash on NULL**: original panicked on NULL `select_type`. This fork: null check.
- **Crash on missing args**: original used naked type assertions. This fork: safe checks.
- **Errors swallowed**: original `rows.Err()` check in wrong position. This fork: fixed.

### Safety guardrails

- **INSERT tool only allows INSERT**: original accepted any statement. This fork validates type.
- **UPDATE/DELETE require WHERE**: original only mentioned it in description. This fork enforces in code.
- **Query capped at 1000 rows**: original returned everything, risking OOM. This fork caps with warning.

### Other

- `alter_table` supports DROP COLUMN (original forbade it)
- `SHOW VARIABLES/STATUS LIKE` uses string concatenation (MySQL doesn't support parameterization here)
- Transactions auto-rollback after 5 minutes

## License

MIT

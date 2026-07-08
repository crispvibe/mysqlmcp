package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	StatementTypeNoExplainCheck = ""
	StatementTypeSelect         = "SELECT"
	StatementTypeInsert         = "INSERT"
	StatementTypeUpdate         = "UPDATE"
	StatementTypeDelete         = "DELETE"

	transactionTimeout = 5 * time.Minute

	// queryTimeout 限制单次 DB 查询/执行的执行时间，防止长查询无限阻塞 stdio 通道导致 AI 客户端判定连接失败。
	queryTimeout = 30 * time.Second

	// migrationTimeout 限制 execute_migration 的总执行时间，migration 可含多条 SQL，需要比单查询更长的超时。
	migrationTimeout = 5 * time.Minute

	// readQueryMaxRows 限制 read_query 单次返回行数，防止 LLM 拉全表堵死 stdio 管道或 OOM。
	// 0 表示不限。事务内读查询不限制（事务场景由调用方控制）。
	readQueryMaxRows = 1000
)

var (
	Host string
	User string
	Pass string
	Port int
	Db   string

	DSN string

	ReadOnly         bool
	WithExplainCheck bool

	DB *sqlx.DB
	dbMu sync.Mutex

	transactions   = map[string]*ManagedTransaction{}
	transactionsMu sync.Mutex
)

type queryer interface {
	QueryxContext(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error)
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

type ExplainResult struct {
	Id           *string `db:"id"`
	SelectType   *string `db:"select_type"`
	Table        *string `db:"table"`
	Partitions   *string `db:"partitions"`
	Type         *string `db:"type"`
	PossibleKeys *string `db:"possible_keys"`
	Key          *string `db:"key"`
	KeyLen       *string `db:"key_len"`
	Ref          *string `db:"ref"`
	Rows         *string `db:"rows"`
	Filtered     *string `db:"filtered"`
	Extra        *string `db:"Extra"`
}

type ShowCreateTableResult struct {
	Table       string `db:"Table"`
	CreateTable string `db:"Create Table"`
}

type ManagedTransaction struct {
	Tx        *sqlx.Tx
	CreatedAt time.Time
	ExpiresAt time.Time
	Timer     *time.Timer
	mu        sync.Mutex
}

func main() {
	flag.StringVar(&Host, "host", "localhost", "MySQL hostname")
	flag.StringVar(&User, "user", "root", "MySQL username")
	flag.StringVar(&Pass, "pass", "", "MySQL password")
	flag.IntVar(&Port, "port", 3306, "MySQL port")
	flag.StringVar(&Db, "db", "", "MySQL database")

	flag.StringVar(&DSN, "dsn", "", "MySQL DSN")

	flag.BoolVar(&ReadOnly, "read-only", false, "Enable read-only mode")
	flag.BoolVar(&WithExplainCheck, "with-explain-check", false, "Check query plan with `EXPLAIN` before executing")
	flag.Parse()

	if len(DSN) == 0 {
		DSN = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&loc=Local&timeout=10s&readTimeout=30s&writeTimeout=30s", User, Pass, Host, Port, Db)
	}

	s := server.NewMCPServer(
		"go-mcp-mysql",
		"0.1.0",
	)

	// Schema Tools
	listDatabaseTool := mcp.NewTool(
		"list_database",
		mcp.WithDescription("List all databases in the MySQL server"),
		mcp.WithTitleAnnotation("List Databases"),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	createDatabaseTool := mcp.NewTool(
		"create_database",
		mcp.WithDescription("Create a database"),
		mcp.WithTitleAnnotation("Create Database"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name"),
		),
		mcp.WithString("charset",
			mcp.Description("Optional default character set, such as utf8mb4"),
		),
		mcp.WithString("collation",
			mcp.Description("Optional default collation, such as utf8mb4_0900_ai_ci"),
		),
	)

	listTableTool := mcp.NewTool(
		"list_table",
		mcp.WithDescription("List all tables in the MySQL server"),
		mcp.WithTitleAnnotation("List Tables"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
	)

	createTableTool := mcp.NewTool(
		"create_table",
		mcp.WithDescription("Create a new table in the MySQL server. Make sure you have added proper comments for each column and the table itself"),
		mcp.WithTitleAnnotation("Create Table"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to create the table"),
		),
	)

	alterTableTool := mcp.NewTool(
		"alter_table",
		mcp.WithDescription("Alter an existing table (ADD/MODIFY/CHANGE/DROP COLUMN, ADD/DROP INDEX/CONSTRAINT, RENAME). Make sure you have updated comments for each modified column"),
		mcp.WithTitleAnnotation("Alter Table"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to alter the table"),
		),
	)

	createIndexTool := mcp.NewTool(
		"create_index",
		mcp.WithDescription("Create a normal or unique index for a table. For complex expression indexes, use execute_ddl"),
		mcp.WithTitleAnnotation("Create Index"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The index name"),
		),
		mcp.WithString("table",
			mcp.Required(),
			mcp.Description("The table name"),
		),
		mcp.WithString("columns",
			mcp.Required(),
			mcp.Description("Comma-separated column names, e.g. 'email' or 'email, tenant_id'"),
		),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
		mcp.WithBoolean("unique",
			mcp.Description("Whether to create a unique index"),
		),
	)

	dropIndexTool := mcp.NewTool(
		"drop_index",
		mcp.WithDescription("Drop an index from a table"),
		mcp.WithTitleAnnotation("Drop Index"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The index name"),
		),
		mcp.WithString("table",
			mcp.Required(),
			mcp.Description("The table name"),
		),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
	)

	dropDatabaseTool := mcp.NewTool(
		"drop_database",
		mcp.WithDescription("Drop a database. This is irreversible and deletes all tables and data in the database"),
		mcp.WithTitleAnnotation("Drop Database"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The database name to drop"),
		),
		mcp.WithBoolean("if_exists",
			mcp.Description("Use IF EXISTS to avoid error when database does not exist"),
		),
	)

	dropTableTool := mcp.NewTool(
		"drop_table",
		mcp.WithDescription("Drop a table. This is irreversible and deletes all data and structure of the table"),
		mcp.WithTitleAnnotation("Drop Table"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The table name to drop"),
		),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
		mcp.WithBoolean("if_exists",
			mcp.Description("Use IF EXISTS to avoid error when table does not exist"),
		),
	)

	truncateTableTool := mcp.NewTool(
		"truncate_table",
		mcp.WithDescription("Truncate a table: delete all rows but keep the table structure. Faster than DELETE for full-table clears, resets AUTO_INCREMENT, and is non-transactional"),
		mcp.WithTitleAnnotation("Truncate Table"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The table name to truncate"),
		),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
	)

	executeDDLTool := mcp.NewTool(
		"execute_ddl",
		mcp.WithDescription("Execute one DDL statement, such as CREATE, ALTER, DROP, TRUNCATE, or RENAME"),
		mcp.WithTitleAnnotation("Execute DDL"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The DDL SQL statement to execute"),
		),
	)

	executeMigrationTool := mcp.NewTool(
		"execute_migration",
		mcp.WithDescription("Execute multiple semicolon-separated SQL statements in order. Useful for development migrations"),
		mcp.WithTitleAnnotation("Execute Migration"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Semicolon-separated SQL statements"),
		),
		mcp.WithBoolean("transaction",
			mcp.Description("Whether to wrap statements in one transaction (default: false). Avoid this for MySQL DDL because many DDL statements commit implicitly"),
		),
	)

	showCreateTableTool := mcp.NewTool(
		"show_create_table",
		mcp.WithDescription("Show the full CREATE TABLE statement, including indexes, constraints, charset, collation, and comments"),
		mcp.WithTitleAnnotation("Show Create Table"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The name of the table"),
		),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
	)

	showColumnsTool := mcp.NewTool(
		"show_columns",
		mcp.WithDescription("Show full column metadata for a table, including collation, nullability, keys, defaults, extras, privileges, and comments"),
		mcp.WithTitleAnnotation("Show Columns"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The name of the table"),
		),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
	)

	showIndexTool := mcp.NewTool(
		"show_index",
		mcp.WithDescription("Show indexes for a table"),
		mcp.WithTitleAnnotation("Show Index"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The name of the table"),
		),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
	)

	showConstraintsTool := mcp.NewTool(
		"show_constraints",
		mcp.WithDescription("Show primary key, unique, check, and foreign key constraints from information_schema"),
		mcp.WithTitleAnnotation("Show Constraints"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
		mcp.WithString("name",
			mcp.Description("Optional table name filter"),
		),
	)

	showTableStatusTool := mcp.NewTool(
		"show_table_status",
		mcp.WithDescription("Show table status (engine, row count, data size, index size, collation, etc.) for troubleshooting"),
		mcp.WithTitleAnnotation("Show Table Status"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("database",
			mcp.Description("Optional database name. If omitted, use the connection's default database"),
		),
		mcp.WithString("name",
			mcp.Description("Optional table name LIKE pattern, e.g. 'user%' or 'order_%'"),
		),
	)

	showProcesslistTool := mcp.NewTool(
		"show_processlist",
		mcp.WithDescription("Show all active MySQL connections/sessions for troubleshooting long-running queries"),
		mcp.WithTitleAnnotation("Show Processlist"),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	killQueryTool := mcp.NewTool(
		"kill_query",
		mcp.WithDescription("Kill a running query by process ID. Use show_processlist first to find the ID"),
		mcp.WithTitleAnnotation("Kill Query"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithNumber("process_id",
			mcp.Required(),
			mcp.Description("The process ID from SHOW PROCESSLIST"),
		),
	)

	showVariablesTool := mcp.NewTool(
		"show_variables",
		mcp.WithDescription("Show MySQL system variables for troubleshooting configuration (e.g. max_connections, wait_timeout)"),
		mcp.WithTitleAnnotation("Show Variables"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("pattern",
			mcp.Description("Optional LIKE pattern, e.g. 'max_%' or 'timeout'"),
		),
	)

	showStatusTool := mcp.NewTool(
		"show_status",
		mcp.WithDescription("Show MySQL server status counters for troubleshooting performance (e.g. Threads_connected, Slow_queries)"),
		mcp.WithTitleAnnotation("Show Status"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("pattern",
			mcp.Description("Optional LIKE pattern, e.g. 'Threads_%' or 'Slow_%'"),
		),
	)

	explainTool := mcp.NewTool(
		"explain",
		mcp.WithDescription("Run EXPLAIN for a SQL statement and return the query plan"),
		mcp.WithTitleAnnotation("Explain"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to explain"),
		),
	)


	beginTransactionTool := mcp.NewTool(
		"begin_transaction",
		mcp.WithDescription("Begin a transaction and return a tx_id. Transactions expire and rollback automatically after 5 minutes"),
		mcp.WithTitleAnnotation("Begin Transaction"),
		mcp.WithDestructiveHintAnnotation(!ReadOnly),
	)

	transactionReadQueryTool := mcp.NewTool(
		"transaction_read_query",
		mcp.WithDescription("Execute a read query inside an open transaction (no row limit, unlike read_query)"),
		mcp.WithTitleAnnotation("Transaction Read Query"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("tx_id",
			mcp.Required(),
			mcp.Description("The transaction id returned by begin_transaction"),
		),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to execute"),
		),
	)

	transactionExecQueryTool := mcp.NewTool(
		"transaction_exec_query",
		mcp.WithDescription("Execute a write or DDL statement inside an open transaction"),
		mcp.WithTitleAnnotation("Transaction Exec Query"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("tx_id",
			mcp.Required(),
			mcp.Description("The transaction id returned by begin_transaction"),
		),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL statement to execute"),
		),
	)

	commitTransactionTool := mcp.NewTool(
		"commit_transaction",
		mcp.WithDescription("Commit an open transaction by tx_id"),
		mcp.WithTitleAnnotation("Commit Transaction"),
		mcp.WithDestructiveHintAnnotation(!ReadOnly),
		mcp.WithString("tx_id",
			mcp.Required(),
			mcp.Description("The transaction id returned by begin_transaction"),
		),
	)

	rollbackTransactionTool := mcp.NewTool(
		"rollback_transaction",
		mcp.WithDescription("Rollback an open transaction by tx_id"),
		mcp.WithTitleAnnotation("Rollback Transaction"),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString("tx_id",
			mcp.Required(),
			mcp.Description("The transaction id returned by begin_transaction"),
		),
	)

	// Data Tools
	readQueryTool := mcp.NewTool(
		"read_query",
		mcp.WithDescription("Execute a read-only SQL query. Results are capped at 1000 rows; use LIMIT/WHERE to narrow down. Call `show_create_table` first if you need table structure"),
		mcp.WithTitleAnnotation("Read Query"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to execute"),
		),
	)

	writeQueryTool := mcp.NewTool(
		"write_query",
		mcp.WithDescription("Execute an INSERT statement. For UPDATE use update_query, for DELETE use delete_query. Make sure the data types match the columns' definitions"),
		mcp.WithTitleAnnotation("Write Query"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to execute"),
		),
	)

	updateQueryTool := mcp.NewTool(
		"update_query",
		mcp.WithDescription("Execute an update SQL query. WHERE is required by default. Set allow_all=true only for intentional full-table updates"),
		mcp.WithTitleAnnotation("Update Query"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to execute"),
		),
		mcp.WithBoolean("allow_all",
			mcp.Description("Allow UPDATE without WHERE for intentional full-table updates"),
		),
	)

	deleteQueryTool := mcp.NewTool(
		"delete_query",
		mcp.WithDescription("Execute a delete SQL query. WHERE is required by default. Set allow_all=true only for intentional full-table deletes"),
		mcp.WithTitleAnnotation("Delete Query"),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to execute"),
		),
		mcp.WithBoolean("allow_all",
			mcp.Description("Allow DELETE without WHERE for intentional full-table deletes"),
		),
	)

	s.AddTool(listDatabaseTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := HandleQuery(ctx, "SHOW DATABASES", StatementTypeNoExplainCheck)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	if !ReadOnly {
		s.AddTool(createDatabaseTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := RequiredStringArg(request, "name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleCreateDatabase(ctx, name, OptionalStringArg(request, "charset"), OptionalStringArg(request, "collation"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	s.AddTool(listTableTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := OptionalStringArg(request, "database")
		result, err := HandleListTables(ctx, database)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	if !ReadOnly {
		s.AddTool(createTableTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result, err := HandleExec(ctx, query, StatementTypeNoExplainCheck)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	if !ReadOnly {
		s.AddTool(alterTableTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result, err := HandleExec(ctx, query, StatementTypeNoExplainCheck)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))

		s.AddTool(createIndexTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := RequiredStringArg(request, "name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			table, err := RequiredStringArg(request, "table")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			columns, err := RequiredStringArg(request, "columns")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleCreateIndex(ctx, name, table, columns, OptionalStringArg(request, "database"), OptionalBoolArg(request, "unique"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))

		s.AddTool(dropIndexTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := RequiredStringArg(request, "name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			table, err := RequiredStringArg(request, "table")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleDropIndex(ctx, name, table, OptionalStringArg(request, "database"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))

		s.AddTool(dropDatabaseTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := RequiredStringArg(request, "name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleDropDatabase(ctx, name, OptionalBoolArg(request, "if_exists"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))

		s.AddTool(dropTableTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := RequiredStringArg(request, "name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleDropTable(ctx, name, OptionalStringArg(request, "database"), OptionalBoolArg(request, "if_exists"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))

		s.AddTool(truncateTableTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			name, err := RequiredStringArg(request, "name")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleTruncateTable(ctx, name, OptionalStringArg(request, "database"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))

		s.AddTool(executeDDLTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleExec(ctx, query, StatementTypeNoExplainCheck)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))

		s.AddTool(executeMigrationTool, wrapHandlerWithTimeout(migrationTimeout, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleExecuteMigration(ctx, query, OptionalBoolArg(request, "transaction"))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	s.AddTool(showCreateTableTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := RequiredStringArg(request, "name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		database := OptionalStringArg(request, "database")
		result, err := HandleShowCreateTable(ctx, name, database)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(showColumnsTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := RequiredStringArg(request, "name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		database := OptionalStringArg(request, "database")
		result, err := HandleShowColumns(ctx, name, database)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(showIndexTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := RequiredStringArg(request, "name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		database := OptionalStringArg(request, "database")
		result, err := HandleShowIndex(ctx, name, database)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(showConstraintsTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := OptionalStringArg(request, "database")
		name := OptionalStringArg(request, "name")
		result, err := HandleShowConstraints(ctx, name, database)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(showTableStatusTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		database := OptionalStringArg(request, "database")
		name := OptionalStringArg(request, "name")
		result, err := HandleShowTableStatus(ctx, name, database)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(showProcesslistTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := HandleShowProcesslist(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	if !ReadOnly {
		s.AddTool(killQueryTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			args := request.GetArguments()
			processID, ok := args["process_id"]
			if !ok {
				return mcp.NewToolResultError("required argument \"process_id\" not found"), nil
			}
			pid, ok := toInt(processID)
			if !ok {
				return mcp.NewToolResultError("argument \"process_id\" is not a valid integer"), nil
			}
			result, err := HandleKillQuery(ctx, pid)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	s.AddTool(showVariablesTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pattern := OptionalStringArg(request, "pattern")
		result, err := HandleShowVariables(ctx, pattern)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(showStatusTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		pattern := OptionalStringArg(request, "pattern")
		result, err := HandleShowStatus(ctx, pattern)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(explainTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := RequiredStringArg(request, "query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := HandleQuery(ctx, fmt.Sprintf("EXPLAIN %s", query), StatementTypeNoExplainCheck)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(beginTransactionTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := HandleBeginTransaction(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(transactionReadQueryTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		txID, err := RequiredStringArg(request, "tx_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		query, err := RequiredStringArg(request, "query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := HandleTransactionQuery(ctx, txID, query)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	if !ReadOnly {
		s.AddTool(transactionExecQueryTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			txID, err := RequiredStringArg(request, "tx_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleTransactionExec(ctx, txID, query)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	s.AddTool(commitTransactionTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		txID, err := RequiredStringArg(request, "tx_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := HandleCommitTransaction(txID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(rollbackTransactionTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		txID, err := RequiredStringArg(request, "tx_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := HandleRollbackTransaction(txID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	s.AddTool(readQueryTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := RequiredStringArg(request, "query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if err := ValidateReadQuery(query); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := HandleQueryWithLimit(ctx, query, StatementTypeSelect, readQueryMaxRows)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	}))

	if !ReadOnly {
		s.AddTool(writeQueryTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := ValidateWriteGuard(query, StatementTypeInsert, true); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			result, err := HandleExec(ctx, query, StatementTypeInsert)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	if !ReadOnly {
		s.AddTool(updateQueryTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := ValidateWriteGuard(query, StatementTypeUpdate, OptionalBoolArg(request, "allow_all")); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleExec(ctx, query, StatementTypeUpdate)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	if !ReadOnly {
		s.AddTool(deleteQueryTool, wrapHandler(func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := RequiredStringArg(request, "query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if err := ValidateWriteGuard(query, StatementTypeDelete, OptionalBoolArg(request, "allow_all")); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			result, err := HandleExec(ctx, query, StatementTypeDelete)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}

			return mcp.NewToolResultText(result), nil
		}))
	}

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// wrapHandler 为每个 tool handler 套上超时和 panic 兜底：
// - 用 queryTimeout 限制单次 DB 操作执行时间，防止长查询无限阻塞 stdio 通道导致 AI 客户端判定连接失败；
// - recover 捕获 panic 转成错误结果返回，防止 panic 杀掉整个进程。
func wrapHandler(handler func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)) func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return wrapHandlerWithTimeout(queryTimeout, handler)
}

// wrapHandlerWithTimeout 同 wrapHandler 但允许自定义超时，供 execute_migration 等需要更长执行时间的工具使用。
func wrapHandlerWithTimeout(timeout time.Duration, handler func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)) func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, request mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		defer func() {
			if r := recover(); r != nil {
				result = mcp.NewToolResultError(fmt.Sprintf("internal error: %v", r))
				err = nil
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return handler(ctx, request)
	}
}

func GetDB() (*sqlx.DB, error) {
	dbMu.Lock()
	defer dbMu.Unlock()

	if DB != nil {
		return DB, nil
	}

	db, err := sqlx.Connect("mysql", DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to establish database connection: %v", err)
	}

	// 连接池生命周期管理：远程 MySQL 连接会被服务端 wait_timeout 或中间网络设备
	// 静默关闭，不设这些参数会导致连接池里残留死连接，复用时卡住直到 OS TCP 超时。
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	DB = db

	return DB, nil
}

func HandleQuery(ctx context.Context, query, expect string, args ...interface{}) (string, error) {
	return HandleQueryWithLimit(ctx, query, expect, 0, args...)
}

// HandleQueryWithLimit 同 HandleQuery 但限制返回行数，maxRows=0 表示不限。
func HandleQueryWithLimit(ctx context.Context, query, expect string, maxRows int, args ...interface{}) (string, error) {
	result, headers, truncated, err := DoQuery(ctx, query, expect, maxRows, args...)
	if err != nil {
		return "", err
	}

	s, err := MapToCSV(result, headers)
	if err != nil {
		return "", err
	}

	if truncated {
		s += fmt.Sprintf("\n# WARNING: result truncated at %d rows, use LIMIT/WHERE to narrow down", maxRows)
	}

	return s, nil
}

func DoQuery(ctx context.Context, query, expect string, maxRows int, args ...interface{}) ([]map[string]interface{}, []string, bool, error) {
	db, err := GetDB()
	if err != nil {
		return nil, nil, false, err
	}

	if len(expect) > 0 {
		if err := HandleExplain(ctx, query, expect); err != nil {
			return nil, nil, false, err
		}
	}

	return DoQueryWithExecutor(ctx, db, query, maxRows, args...)
}

func DoQueryWithExecutor(ctx context.Context, q queryer, query string, maxRows int, args ...interface{}) ([]map[string]interface{}, []string, bool, error) {
	rows, err := q.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}

	result := []map[string]interface{}{}
	rowCount := 0
	truncated := false
	for rows.Next() {
		if maxRows > 0 && rowCount >= maxRows {
			truncated = true
			break
		}
		row, err := rows.SliceScan()
		if err != nil {
			return nil, nil, false, err
		}

		resultRow := map[string]interface{}{}
		for i, col := range cols {
			switch v := row[i].(type) {
			case []byte:
				resultRow[col] = string(v)
			default:
				resultRow[col] = v
			}
		}
		result = append(result, resultRow)
		rowCount++
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}

	return result, cols, truncated, nil
}

func HandleExec(ctx context.Context, query, expect string) (string, error) {
	db, err := GetDB()
	if err != nil {
		return "", err
	}

	if len(expect) > 0 {
		if err := HandleExplain(ctx, query, expect); err != nil {
			return "", err
		}
	}

	result, err := db.ExecContext(ctx, query)
	if err != nil {
		return "", err
	}

	return FormatExecResult(result, expect)
}

func FormatExecResult(result sql.Result, expect string) (string, error) {
	ra, err := result.RowsAffected()
	if err != nil {
		return "", err
	}

	switch expect {
	case StatementTypeInsert:
		li, err := result.LastInsertId()
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("%d rows affected, last insert id: %d", ra, li), nil
	default:
		return fmt.Sprintf("%d rows affected", ra), nil
	}
}

func HandleExplain(ctx context.Context, query, expect string) error {
	if !WithExplainCheck {
		return nil
	}

	db, err := GetDB()
	if err != nil {
		return err
	}

	rows, err := db.QueryxContext(ctx, fmt.Sprintf("EXPLAIN %s", query))
	if err != nil {
		return err
	}
	defer rows.Close()

	result := []ExplainResult{}
	for rows.Next() {
		var row ExplainResult
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if len(result) != 1 {
		return fmt.Errorf("unable to check query plan, denied")
	}

	match := false
	switch expect {
	case StatementTypeInsert:
		fallthrough
	case StatementTypeUpdate:
		fallthrough
	case StatementTypeDelete:
		if result[0].SelectType == nil {
			return fmt.Errorf("unable to check query plan: select_type is NULL, denied")
		}
		if *result[0].SelectType == expect {
			match = true
		}
	default:
		// for SELECT type query, the select_type will be multiple values
		// here we check if it's not INSERT, UPDATE or DELETE
		if result[0].SelectType == nil {
			return fmt.Errorf("unable to check query plan: select_type is NULL, denied")
		}
		match = true
		for _, typ := range []string{StatementTypeInsert, StatementTypeUpdate, StatementTypeDelete} {
			if *result[0].SelectType == typ {
				match = false
				break
			}
		}
	}

	if !match {
		return fmt.Errorf("query plan does not match expected pattern, denied")
	}

	return nil
}

func HandleListTables(ctx context.Context, database string) (string, error) {
	query := "SHOW TABLES"
	if strings.TrimSpace(database) != "" {
		query = fmt.Sprintf("SHOW TABLES FROM %s", QuoteIdentifier(database))
	}

	return HandleQuery(ctx, query, StatementTypeNoExplainCheck)
}

func HandleShowCreateTable(ctx context.Context, name, database string) (string, error) {
	db, err := GetDB()
	if err != nil {
		return "", err
	}

	tableName, err := QualifiedTableName(name, database)
	if err != nil {
		return "", err
	}

	rows, err := db.QueryxContext(ctx, fmt.Sprintf("SHOW CREATE TABLE %s", tableName))
	if err != nil {
		return "", err
	}
	defer rows.Close()

	result := []ShowCreateTableResult{}
	for rows.Next() {
		var row ShowCreateTableResult
		if err := rows.StructScan(&row); err != nil {
			return "", err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	if len(result) == 0 {
		return "", fmt.Errorf("table %s does not exist", name)
	}

	return result[0].CreateTable, nil
}

func HandleShowColumns(ctx context.Context, name, database string) (string, error) {
	tableName, err := QualifiedTableName(name, database)
	if err != nil {
		return "", err
	}

	return HandleQuery(ctx, fmt.Sprintf("SHOW FULL COLUMNS FROM %s", tableName), StatementTypeNoExplainCheck)
}

func HandleShowIndex(ctx context.Context, name, database string) (string, error) {
	tableName, err := QualifiedTableName(name, database)
	if err != nil {
		return "", err
	}

	return HandleQuery(ctx, fmt.Sprintf("SHOW INDEX FROM %s", tableName), StatementTypeNoExplainCheck)
}

func HandleShowConstraints(ctx context.Context, name, database string) (string, error) {
	query := `
SELECT
	tc.CONSTRAINT_SCHEMA,
	tc.TABLE_NAME,
	tc.CONSTRAINT_NAME,
	tc.CONSTRAINT_TYPE,
	kcu.COLUMN_NAME,
	kcu.ORDINAL_POSITION,
	kcu.REFERENCED_TABLE_SCHEMA,
	kcu.REFERENCED_TABLE_NAME,
	kcu.REFERENCED_COLUMN_NAME,
	rc.UPDATE_RULE,
	rc.DELETE_RULE
FROM information_schema.TABLE_CONSTRAINTS tc
LEFT JOIN information_schema.KEY_COLUMN_USAGE kcu
	ON tc.CONSTRAINT_SCHEMA = kcu.CONSTRAINT_SCHEMA
	AND tc.TABLE_NAME = kcu.TABLE_NAME
	AND tc.CONSTRAINT_NAME = kcu.CONSTRAINT_NAME
LEFT JOIN information_schema.REFERENTIAL_CONSTRAINTS rc
	ON tc.CONSTRAINT_SCHEMA = rc.CONSTRAINT_SCHEMA
	AND tc.CONSTRAINT_NAME = rc.CONSTRAINT_NAME
WHERE tc.CONSTRAINT_SCHEMA = COALESCE(NULLIF(?, ''), DATABASE())`
	args := []interface{}{strings.TrimSpace(database)}
	if strings.TrimSpace(name) != "" {
		query += " AND tc.TABLE_NAME = ?"
		args = append(args, strings.TrimSpace(name))
	}
	query += " ORDER BY tc.TABLE_NAME, tc.CONSTRAINT_NAME, kcu.ORDINAL_POSITION"

	return HandleQuery(ctx, query, StatementTypeNoExplainCheck, args...)
}

func HandleShowTableStatus(ctx context.Context, name, database string) (string, error) {
	query := "SHOW TABLE STATUS"
	if strings.TrimSpace(database) != "" {
		query = fmt.Sprintf("SHOW TABLE STATUS FROM %s", QuoteIdentifier(database))
	}
	if strings.TrimSpace(name) != "" {
		// SHOW TABLE STATUS 不支持参数化占位符，用 QuoteStringLiteral 转义防注入
		query += fmt.Sprintf(" LIKE %s", QuoteStringLiteral(strings.TrimSpace(name)))
	}

	return HandleQuery(ctx, query, StatementTypeNoExplainCheck)
}

func HandleShowProcesslist(ctx context.Context) (string, error) {
	return HandleQuery(ctx, "SHOW FULL PROCESSLIST", StatementTypeNoExplainCheck)
}

func HandleKillQuery(ctx context.Context, processID int) (string, error) {
	if processID <= 0 {
		return "", fmt.Errorf("process_id must be a positive integer")
	}
	return HandleExec(ctx, fmt.Sprintf("KILL %d", processID), StatementTypeNoExplainCheck)
}

func HandleShowVariables(ctx context.Context, pattern string) (string, error) {
	query := "SHOW VARIABLES"
	if strings.TrimSpace(pattern) != "" {
		// SHOW VARIABLES 不支持参数化占位符，用 QuoteStringLiteral 转义防注入
		query += fmt.Sprintf(" LIKE %s", QuoteStringLiteral(strings.TrimSpace(pattern)))
	}
	return HandleQuery(ctx, query, StatementTypeNoExplainCheck)
}

func HandleShowStatus(ctx context.Context, pattern string) (string, error) {
	query := "SHOW STATUS"
	if strings.TrimSpace(pattern) != "" {
		query += fmt.Sprintf(" LIKE %s", QuoteStringLiteral(strings.TrimSpace(pattern)))
	}
	return HandleQuery(ctx, query, StatementTypeNoExplainCheck)
}

func HandleCreateDatabase(ctx context.Context, name, charset, collation string) (string, error) {
	parts := []string{"CREATE DATABASE", QuoteIdentifier(name)}
	if strings.TrimSpace(charset) != "" {
		if !IsSafeSQLToken(charset) {
			return "", fmt.Errorf("charset contains unsupported characters")
		}
		parts = append(parts, "DEFAULT CHARACTER SET", strings.TrimSpace(charset))
	}
	if strings.TrimSpace(collation) != "" {
		if !IsSafeSQLToken(collation) {
			return "", fmt.Errorf("collation contains unsupported characters")
		}
		parts = append(parts, "DEFAULT COLLATE", strings.TrimSpace(collation))
	}

	return HandleExec(ctx, strings.Join(parts, " "), StatementTypeNoExplainCheck)
}

func HandleCreateIndex(ctx context.Context, name, table, columns, database string, unique bool) (string, error) {
	tableName, err := QualifiedTableName(table, database)
	if err != nil {
		return "", err
	}

	quotedColumns, err := QuoteColumnList(columns)
	if err != nil {
		return "", err
	}

	indexType := "INDEX"
	if unique {
		indexType = "UNIQUE INDEX"
	}
	query := fmt.Sprintf("CREATE %s %s ON %s (%s)", indexType, QuoteIdentifier(name), tableName, quotedColumns)

	return HandleExec(ctx, query, StatementTypeNoExplainCheck)
}

func HandleDropIndex(ctx context.Context, name, table, database string) (string, error) {
	tableName, err := QualifiedTableName(table, database)
	if err != nil {
		return "", err
	}

	return HandleExec(ctx, fmt.Sprintf("DROP INDEX %s ON %s", QuoteIdentifier(name), tableName), StatementTypeNoExplainCheck)
}

func HandleDropDatabase(ctx context.Context, name string, ifExists bool) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("database name is required")
	}
	query := fmt.Sprintf("DROP DATABASE %s %s", ifExistsClause(ifExists), QuoteIdentifier(name))
	return HandleExec(ctx, query, StatementTypeNoExplainCheck)
}

func HandleDropTable(ctx context.Context, name, database string, ifExists bool) (string, error) {
	tableName, err := QualifiedTableName(name, database)
	if err != nil {
		return "", err
	}
	query := fmt.Sprintf("DROP TABLE %s %s", ifExistsClause(ifExists), tableName)
	return HandleExec(ctx, query, StatementTypeNoExplainCheck)
}

func HandleTruncateTable(ctx context.Context, name, database string) (string, error) {
	tableName, err := QualifiedTableName(name, database)
	if err != nil {
		return "", err
	}
	return HandleExec(ctx, fmt.Sprintf("TRUNCATE TABLE %s", tableName), StatementTypeNoExplainCheck)
}

func ifExistsClause(ifExists bool) string {
	if ifExists {
		return "IF EXISTS"
	}
	return ""
}

func HandleExecuteMigration(ctx context.Context, query string, useTransaction bool) (string, error) {
	statements, err := SplitSQLStatements(query)
	if err != nil {
		return "", err
	}
	if len(statements) == 0 {
		return "", fmt.Errorf("migration has no executable statements")
	}

	db, err := GetDB()
	if err != nil {
		return "", err
	}

	var output strings.Builder
	if useTransaction {
		tx, err := db.BeginTxx(ctx, nil)
		if err != nil {
			return "", err
		}
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback()
			}
		}()

		for i, statement := range statements {
			result, err := ExecuteStatement(ctx, tx, statement)
			if err != nil {
				return "", fmt.Errorf("statement %d failed: %w", i+1, err)
			}
			WriteMigrationResult(&output, i+1, statement, result)
		}
		if err := tx.Commit(); err != nil {
			return "", err
		}
		committed = true
		return output.String(), nil
	}

	for i, statement := range statements {
		result, err := ExecuteStatement(ctx, db, statement)
		if err != nil {
			return "", fmt.Errorf("statement %d failed: %w", i+1, err)
		}
		WriteMigrationResult(&output, i+1, statement, result)
	}

	return output.String(), nil
}

func ExecuteStatement(ctx context.Context, e execer, statement string) (string, error) {
	result, err := e.ExecContext(ctx, statement)
	if err != nil {
		return "", err
	}

	return FormatExecResult(result, StatementTypeNoExplainCheck)
}

func WriteMigrationResult(output *strings.Builder, index int, statement, result string) {
	if output.Len() > 0 {
		output.WriteString("\n")
	}
	output.WriteString(fmt.Sprintf("%d. %s\n%s", index, OneLineSQL(statement), result))
}

func OneLineSQL(statement string) string {
	return strings.Join(strings.Fields(statement), " ")
}

func HandleBeginTransaction(ctx context.Context) (string, error) {
	db, err := GetDB()
	if err != nil {
		return "", err
	}

	// 事务需要跨多个请求存活，不能用 wrapHandler 的超时 ctx——handler 返回后 ctx 会被 cancel，
	// 导致 database/sql 的 Tx 被自动回滚。用 context.Background() 创建事务，
	// Begin 操作本身很快（START TRANSACTION），连接建立有 DSN timeout 兜底，
	// 事务生命周期由 transactionTimeout（5 分钟）管理。
	tx, err := db.BeginTxx(context.Background(), nil)
	if err != nil {
		return "", err
	}

	txID, err := NewTransactionID()
	if err != nil {
		_ = tx.Rollback()
		return "", err
	}

	now := time.Now()
	mt := &ManagedTransaction{
		Tx:        tx,
		CreatedAt: now,
		ExpiresAt: now.Add(transactionTimeout),
	}
	mt.Timer = time.AfterFunc(transactionTimeout, func() {
		transactionsMu.Lock()
		current := transactions[txID]
		if current == mt {
			delete(transactions, txID)
		}
		transactionsMu.Unlock()

		if current == mt {
			mt.mu.Lock()
			defer mt.mu.Unlock()
			_ = mt.Tx.Rollback()
		}
	})

	transactionsMu.Lock()
	transactions[txID] = mt
	transactionsMu.Unlock()

	return fmt.Sprintf("tx_id: %s, expires_at: %s", txID, mt.ExpiresAt.Format(time.RFC3339)), nil
}

func HandleTransactionQuery(ctx context.Context, txID, query string) (string, error) {
	mt, err := GetTransaction(txID)
	if err != nil {
		return "", err
	}

	mt.mu.Lock()
	defer mt.mu.Unlock()

	if time.Now().After(mt.ExpiresAt) {
		return "", fmt.Errorf("transaction %s has expired", txID)
	}

	result, headers, _, err := DoQueryWithExecutor(ctx, mt.Tx, query, 0)
	if err != nil {
		return "", err
	}

	return MapToCSV(result, headers)
}

func HandleTransactionExec(ctx context.Context, txID, query string) (string, error) {
	if IsImplicitCommitStatement(query) {
		return "", fmt.Errorf("transaction_exec_query does not allow statements that can cause implicit commit")
	}

	mt, err := GetTransaction(txID)
	if err != nil {
		return "", err
	}

	mt.mu.Lock()
	defer mt.mu.Unlock()

	if time.Now().After(mt.ExpiresAt) {
		return "", fmt.Errorf("transaction %s has expired", txID)
	}

	result, err := mt.Tx.ExecContext(ctx, query)
	if err != nil {
		return "", err
	}

	return FormatExecResult(result, StatementTypeNoExplainCheck)
}

func HandleCommitTransaction(txID string) (string, error) {
	mt, err := RemoveTransaction(txID)
	if err != nil {
		return "", err
	}

	mt.mu.Lock()
	defer mt.mu.Unlock()

	if err := mt.Tx.Commit(); err != nil {
		return "", err
	}

	return "transaction committed", nil
}

func HandleRollbackTransaction(txID string) (string, error) {
	mt, err := RemoveTransaction(txID)
	if err != nil {
		return "", err
	}

	mt.mu.Lock()
	defer mt.mu.Unlock()

	if err := mt.Tx.Rollback(); err != nil {
		return "", err
	}

	return "transaction rolled back", nil
}

func RequiredStringArg(request mcp.CallToolRequest, name string) (string, error) {
	args := request.GetArguments()
	if args == nil {
		return "", fmt.Errorf("required argument %q not found", name)
	}

	value, ok := args[name]
	if !ok {
		return "", fmt.Errorf("required argument %q not found", name)
	}

	s, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("argument %q is not a string", name)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("argument %q is empty", name)
	}

	return s, nil
}

func OptionalStringArg(request mcp.CallToolRequest, name string) string {
	args := request.GetArguments()
	if args == nil {
		return ""
	}

	value, ok := args[name]
	if !ok {
		return ""
	}

	s, ok := value.(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(s)
}

func OptionalBoolArg(request mcp.CallToolRequest, name string) bool {
	args := request.GetArguments()
	if args == nil {
		return false
	}

	value, ok := args[name]
	if !ok {
		return false
	}

	v, ok := value.(bool)
	if !ok {
		return false
	}

	return v
}

// toInt 把 MCP 参数（可能是 float64/json.Number/int）安全转成 int。
func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func ValidateReadQuery(query string) error {
	verb := FirstSQLVerb(query)
	switch verb {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN":
		return nil
	default:
		return fmt.Errorf("read_query only allows SELECT, SHOW, DESCRIBE, DESC, or EXPLAIN statements")
	}
}

func ValidateWriteGuard(query, expect string, allowAll bool) error {
	verb := FirstSQLVerb(query)
	if verb != expect {
		return fmt.Errorf("%s_query only allows %s statements", strings.ToLower(expect), expect)
	}
	if allowAll {
		return nil
	}
	if !HasSQLKeyword(query, "WHERE") {
		return fmt.Errorf("%s without WHERE is denied; set allow_all=true for intentional full-table operation", expect)
	}

	return nil
}

func FirstSQLVerb(query string) string {
	cleaned := strings.TrimSpace(StripSQLComments(query))
	if cleaned == "" {
		return ""
	}

	fields := strings.Fields(cleaned)
	if len(fields) == 0 {
		return ""
	}

	return strings.ToUpper(fields[0])
}

func HasSQLKeyword(query, keyword string) bool {
	cleaned := StripSQLComments(query)
	keyword = strings.ToUpper(keyword)
	for _, token := range SQLTokens(cleaned) {
		if token == keyword {
			return true
		}
	}

	return false
}

func SQLTokens(query string) []string {
	tokens := []string{}
	var current strings.Builder
	for _, r := range query {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			current.WriteRune(r)
			continue
		}
		if current.Len() > 0 {
			tokens = append(tokens, strings.ToUpper(current.String()))
			current.Reset()
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, strings.ToUpper(current.String()))
	}

	return tokens
}

func StripSQLComments(query string) string {
	var out strings.Builder
	var quote rune
	lineComment := false
	blockComment := false
	escaped := false

	for i, r := range query {
		var next rune
		if i+1 < len(query) {
			next = rune(query[i+1])
		}

		if lineComment {
			if r == '\n' {
				lineComment = false
				out.WriteRune(' ')
			}
			continue
		}

		if blockComment {
			if r == '/' && i > 0 && query[i-1] == '*' {
				blockComment = false
				out.WriteRune(' ')
			}
			continue
		}

		if quote != 0 {
			out.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' && quote != '`' {
				escaped = true
				continue
			}
			if r == quote {
				quote = 0
			}
			continue
		}

		if r == '-' && next == '-' {
			lineComment = true
			continue
		}
		if r == '#' {
			lineComment = true
			continue
		}
		if r == '/' && next == '*' {
			blockComment = true
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			quote = r
			out.WriteRune(r)
			continue
		}

		out.WriteRune(r)
	}

	return out.String()
}

func QuoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(strings.TrimSpace(name), "`", "``") + "`"
}

func QuoteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func QualifiedTableName(name, database string) (string, error) {
	name = strings.TrimSpace(name)
	database = strings.TrimSpace(database)
	if name == "" {
		return "", fmt.Errorf("table name is empty")
	}
	if database == "" {
		return QuoteIdentifier(name), nil
	}

	return fmt.Sprintf("%s.%s", QuoteIdentifier(database), QuoteIdentifier(name)), nil
}

func QuoteColumnList(columns string) (string, error) {
	parts := strings.Split(columns, ",")
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		column := strings.TrimSpace(part)
		if column == "" {
			continue
		}
		quoted = append(quoted, QuoteIdentifier(column))
	}
	if len(quoted) == 0 {
		return "", fmt.Errorf("columns is empty")
	}

	return strings.Join(quoted, ", "), nil
}

func IsSafeSQLToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' {
			continue
		}
		return false
	}

	return true
}

func SplitSQLStatements(query string) ([]string, error) {
	var statements []string
	var current strings.Builder
	var quote rune
	lineComment := false
	blockComment := false
	escaped := false

	for i, r := range query {
		var next rune
		if i+1 < len(query) {
			next = rune(query[i+1])
		}

		if lineComment {
			current.WriteRune(r)
			if r == '\n' {
				lineComment = false
			}
			continue
		}

		if blockComment {
			current.WriteRune(r)
			if r == '*' && next == '/' {
				continue
			}
			if r == '/' && i > 0 && query[i-1] == '*' {
				blockComment = false
			}
			continue
		}

		if quote != 0 {
			current.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' && quote != '`' {
				escaped = true
				continue
			}
			if r == quote {
				quote = 0
			}
			continue
		}

		if r == '-' && next == '-' {
			lineComment = true
			current.WriteRune(r)
			continue
		}
		if r == '#' {
			lineComment = true
			current.WriteRune(r)
			continue
		}
		if r == '/' && next == '*' {
			blockComment = true
			current.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' || r == '`' {
			quote = r
			current.WriteRune(r)
			continue
		}
		if r == ';' {
			statement := strings.TrimSpace(current.String())
			if statement != "" {
				statements = append(statements, statement)
			}
			current.Reset()
			continue
		}

		current.WriteRune(r)
	}

	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted string in migration SQL")
	}
	if blockComment {
		return nil, fmt.Errorf("unterminated block comment in migration SQL")
	}

	statement := strings.TrimSpace(current.String())
	if statement != "" {
		statements = append(statements, statement)
	}

	return statements, nil
}

func NewTransactionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return hex.EncodeToString(buf), nil
}

func GetTransaction(txID string) (*ManagedTransaction, error) {
	transactionsMu.Lock()
	defer transactionsMu.Unlock()

	mt, ok := transactions[txID]
	if !ok {
		return nil, fmt.Errorf("transaction %s does not exist or has expired", txID)
	}

	return mt, nil
}

func RemoveTransaction(txID string) (*ManagedTransaction, error) {
	transactionsMu.Lock()
	defer transactionsMu.Unlock()

	mt, ok := transactions[txID]
	if !ok {
		return nil, fmt.Errorf("transaction %s does not exist or has expired", txID)
	}

	delete(transactions, txID)
	if mt.Timer != nil {
		mt.Timer.Stop()
	}

	return mt, nil
}

func IsImplicitCommitStatement(query string) bool {
	fields := strings.Fields(strings.TrimSpace(query))
	if len(fields) == 0 {
		return false
	}

	switch strings.ToUpper(fields[0]) {
	case "ALTER", "CREATE", "DROP", "RENAME", "TRUNCATE", "GRANT", "REVOKE", "LOCK", "UNLOCK":
		return true
	default:
		return false
	}
}

func MapToCSV(m []map[string]interface{}, headers []string) (string, error) {
	var csvBuf strings.Builder
	writer := csv.NewWriter(&csvBuf)

	if err := writer.Write(headers); err != nil {
		return "", fmt.Errorf("failed to write headers: %v", err)
	}

	for _, item := range m {
		row := make([]string, len(headers))
		for i, header := range headers {
			value, exists := item[header]
			if !exists {
				return "", fmt.Errorf("key '%s' not found in map", header)
			}
			row[i] = fmt.Sprintf("%v", value)
		}
		if err := writer.Write(row); err != nil {
			return "", fmt.Errorf("failed to write row: %v", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("error flushing CSV writer: %v", err)
	}

	return csvBuf.String(), nil
}

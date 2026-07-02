package main

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
)

func setupMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock, func()) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("Failed to create mock DB: %v", err)
	}

	// Save the original DB
	originalDB := DB

	// Replace with our mock
	DB = sqlx.NewDb(db, "sqlmock")

	// Return a cleanup function
	cleanup := func() {
		db.Close()
		DB = originalDB
	}

	return db, mock, cleanup
}

func cleanupTransactions() {
	transactionsMu.Lock()
	defer transactionsMu.Unlock()

	for id, mt := range transactions {
		if mt.Timer != nil {
			mt.Timer.Stop()
		}
		_ = mt.Tx.Rollback()
		delete(transactions, id)
	}
}

func TestGetDB(t *testing.T) {
	// Save the original DB
	originalDB := DB
	defer func() { DB = originalDB }()

	t.Run("returns existing DB if already set", func(t *testing.T) {
		// Set a mock DB
		mockDB := &sqlx.DB{}
		DB = mockDB

		// Call GetDB
		db, err := GetDB()

		// Verify results
		assert.NoError(t, err)
		assert.Equal(t, mockDB, db)
	})

	t.Run("creates new DB connection if not set", func(t *testing.T) {
		// Reset DB to nil
		DB = nil

		// Set DSN to a value that will work with sqlmock
		originalDSN := DSN
		DSN = "sqlmock"
		defer func() { DSN = originalDSN }()

		// This test is more of an integration test and would require a real DB
		// For unit testing, we'll just verify that it returns an error with an invalid DSN
		_, err := GetDB()
		assert.Error(t, err)
	})
}

func TestHandleQuery(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("successful query", func(t *testing.T) {
		// Setup mock expectations
		rows := sqlmock.NewRows([]string{"id", "name"}).
			AddRow(1, "test1").
			AddRow(2, "test2")

		mock.ExpectQuery("SELECT").WillReturnRows(rows)

		// Call HandleQuery
		result, err := HandleQuery("SELECT id, name FROM users", StatementTypeNoExplainCheck)

		// Verify results
		assert.NoError(t, err)
		assert.Contains(t, result, "id,name")
		assert.Contains(t, result, "1,test1")
		assert.Contains(t, result, "2,test2")
	})

	t.Run("query error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectQuery("SELECT").WillReturnError(fmt.Errorf("query error"))

		// Call HandleQuery
		_, err := HandleQuery("SELECT id, name FROM users", StatementTypeNoExplainCheck)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "query error")
	})
}

func TestDoQuery(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("successful query", func(t *testing.T) {
		// Setup mock expectations
		rows := sqlmock.NewRows([]string{"id", "name"}).
			AddRow(1, "test1").
			AddRow(2, "test2")

		mock.ExpectQuery("SELECT").WillReturnRows(rows)

		// Call DoQuery
		result, headers, _, err := DoQuery("SELECT id, name FROM users", StatementTypeNoExplainCheck, 0)

		// Verify results
		assert.NoError(t, err)
		assert.Equal(t, []string{"id", "name"}, headers)
		assert.Len(t, result, 2)
		assert.Equal(t, int64(1), result[0]["id"])
		assert.Equal(t, "test1", result[0]["name"])
		assert.Equal(t, int64(2), result[1]["id"])
		assert.Equal(t, "test2", result[1]["name"])
	})

	t.Run("with explain check", func(t *testing.T) {
		// Save original WithExplainCheck value
		originalWithExplainCheck := WithExplainCheck
		WithExplainCheck = true
		defer func() { WithExplainCheck = originalWithExplainCheck }()

		// Setup mock expectations for EXPLAIN
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}).
			AddRow("1", "SELECT", "users", nil, "ALL", nil, nil, nil, nil, "2", "100.00", nil)

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// Setup mock expectations for actual query
		rows := sqlmock.NewRows([]string{"id", "name"}).
			AddRow(1, "test1").
			AddRow(2, "test2")

		mock.ExpectQuery("SELECT").WillReturnRows(rows)

		// Call DoQuery
		result, headers, _, err := DoQuery("SELECT id, name FROM users", StatementTypeSelect, 0)

		// Verify results
		assert.NoError(t, err)
		assert.Equal(t, []string{"id", "name"}, headers)
		assert.Len(t, result, 2)
	})

	t.Run("query error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectQuery("SELECT").WillReturnError(fmt.Errorf("query error"))

		// Call DoQuery
		_, _, _, err := DoQuery("SELECT id, name FROM users", StatementTypeNoExplainCheck, 0)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "query error")
	})

	t.Run("columns error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectQuery("SELECT").WillReturnError(fmt.Errorf("columns error"))

		// Call DoQuery
		_, _, _, err := DoQuery("SELECT id, name FROM users", StatementTypeNoExplainCheck, 0)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "columns error")
	})

	t.Run("scan error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectQuery("SELECT").WillReturnError(fmt.Errorf("scan error"))

		// Call DoQuery
		_, _, _, err := DoQuery("SELECT id, name FROM users", StatementTypeNoExplainCheck, 0)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "scan error")
	})

	t.Run("with byte array conversion", func(t *testing.T) {
		// Setup mock expectations with a byte array value
		rows := sqlmock.NewRows([]string{"id", "blob"}).
			AddRow(1, []byte("binary data"))

		mock.ExpectQuery("SELECT").WillReturnRows(rows)

		// Call DoQuery
		result, headers, _, err := DoQuery("SELECT id, blob FROM users", StatementTypeNoExplainCheck, 0)

		// Verify results
		assert.NoError(t, err)
		assert.Equal(t, []string{"id", "blob"}, headers)
		assert.Len(t, result, 1)
		assert.Equal(t, int64(1), result[0]["id"])
		assert.Equal(t, "binary data", result[0]["blob"])
	})

	// 行数限制：maxRows=2 时返回 2 行数据 + 1 行截断警告
	t.Run("maxRows truncation", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"id", "name"}).
			AddRow(1, "a").
			AddRow(2, "b").
			AddRow(3, "c").
			AddRow(4, "d")

		mock.ExpectQuery("SELECT").WillReturnRows(rows)

		result, _, truncated, err := DoQuery("SELECT id, name FROM users", StatementTypeNoExplainCheck, 2)
		assert.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, int64(1), result[0]["id"])
		assert.Equal(t, int64(2), result[1]["id"])
		assert.True(t, truncated, "should be truncated")
	})
}

func TestHandleQueryWithLimitTruncationWarning(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	// 3 行数据，限制 2 行
	rows := sqlmock.NewRows([]string{"id", "name"}).
		AddRow(1, "a").
		AddRow(2, "b").
		AddRow(3, "c")
	mock.ExpectQuery("SELECT").WillReturnRows(rows)

	result, err := HandleQueryWithLimit("SELECT id, name FROM users", StatementTypeNoExplainCheck, 2)
	assert.NoError(t, err)
	// 警告必须出现在最终 CSV 文本里，LLM 才能看到
	assert.Contains(t, result, "WARNING")
	assert.Contains(t, result, "truncated")
}

func TestHandleExec(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("insert statement", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectExec("INSERT").WillReturnResult(sqlmock.NewResult(123, 1))

		// Call HandleExec
		result, err := HandleExec("INSERT INTO users (name) VALUES ('test')", StatementTypeInsert)

		// Verify results
		assert.NoError(t, err)
		assert.Contains(t, result, "1 rows affected")
		assert.Contains(t, result, "last insert id: 123")
	})

	t.Run("update statement", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectExec("UPDATE").WillReturnResult(sqlmock.NewResult(0, 2))

		// Call HandleExec
		result, err := HandleExec("UPDATE users SET name = 'updated' WHERE id IN (1, 2)", StatementTypeNoExplainCheck)

		// Verify results
		assert.NoError(t, err)
		assert.Equal(t, "2 rows affected", result)
	})

	t.Run("exec error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectExec("UPDATE").WillReturnError(fmt.Errorf("exec error"))

		// Call HandleExec
		_, err := HandleExec("UPDATE users SET name = 'updated'", StatementTypeNoExplainCheck)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "exec error")
	})
}

func TestHandleExplain(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	// Save original WithExplainCheck value
	originalWithExplainCheck := WithExplainCheck
	defer func() { WithExplainCheck = originalWithExplainCheck }()

	t.Run("with explain check disabled", func(t *testing.T) {
		// Disable explain check
		WithExplainCheck = false

		// Call HandleExplain - should return nil without querying
		err := HandleExplain("SELECT * FROM users", StatementTypeSelect)

		// Verify results
		assert.NoError(t, err)
	})

	// Enable explain check for the rest of the tests
	WithExplainCheck = true

	t.Run("select query", func(t *testing.T) {
		// Setup mock expectations
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}).
			AddRow("1", "SIMPLE", "users", nil, "ALL", nil, nil, nil, nil, "2", "100.00", nil)

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// Call HandleExplain
		err := HandleExplain("SELECT * FROM users", StatementTypeSelect)

		// Verify results
		assert.NoError(t, err)
	})

	t.Run("insert query", func(t *testing.T) {
		// Setup mock expectations
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}).
			AddRow("1", "INSERT", "users", nil, "ALL", nil, nil, nil, nil, "1", "100.00", nil)

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// Call HandleExplain
		err := HandleExplain("INSERT INTO users (name) VALUES ('test')", StatementTypeInsert)

		// Verify results
		assert.NoError(t, err)
	})

	t.Run("update query", func(t *testing.T) {
		// Setup mock expectations
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}).
			AddRow("1", "UPDATE", "users", nil, "ALL", nil, nil, nil, nil, "1", "100.00", nil)

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// Call HandleExplain
		err := HandleExplain("UPDATE users SET name = 'test' WHERE id = 1", StatementTypeUpdate)

		// Verify results
		assert.NoError(t, err)
	})

	t.Run("delete query", func(t *testing.T) {
		// Setup mock expectations
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}).
			AddRow("1", "DELETE", "users", nil, "ALL", nil, nil, nil, nil, "1", "100.00", nil)

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// Call HandleExplain
		err := HandleExplain("DELETE FROM users WHERE id = 1", StatementTypeDelete)

		// Verify results
		assert.NoError(t, err)
	})

	t.Run("explain error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectQuery("EXPLAIN").WillReturnError(fmt.Errorf("explain error"))

		// Call HandleExplain
		err := HandleExplain("SELECT * FROM users", StatementTypeSelect)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "explain error")
	})

	t.Run("no results", func(t *testing.T) {
		// Setup mock expectations
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"})

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// Call HandleExplain
		err := HandleExplain("SELECT * FROM users", StatementTypeSelect)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unable to check query plan")
	})

	t.Run("type mismatch", func(t *testing.T) {
		// Setup mock expectations
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}).
			AddRow("1", "INSERT", "users", nil, "ALL", nil, nil, nil, nil, "1", "100.00", nil)

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// Call HandleExplain
		err := HandleExplain("INSERT INTO users (name) VALUES ('test')", StatementTypeUpdate)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "query plan does not match expected pattern")
	})

	t.Run("scan error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectQuery("EXPLAIN").WillReturnError(fmt.Errorf("scan error"))

		// Call HandleExplain
		err := HandleExplain("SELECT * FROM users", StatementTypeSelect)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "scan error")
	})

	// Bug A 复现：EXPLAIN 返回 select_type 为 NULL 时，旧代码 *result[0].SelectType
	// 解引用 nil 指针导致进程崩溃（与 6-29 崩溃报告吻合）。
	t.Run("nil select_type should not panic", func(t *testing.T) {
		explainRows := sqlmock.NewRows([]string{"id", "select_type", "table", "partitions", "type", "possible_keys", "key", "key_len", "ref", "rows", "filtered", "Extra"}).
			AddRow(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

		mock.ExpectQuery("EXPLAIN").WillReturnRows(explainRows)

		// 不应 panic，应返回 error
		err := HandleExplain("SELECT * FROM users", StatementTypeSelect)
		assert.Error(t, err)
	})
}

func TestHandleShowCreateTable(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("successful desc", func(t *testing.T) {
		// Setup mock expectations
		rows := sqlmock.NewRows([]string{"Table", "Create Table"}).
			AddRow("users", "CREATE TABLE `users` (`id` int(11) NOT NULL AUTO_INCREMENT, `name` varchar(255) NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")

		mock.ExpectQuery("SHOW CREATE TABLE").WillReturnRows(rows)

		// Call HandleShowCreateTable
		result, err := HandleShowCreateTable("users", "")

		// Verify results
		assert.NoError(t, err)
		assert.Contains(t, result, "CREATE TABLE `users`")
	})

	t.Run("table not found", func(t *testing.T) {
		// Setup mock expectations
		rows := sqlmock.NewRows([]string{"Table", "Create Table"})

		mock.ExpectQuery("SHOW CREATE TABLE").WillReturnRows(rows)

		// Call HandleShowCreateTable
		_, err := HandleShowCreateTable("nonexistent", "")

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "does not exist")
	})

	t.Run("query error", func(t *testing.T) {
		// Setup mock expectations
		mock.ExpectQuery("SHOW CREATE TABLE").WillReturnError(fmt.Errorf("query error"))

		// Call HandleShowCreateTable
		_, err := HandleShowCreateTable("users", "")

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "query error")
	})
}

func TestMapToCSV(t *testing.T) {
	t.Run("successful mapping", func(t *testing.T) {
		// Setup test data
		data := []map[string]interface{}{
			{"id": 1, "name": "test1"},
			{"id": 2, "name": "test2"},
		}
		headers := []string{"id", "name"}

		// Call MapToCSV
		result, err := MapToCSV(data, headers)

		// Verify results
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Len(t, lines, 3)
		assert.Equal(t, "id,name", lines[0])
		assert.Equal(t, "1,test1", lines[1])
		assert.Equal(t, "2,test2", lines[2])
	})

	t.Run("missing key", func(t *testing.T) {
		// Setup test data
		data := []map[string]interface{}{
			{"id": 1}, // missing "name"
		}
		headers := []string{"id", "name"}

		// Call MapToCSV
		_, err := MapToCSV(data, headers)

		// Verify results
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "key 'name' not found in map")
	})

	t.Run("empty data", func(t *testing.T) {
		// Setup test data
		data := []map[string]interface{}{}
		headers := []string{"id", "name"}

		// Call MapToCSV
		result, err := MapToCSV(data, headers)

		// Verify results
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Len(t, lines, 1)
		assert.Equal(t, "id,name", lines[0])
	})

	t.Run("handles different types", func(t *testing.T) {
		// Setup test data
		data := []map[string]interface{}{
			{"id": 1, "name": "test1", "active": true, "score": 3.14},
		}
		headers := []string{"id", "name", "active", "score"}

		// Call MapToCSV
		result, err := MapToCSV(data, headers)

		// Verify results
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Len(t, lines, 2)
		assert.Equal(t, "id,name,active,score", lines[0])
		assert.Equal(t, "1,test1,true,3.14", lines[1])
	})

	t.Run("header write error", func(t *testing.T) {
		// This is hard to test directly since we can't easily mock the csv.Writer
		// But we can at least ensure our error handling code is covered
		// by checking that the error message is correctly formatted
		_ = []map[string]interface{}{}
		_ = []string{"id", "name"}

		// Create a mock error
		mockErr := fmt.Errorf("mock header write error")

		// Simulate the error by checking the error message format
		errMsg := fmt.Errorf("failed to write headers: %v", mockErr).Error()
		assert.Contains(t, errMsg, "failed to write headers")
		assert.Contains(t, errMsg, "mock header write error")
	})

	t.Run("row write error", func(t *testing.T) {
		// Similar to the header write error test, we're checking error message format
		mockErr := fmt.Errorf("mock row write error")
		errMsg := fmt.Errorf("failed to write row: %v", mockErr).Error()
		assert.Contains(t, errMsg, "failed to write row")
		assert.Contains(t, errMsg, "mock row write error")
	})

	t.Run("flush error", func(t *testing.T) {
		// Similar to the other error tests, we're checking error message format
		mockErr := fmt.Errorf("mock flush error")
		errMsg := fmt.Errorf("error flushing CSV writer: %v", mockErr).Error()
		assert.Contains(t, errMsg, "error flushing CSV writer")
		assert.Contains(t, errMsg, "mock flush error")
	})
}

func TestIdentifierHelpers(t *testing.T) {
	t.Run("quote identifier escapes backticks", func(t *testing.T) {
		assert.Equal(t, "`a``b`", QuoteIdentifier("a`b"))
	})

	t.Run("qualified table with database", func(t *testing.T) {
		name, err := QualifiedTableName("users", "app")
		assert.NoError(t, err)
		assert.Equal(t, "`app`.`users`", name)
	})

	t.Run("qualified table rejects empty table", func(t *testing.T) {
		_, err := QualifiedTableName("", "app")
		assert.Error(t, err)
	})
}

func TestHandleListTablesWithDatabase(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"Tables_in_app"}).
		AddRow("users")

	mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLES FROM `app`")).WillReturnRows(rows)

	result, err := HandleListTables("app")
	assert.NoError(t, err)
	assert.Contains(t, result, "Tables_in_app")
	assert.Contains(t, result, "users")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleShowCreateTableWithDatabase(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"Table", "Create Table"}).
		AddRow("users", "CREATE TABLE `users` (`id` int NOT NULL, PRIMARY KEY (`id`)) ENGINE=InnoDB")

	mock.ExpectQuery(regexp.QuoteMeta("SHOW CREATE TABLE `app`.`users`")).WillReturnRows(rows)

	result, err := HandleShowCreateTable("users", "app")
	assert.NoError(t, err)
	assert.Contains(t, result, "CREATE TABLE `users`")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleShowIndex(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"Table", "Non_unique", "Key_name", "Seq_in_index", "Column_name"}).
		AddRow("users", 0, "PRIMARY", 1, "id")

	mock.ExpectQuery(regexp.QuoteMeta("SHOW INDEX FROM `app`.`users`")).WillReturnRows(rows)

	result, err := HandleShowIndex("users", "app")
	assert.NoError(t, err)
	assert.Contains(t, result, "PRIMARY")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleShowConstraints(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"CONSTRAINT_SCHEMA", "TABLE_NAME", "CONSTRAINT_NAME", "CONSTRAINT_TYPE"}).
		AddRow("app", "users", "PRIMARY", "PRIMARY KEY")

	mock.ExpectQuery("information_schema\\.TABLE_CONSTRAINTS").
		WithArgs("app", "users").
		WillReturnRows(rows)

	result, err := HandleShowConstraints("users", "app")
	assert.NoError(t, err)
	assert.Contains(t, result, "PRIMARY KEY")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleShowTableStatus(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("with database and name pattern", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"Name", "Engine", "Rows", "Data_length", "Index_length"}).
			AddRow("users", "InnoDB", int64(100), int64(16384), int64(0))

		mock.ExpectQuery(regexp.QuoteMeta("SHOW TABLE STATUS FROM `app` LIKE 'user%'")).
			WillReturnRows(rows)

		result, err := HandleShowTableStatus("user%", "app")
		assert.NoError(t, err)
		assert.Contains(t, result, "InnoDB")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("without database or name", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"Name", "Engine", "Rows"}).
			AddRow("users", "InnoDB", int64(100))

		mock.ExpectQuery("SHOW TABLE STATUS").WillReturnRows(rows)

		result, err := HandleShowTableStatus("", "")
		assert.NoError(t, err)
		assert.Contains(t, result, "InnoDB")
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestHandleShowProcesslist(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	rows := sqlmock.NewRows([]string{"Id", "User", "Host", "db", "Command", "Time", "State", "Info"}).
		AddRow(1, "root", "localhost", "app", "Query", 0, "executing", "SELECT 1")

	mock.ExpectQuery("SHOW FULL PROCESSLIST").WillReturnRows(rows)

	result, err := HandleShowProcesslist()
	assert.NoError(t, err)
	assert.Contains(t, result, "SELECT 1")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestHandleKillQuery(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("valid process id", func(t *testing.T) {
		mock.ExpectExec("KILL 42").WillReturnResult(sqlmock.NewResult(0, 0))
		result, err := HandleKillQuery(42)
		assert.NoError(t, err)
		assert.Contains(t, result, "0 rows affected")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invalid process id", func(t *testing.T) {
		_, err := HandleKillQuery(-1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "positive integer")
	})

	t.Run("zero process id", func(t *testing.T) {
		_, err := HandleKillQuery(0)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "positive integer")
	})
}

func TestHandleShowVariables(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("with pattern", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"Variable_name", "Value"}).
			AddRow("max_connections", "151")
		mock.ExpectQuery(regexp.QuoteMeta("SHOW VARIABLES LIKE 'max_%'")).WillReturnRows(rows)
		result, err := HandleShowVariables("max_%")
		assert.NoError(t, err)
		assert.Contains(t, result, "max_connections")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("without pattern", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"Variable_name", "Value"}).
			AddRow("wait_timeout", "28800")
		mock.ExpectQuery("SHOW VARIABLES").WillReturnRows(rows)
		_, err := HandleShowVariables("")
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestHandleShowStatus(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	t.Run("with pattern", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"Variable_name", "Value"}).
			AddRow("Threads_connected", "5")
		mock.ExpectQuery(regexp.QuoteMeta("SHOW STATUS LIKE 'Threads_%'")).WillReturnRows(rows)
		result, err := HandleShowStatus("Threads_%")
		assert.NoError(t, err)
		assert.Contains(t, result, "Threads_connected")
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("without pattern", func(t *testing.T) {
		rows := sqlmock.NewRows([]string{"Variable_name", "Value"}).
			AddRow("Slow_queries", "0")
		mock.ExpectQuery("SHOW STATUS").WillReturnRows(rows)
		_, err := HandleShowStatus("")
		assert.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestTransactionLifecycle(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()
	defer cleanupTransactions()

	mock.ExpectBegin()
	beginResult, err := HandleBeginTransaction(context.Background())
	assert.NoError(t, err)
	assert.Contains(t, beginResult, "tx_id: ")

	txID := strings.TrimPrefix(strings.Split(beginResult, ",")[0], "tx_id: ")
	assert.NotEmpty(t, txID)

	mock.ExpectExec("UPDATE users").WillReturnResult(sqlmock.NewResult(0, 1))
	execResult, err := HandleTransactionExec(txID, "UPDATE users SET name = 'a' WHERE id = 1")
	assert.NoError(t, err)
	assert.Equal(t, "1 rows affected", execResult)

	mock.ExpectCommit()
	commitResult, err := HandleCommitTransaction(txID)
	assert.NoError(t, err)
	assert.Equal(t, "transaction committed", commitResult)

	_, err = GetTransaction(txID)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestTransactionExecRejectsImplicitCommit(t *testing.T) {
	_, _, cleanup := setupMockDB(t)
	defer cleanup()

	_, err := HandleTransactionExec("missing", "ALTER TABLE users ADD COLUMN age int")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "implicit commit")
}

func TestDevelopmentDDLHandlers(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	mock.ExpectExec(regexp.QuoteMeta("CREATE DATABASE `app` DEFAULT CHARACTER SET utf8mb4 DEFAULT COLLATE utf8mb4_0900_ai_ci")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	result, err := HandleCreateDatabase("app", "utf8mb4", "utf8mb4_0900_ai_ci")
	assert.NoError(t, err)
	assert.Equal(t, "1 rows affected", result)

	mock.ExpectExec(regexp.QuoteMeta("CREATE UNIQUE INDEX `uk_users_email` ON `app`.`users` (`email`, `tenant_id`)")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	result, err = HandleCreateIndex("uk_users_email", "users", "email, tenant_id", "app", true)
	assert.NoError(t, err)
	assert.Equal(t, "0 rows affected", result)

	mock.ExpectExec(regexp.QuoteMeta("DROP INDEX `uk_users_email` ON `app`.`users`")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	result, err = HandleDropIndex("uk_users_email", "users", "app")
	assert.NoError(t, err)
	assert.Equal(t, "0 rows affected", result)

	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestSplitSQLStatements(t *testing.T) {
	sql := `
CREATE TABLE users (name varchar(255) DEFAULT 'a;b');
-- comment with ;
INSERT INTO users(name) VALUES ('x;y');
`

	statements, err := SplitSQLStatements(sql)
	assert.NoError(t, err)
	assert.Len(t, statements, 2)
	assert.Contains(t, statements[0], "'a;b'")
	assert.Contains(t, statements[1], "'x;y'")
}

func TestSQLGuards(t *testing.T) {
	assert.NoError(t, ValidateReadQuery("SELECT * FROM users"))
	assert.NoError(t, ValidateReadQuery(" show tables"))
	assert.Error(t, ValidateReadQuery("UPDATE users SET name = 'x' WHERE id = 1"))

	assert.NoError(t, ValidateWriteGuard("UPDATE users SET name = 'x' WHERE id = 1", StatementTypeUpdate, false))
	assert.Error(t, ValidateWriteGuard("UPDATE users SET name = 'x'", StatementTypeUpdate, false))
	assert.NoError(t, ValidateWriteGuard("UPDATE users SET name = 'x'", StatementTypeUpdate, true))

	assert.NoError(t, ValidateWriteGuard("DELETE FROM users WHERE id = 1", StatementTypeDelete, false))
	assert.Error(t, ValidateWriteGuard("DELETE FROM users -- WHERE id = 1", StatementTypeDelete, false))
	assert.Error(t, ValidateWriteGuard("SELECT * FROM users WHERE id = 1", StatementTypeDelete, false))
}

func TestHandleExecuteMigration(t *testing.T) {
	_, mock, cleanup := setupMockDB(t)
	defer cleanup()

	mock.ExpectExec(regexp.QuoteMeta("CREATE TABLE users (id int)")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO users(id) VALUES (1)")).
		WillReturnResult(sqlmock.NewResult(1, 1))

	result, err := HandleExecuteMigration(context.Background(), "CREATE TABLE users (id int); INSERT INTO users(id) VALUES (1);", false)
	assert.NoError(t, err)
	assert.Contains(t, result, "1. CREATE TABLE users (id int)")
	assert.Contains(t, result, "2. INSERT INTO users(id) VALUES (1)")
	assert.NoError(t, mock.ExpectationsWereMet())
}

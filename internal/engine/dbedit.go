package engine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"github.com/joshuadouch/berth/internal/model"
)

// The database editor connects as the site's own database user over the
// local socket, so it can only ever see that site's database.

func (e *Engine) siteDB(ctx context.Context, s model.Site) (*sql.DB, error) {
	if s.DB == nil {
		return nil, errors.New("this site has no database")
	}
	cfg := mysql.NewConfig()
	cfg.User, cfg.Passwd, cfg.DBName = s.DB.User, s.DB.Password, s.DB.Name
	cfg.Net, cfg.Addr = "unix", e.Cfg.MySQL.Socket
	if strings.Contains(e.Cfg.MySQL.Socket, ":") && !strings.HasPrefix(e.Cfg.MySQL.Socket, "/") {
		cfg.Net = "tcp" // host:port (tests)
	}
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 60*time.Second, 60*time.Second
	cfg.Collation = "utf8mb4_unicode_ci"
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(2)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("could not connect to the database: %w", err)
	}
	return db, nil
}

// DBTable is one table in the editor's list.
type DBTable struct {
	Name   string `json:"name"`
	Rows   int64  `json:"rows"` // estimate for InnoDB
	Size   int64  `json:"size"`
	Engine string `json:"engine"`
}

// DBTables lists the site's tables.
func (e *Engine) DBTables(ctx context.Context, s model.Site) ([]DBTable, error) {
	db, err := e.siteDB(ctx, s)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT TABLE_NAME, COALESCE(TABLE_ROWS,0), COALESCE(DATA_LENGTH+INDEX_LENGTH,0), COALESCE(ENGINE,'')
		FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() ORDER BY TABLE_NAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DBTable{}
	for rows.Next() {
		var t DBTable
		if err := rows.Scan(&t.Name, &t.Rows, &t.Size, &t.Engine); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DBColumn describes a column.
type DBColumn struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Key      string  `json:"key"`
	Default  *string `json:"default"`
	Extra    string  `json:"extra"`
}

// DBRows is one page of a table.
type DBRows struct {
	Columns []DBColumn `json:"columns"`
	Primary []string   `json:"primary"` // empty: rows cannot be edited
	Rows    [][]any    `json:"rows"`
	Total   int64      `json:"total"`
}

// DBQuery selects a page of a table.
type DBQuery struct {
	Page   int    `json:"page"`
	Size   int    `json:"size"`
	Sort   string `json:"sort"`
	Desc   bool   `json:"desc"`
	Search string `json:"search"`
}

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

func columns(ctx context.Context, db *sql.DB, table string) ([]DBColumn, []string, error) {
	rows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE='YES', COLUMN_KEY, COLUMN_DEFAULT, EXTRA
		FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, table)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var cols []DBColumn
	for rows.Next() {
		var c DBColumn
		var def sql.NullString
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Key, &def, &c.Extra); err != nil {
			return nil, nil, err
		}
		if def.Valid {
			c.Default = &def.String
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if len(cols) == 0 {
		return nil, nil, errors.New("no such table")
	}
	pk := []string{}
	krows, err := db.QueryContext(ctx, `SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY' ORDER BY ORDINAL_POSITION`, table)
	if err == nil {
		defer krows.Close()
		for krows.Next() {
			var n string
			if krows.Scan(&n) == nil {
				pk = append(pk, n)
			}
		}
	}
	return cols, pk, nil
}

// cellValue turns a scanned value into JSON: text, null, or a marker for
// binary data the editor cannot show.
func cellValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		if utf8.Valid(x) {
			return string(x)
		}
		return map[string]any{"binary": len(x)}
	case time.Time:
		return x.Format("2006-01-02 15:04:05")
	default:
		return fmt.Sprint(x)
	}
}

func scanAll(rows *sql.Rows, limit int) ([]string, [][]any, bool, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, false, err
	}
	out := [][]any{}
	more := false
	for rows.Next() {
		if limit > 0 && len(out) >= limit {
			more = true
			break
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, false, err
		}
		row := make([]any, len(cols))
		for i, v := range raw {
			row[i] = cellValue(v)
		}
		out = append(out, row)
	}
	return cols, out, more, rows.Err()
}

// DBBrowse returns a page of rows.
func (e *Engine) DBBrowse(ctx context.Context, s model.Site, table string, q DBQuery) (DBRows, error) {
	db, err := e.siteDB(ctx, s)
	if err != nil {
		return DBRows{}, err
	}
	defer db.Close()
	cols, pk, err := columns(ctx, db, table)
	if err != nil {
		return DBRows{}, err
	}
	if q.Size <= 0 || q.Size > 500 {
		q.Size = 50
	}
	if q.Page < 0 {
		q.Page = 0
	}
	where, args := "", []any{}
	if q.Search = strings.TrimSpace(q.Search); q.Search != "" {
		var ors []string
		for _, c := range cols {
			ors = append(ors, "CAST("+quoteIdent(c.Name)+" AS CHAR) LIKE ?")
			args = append(args, "%"+q.Search+"%")
		}
		where = " WHERE " + strings.Join(ors, " OR ")
	}
	order := ""
	sortCol := q.Sort
	if sortCol == "" && len(pk) > 0 {
		sortCol = pk[0]
	}
	for _, c := range cols {
		if c.Name == sortCol {
			order = " ORDER BY " + quoteIdent(c.Name)
			if q.Desc {
				order += " DESC"
			}
		}
	}
	res := DBRows{Columns: cols, Primary: pk}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(table)+where, args...).Scan(&res.Total); err != nil {
		return res, err
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s%s%s LIMIT %d OFFSET %d", quoteIdent(table), where, order, q.Size, q.Page*q.Size), args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	_, res.Rows, _, err = scanAll(rows, 0)
	return res, err
}

// DBRowChange is an insert, update or delete from the editor. Values map
// column names to a string, or null.
type DBRowChange struct {
	Key    map[string]any `json:"key"`
	Values map[string]any `json:"values"`
}

func checkCols(cols []DBColumn, m map[string]any) error {
	known := map[string]bool{}
	for _, c := range cols {
		known[c.Name] = true
	}
	for k, v := range m {
		if !known[k] {
			return fmt.Errorf("unknown column %q", k)
		}
		switch v.(type) {
		case nil, string, float64, bool:
		default:
			return fmt.Errorf("column %q: only text or null can be stored", k)
		}
	}
	return nil
}

func keyWhere(pk []string, key map[string]any) (string, []any, error) {
	if len(pk) == 0 {
		return "", nil, errors.New("this table has no primary key, so rows cannot be edited here; use a query instead")
	}
	var parts []string
	var args []any
	for _, k := range pk {
		v, ok := key[k]
		if !ok {
			return "", nil, fmt.Errorf("missing key column %q", k)
		}
		if v == nil {
			parts = append(parts, quoteIdent(k)+" IS NULL")
		} else {
			parts = append(parts, quoteIdent(k)+" = ?")
			args = append(args, v)
		}
	}
	return " WHERE " + strings.Join(parts, " AND "), args, nil
}

// DBChange inserts (no key), updates (key and values) or deletes (key only)
// one row.
func (e *Engine) DBChange(ctx context.Context, s model.Site, table, op string, ch DBRowChange) (int64, error) {
	db, err := e.siteDB(ctx, s)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	cols, pk, err := columns(ctx, db, table)
	if err != nil {
		return 0, err
	}
	if err := checkCols(cols, ch.Values); err != nil {
		return 0, err
	}
	if err := checkCols(cols, ch.Key); err != nil {
		return 0, err
	}
	var stmt string
	var args []any
	switch op {
	case "insert":
		var names, marks []string
		for k, v := range ch.Values {
			names = append(names, quoteIdent(k))
			marks = append(marks, "?")
			args = append(args, v)
		}
		if len(names) == 0 {
			stmt = "INSERT INTO " + quoteIdent(table) + " () VALUES ()"
		} else {
			stmt = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteIdent(table), strings.Join(names, ", "), strings.Join(marks, ", "))
		}
	case "update":
		if len(ch.Values) == 0 {
			return 0, errors.New("nothing to change")
		}
		var sets []string
		for k, v := range ch.Values {
			sets = append(sets, quoteIdent(k)+" = ?")
			args = append(args, v)
		}
		w, wargs, err := keyWhere(pk, ch.Key)
		if err != nil {
			return 0, err
		}
		stmt = "UPDATE " + quoteIdent(table) + " SET " + strings.Join(sets, ", ") + w + " LIMIT 1"
		args = append(args, wargs...)
	case "delete":
		w, wargs, err := keyWhere(pk, ch.Key)
		if err != nil {
			return 0, err
		}
		stmt, args = "DELETE FROM "+quoteIdent(table)+w+" LIMIT 1", wargs
	default:
		return 0, errors.New("unknown change")
	}
	res, err := db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DBQueryResult is the answer to a typed SQL statement.
type DBQueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
	Affected  int64    `json:"affected"`
	Millis    int64    `json:"ms"`
}

// DBRunSQL runs one statement typed by the admin, as the site's user.
func (e *Engine) DBRunSQL(ctx context.Context, s model.Site, stmt string) (DBQueryResult, error) {
	stmt = strings.TrimSpace(stmt)
	if stmt == "" {
		return DBQueryResult{}, errors.New("type a query")
	}
	db, err := e.siteDB(ctx, s)
	if err != nil {
		return DBQueryResult{}, err
	}
	defer db.Close()
	start := time.Now()
	conn, err := db.Conn(ctx)
	if err != nil {
		return DBQueryResult{}, err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, stmt)
	if err != nil {
		return DBQueryResult{}, err
	}
	defer rows.Close()
	cols, data, more, err := scanAll(rows, 1000)
	res := DBQueryResult{Columns: cols, Rows: data, Truncated: more, Millis: time.Since(start).Milliseconds()}
	if err != nil {
		return res, err
	}
	if len(cols) == 0 {
		// Statements without a result set: ask how many rows changed.
		rows.Close()
		_ = conn.QueryRowContext(ctx, "SELECT ROW_COUNT()").Scan(&res.Affected)
	}
	return res, nil
}

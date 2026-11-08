package db

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	Storage   *Postgres
	ErrNoRows = errors.New("sql: no rows in result set")

	validIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Postgres struct {
	DB      *sqlx.DB
	schemas []string
	tunnel  []io.Closer // the ssh client and its local listener, closed with DB
}

type Transaction struct {
	tx *sqlx.Tx
}

type TableInfo struct {
	Name       string         `db:"table_name"`
	Type       string         `db:"table_type"`
	Size       string         `db:"table_size"`
	PrimaryKey sql.NullString `db:"primary_key"`
}

type DBConfig struct {
	Title     string
	URL       string
	ViewsPath string
}

func New(dataBaseURL string) (*Postgres, error) {
	pg := &Postgres{}
	u, err := url.Parse(dataBaseURL)
	if err != nil {
		return nil, err
	}

	pg.schemas = []string{"public"}
	if u.Query().Has("search_path") {
		pg.schemas = strings.Split(u.Query().Get("search_path"), ",")
		for i, sc := range pg.schemas {
			pg.schemas[i] = strings.TrimSpace(sc)
		}
	}

	pg.DB, err = pg.open(withDefaults(u).String())
	if err != nil {
		pg.Close()
		return nil, err
	}
	return pg, nil
}

// withDefaults bounds what a connection may wait for, unless the URL says
// otherwise: the interface is one loop, and a server that never answers
// (or a lock that never frees) would freeze it.
func withDefaults(u *url.URL) *url.URL {
	q := u.Query()
	if !q.Has("connect_timeout") {
		q.Set("connect_timeout", "10")
	}
	if !q.Has("statement_timeout") {
		q.Set("statement_timeout", "60s")
	}
	out := *u
	out.RawQuery = q.Encode()
	return &out
}

// expandPath replaces a leading "~" with the current user's home directory.
func expandPath(p string) (string, error) {
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return strings.Replace(p, "~", home, 1), nil
	}
	return p, nil
}

// ssh+postgres://dbUser:dbPass@dbRemoteHost:5432/dbname?sshalias=mydb&localport=5433&sslmode=disable
func (pg *Postgres) open(dbsource string) (*sqlx.DB, error) {
	if after, ok := strings.CutPrefix(dbsource, "ssh+"); ok {
		u, err := url.Parse(after)
		if err != nil {
			return nil, fmt.Errorf("failed to parse DSN: %w", err)
		}
		t, err := sshTarget(u)
		if err != nil {
			return nil, err
		}
		err = pg.startTunnel(t)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		for _, k := range []string{"sshuser", "sshhost", "sshport", "sshkey", "localport", "sshalias", "remotetarget"} {
			q.Del(k)
		}
		u.RawQuery = q.Encode()
		u.Host = net.JoinHostPort("localhost", t.localPort)
		dbsource = u.String()
	}

	db, err := sqlx.Open("pgx", dbsource)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(30)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

type tunnelTarget struct {
	user, host, port, key string
	localPort, remote     string
}

// sshTarget reads the tunnel from the URL's parameters, filling the gaps
// from ~/.ssh/config when sshalias names a host there.
func sshTarget(u *url.URL) (tunnelTarget, error) {
	q := u.Query()
	t := tunnelTarget{
		user:      q.Get("sshuser"),
		host:      q.Get("sshhost"),
		port:      q.Get("sshport"),
		key:       q.Get("sshkey"),
		localPort: q.Get("localport"),
		remote:    q.Get("remotetarget"),
	}
	if t.remote == "" {
		t.remote = u.Host
	}
	if t.localPort == "" {
		return t, errors.New("localport must be specified for SSH tunneling")
	}
	alias := q.Get("sshalias")
	if alias != "" {
		err := t.fromSSHConfig(alias)
		if err != nil {
			return t, err
		}
	}
	if t.port == "" {
		t.port = "22"
	}
	if t.user == "" {
		t.user = os.Getenv("USER")
	}
	key, err := expandPath(t.key)
	if err != nil {
		return t, fmt.Errorf("failed to expand SSH key path: %w", err)
	}
	t.key = key
	return t, nil
}

func (t *tunnelTarget) fromSSHConfig(alias string) error {
	f, err := os.Open(filepath.Clean(os.ExpandEnv("$HOME/.ssh/config")))
	if err != nil {
		return fmt.Errorf("failed to open SSH config: %w", err)
	}
	defer func() { _ = f.Close() }()
	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return fmt.Errorf("failed to decode SSH config: %w", err)
	}
	get := func(key string) string {
		v, _ := cfg.Get(alias, key)
		return v
	}
	if t.user == "" {
		t.user = get("User")
	}
	if t.host == "" {
		t.host = cmp.Or(get("Hostname"), alias)
	}
	if t.port == "" {
		t.port = get("Port")
	}
	if t.key == "" && get("IdentityFile") != "" {
		t.key = os.ExpandEnv(get("IdentityFile"))
	}
	return nil
}

// startTunnel forwards localhost:localPort to the remote target through
// ssh; both ends are kept in pg.tunnel for Close.
func (pg *Postgres) startTunnel(t tunnelTarget) error {
	key, err := os.ReadFile(filepath.Clean(t.key))
	if err != nil {
		return fmt.Errorf("failed to read SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to parse SSH key: %w", err)
	}
	hostKeyCallback, err := knownhosts.New(os.ExpandEnv("$HOME/.ssh/known_hosts"))
	if err != nil {
		return fmt.Errorf("failed to create host key callback: %w", err)
	}
	sshConn, err := ssh.Dial("tcp", net.JoinHostPort(t.host, t.port), &ssh.ClientConfig{
		User:            t.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		Timeout:         5 * time.Second,
		HostKeyCallback: hostKeyCallback,
	})
	if err != nil {
		return fmt.Errorf("failed to dial SSH: %w", err)
	}
	pg.tunnel = append(pg.tunnel, sshConn)
	listener, err := net.Listen("tcp", net.JoinHostPort("localhost", t.localPort))
	if err != nil {
		return fmt.Errorf("failed to start local listener: %w", err)
	}
	pg.tunnel = append(pg.tunnel, listener)
	go forward(listener, sshConn, t.remote)
	return nil
}

func forward(listener net.Listener, sshConn *ssh.Client, remote string) {
	for {
		localConn, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return // Close took the tunnel down
		}
		if err != nil {
			continue
		}
		remoteConn, err := sshConn.Dial("tcp", remote)
		if err != nil {
			_ = localConn.Close()
			continue
		}
		go func() {
			defer func() { _ = localConn.Close() }()
			defer func() { _ = remoteConn.Close() }()
			go func() { _, _ = io.Copy(localConn, remoteConn) }()
			_, _ = io.Copy(remoteConn, localConn)
		}()
	}
}

func (pg *Postgres) Stats() sql.DBStats {
	return pg.DB.Stats()
}

func (pg *Postgres) BeginTransaction() (*Transaction, error) {
	var err error
	tx, err := pg.DB.Beginx()
	if err != nil {
		return nil, err
	}
	t := Transaction{
		tx: tx,
	}
	return &t, nil
}

func (pg *Transaction) Commit() error {
	err := pg.tx.Commit()
	return err
}

func (pg *Transaction) Rollback() error {
	err := pg.tx.Rollback()
	return err
}

func (pg *Transaction) Select(dest any, query string, args ...any) error {
	return pg.tx.Select(dest, query, args...)
}

func (pg *Transaction) Query(query string, args ...any) (*sql.Rows, error) {
	return pg.tx.Query(query, args...)
}

func (pg *Transaction) Get(dest any, query string, args ...any) error {
	return pg.tx.Get(dest, query, args...)
}

func (pg *Transaction) Exec(query string, args ...any) error {
	_, err := pg.tx.Exec(query, args...)
	return err
}

func (pg *Transaction) QueryRow(query string, args ...any) *sql.Row {
	return pg.tx.QueryRow(query, args...)
}

func (pg *Postgres) QueryRow(query string, args ...any) *sql.Row {
	return pg.DB.QueryRow(query, args...)
}

func (pg *Postgres) Query(query string, args ...any) (*sql.Rows, error) {
	return pg.DB.Query(query, args...)
}

func (pg *Postgres) Close() {
	if pg.DB != nil {
		err := pg.DB.Close()
		if err != nil {
			log.Println(err)
		}
	}
	for _, v := range slices.Backward(pg.tunnel) {
		_ = v.Close()
	}
	pg.tunnel = nil
}

func (pg *Postgres) Exec(query string, args ...any) error {
	_, err := pg.DB.Exec(query, args...)
	return err
}

func (pg *Postgres) Get(ret any, sqlStatement string, args ...any) error {
	stmt, err := pg.DB.Preparex(sqlStatement)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	err = stmt.Get(ret, args...)
	if err != nil {
		if err == sql.ErrNoRows {
			return ErrNoRows
		}
		return err
	}
	return nil
}

func (pg *Postgres) ListTablesAndViews() ([]TableInfo, error) {
	const sqlStatement = `SELECT
        c.relname AS table_name,
        CASE c.relkind
            WHEN 'r' THEN 'table'
            WHEN 'p' THEN 'table'
            WHEN 'v' THEN 'view'
            WHEN 'm' THEN 'materialized view'
            ELSE c.relkind::text
        END AS table_type,
        pg_size_pretty(pg_total_relation_size(c.oid)) AS table_size,
        (
            SELECT string_agg(a.attname, ', ' ORDER BY array_position(i.indkey::int2[], a.attnum))
            FROM pg_index i
            JOIN pg_attribute a
                ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
            WHERE i.indrelid = c.oid AND i.indisprimary
        ) AS primary_key
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = ANY($1::text[])
          AND c.relkind IN ('r', 'p', 'v', 'm')
        ORDER BY table_type, c.relname;`
	var tables []TableInfo
	err := pg.DB.Select(&tables, sqlStatement, pg.schemas)
	if err != nil {
		return nil, err
	}
	return tables, nil
}

type ColumnInfo struct {
	ColumnName string `db:"column_name"`
	DataType   string `db:"data_type"`
}

func (pg *Postgres) ListColumns(tableName string) ([]ColumnInfo, error) {
	rel, err := pg.Resolve(tableName)
	if err != nil {
		return nil, err
	}
	return pg.relColumns(rel)
}

func (pg *Postgres) relColumns(rel Rel) ([]ColumnInfo, error) {
	const sqlStatement = `SELECT column_name, data_type
	FROM information_schema.columns
	WHERE table_schema = $1 AND table_name = $2
	ORDER BY ordinal_position;`
	var columns []ColumnInfo
	err := pg.DB.Select(&columns, sqlStatement, rel.Schema, rel.Name)
	if err != nil {
		return nil, err
	}
	return columns, nil
}

func (pg *Postgres) ListRecords(
	tableName string,
	_ string, // the primary key now comes from the catalog, quoted
	offset int,
	limit int,
	where string,
	orderBy string,
) (
	[]map[string]any,
	[]ColumnInfo,
	int,
	error,
) {
	rel, err := pg.Resolve(tableName)
	if err != nil {
		return nil, nil, 0, err
	}
	if where != "" {
		where = "WHERE " + where
	}

	var totalRecords int
	err = pg.Get(&totalRecords, fmt.Sprintf("SELECT COUNT(*) FROM %s %s", rel.SQL, where))
	if err != nil {
		return nil, nil, 0, err
	}

	columns, err := pg.relColumns(rel)
	if err != nil {
		return nil, nil, 0, err
	}
	fields := make([]string, len(columns))
	for i, column := range columns {
		fields[i] = QuoteName(column.ColumnName)
	}

	pks, err := pg.relPrimaryKey(rel)
	if err != nil {
		return nil, nil, 0, err
	}
	for i, pk := range pks {
		pks[i] = QuoteName(pk)
	}
	// The primary key last breaks ties, so a page boundary on equal values
	// neither skips nor repeats rows.
	order := strings.Join(slices.DeleteFunc([]string{orderBy, strings.Join(pks, ", ")}, func(s string) bool { return s == "" }), ", ")
	if order != "" {
		order = "ORDER BY " + order
	}

	sqlStatement := fmt.Sprintf(
		"SELECT %s\nFROM\n%s %s %s\nOFFSET %d\nLIMIT %d",
		strings.Join(fields, ", "),
		rel.SQL,
		where,
		order,
		offset,
		limit,
	)

	rows, err := pg.DB.Queryx(sqlStatement)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error executing query: %v %v", sqlStatement, err)
	}
	defer func() { _ = rows.Close() }()

	var records []map[string]any
	for rows.Next() {
		values := make([]any, len(columns))
		for i := range values {
			values[i] = new(any)
		}
		err = rows.Scan(values...)
		if err != nil {
			return nil, nil, 0, err
		}
		record := make(map[string]any, len(columns))
		for i, column := range columns {
			record[column.ColumnName] = displayValue(*(values[i].(*any)), column.DataType)
		}
		records = append(records, record)
	}
	return records, columns, totalRecords, rows.Err()
}

// displayValue is v as the grid shows and edits it: nil stays nil (NULL),
// the rest is text exactly as stored, never rounded or trimmed, since the
// text of a key is what finds its row again.
func displayValue(v any, dataType string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		if dataType == "date" {
			return x.Format(time.DateOnly)
		}
		return x.Format(time.RFC3339Nano)
	}
	return fmt.Sprint(v)
}

func (pg *Postgres) GetPrimaryKeyColumns(tableName string) ([]string, error) {
	rel, err := pg.Resolve(tableName)
	if err != nil {
		return nil, err
	}
	return pg.relPrimaryKey(rel)
}

func (pg *Postgres) relPrimaryKey(rel Rel) ([]string, error) {
	const query = `SELECT a.attname
	FROM pg_index i
	JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
	WHERE i.indrelid = $1 AND i.indisprimary
	ORDER BY array_position(i.indkey::int2[], a.attnum)`
	var pkColumns []string
	err := pg.DB.Select(&pkColumns, query, rel.OID)
	if err != nil {
		return nil, fmt.Errorf("primary key of %s: %w", rel.SQL, err)
	}
	return pkColumns, nil
}

func toGenericTimestamp(input string) (time.Time, error) {
	if strings.Contains(input, "T") {
		if t, err := time.Parse(time.RFC3339Nano, input); err == nil {
			return t, nil
		}
		return time.Parse(time.RFC3339, input)
	}
	parts := strings.SplitN(input, " ", 2)
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid timestamp format: %s", input)
	}
	newInput := parts[0] + "T" + parts[1]
	if t, err := time.Parse(time.RFC3339Nano, newInput); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, newInput)
}

func (pg *Postgres) UpdateDataCell(
	tableName,
	fieldName string,
	newValue any,
	pkField []string,
	pkValue []any,
) error {
	rel, err := pg.Resolve(tableName)
	if err != nil {
		return err
	}
	columns, err := pg.relColumns(rel)
	if err != nil {
		return fmt.Errorf("error fetching column types: %w", err)
	}
	colTypes := make(map[string]string)
	for _, col := range columns {
		colTypes[col.ColumnName] = col.DataType
	}

	if dt, ok := colTypes[fieldName]; ok && strings.Contains(strings.ToLower(dt), "timestamp") {
		if s, ok := newValue.(string); ok {
			t, err := toGenericTimestamp(s)
			if err != nil {
				return fmt.Errorf("error converting newValue timestamp: %w", err)
			}
			newValue = t
		}
	}

	for i, field := range pkField {
		if dt, ok := colTypes[field]; ok && strings.Contains(strings.ToLower(dt), "timestamp") {
			if s, ok := pkValue[i].(string); ok {
				t, err := toGenericTimestamp(s)
				if err != nil {
					return fmt.Errorf("error converting pk value for %s: %w", field, err)
				}
				pkValue[i] = t
			}
		}
	}

	updateCast := ""
	if dt, ok := colTypes[fieldName]; ok && strings.Contains(strings.ToLower(dt), "timestamp") {
		updateCast = "::" + dt
	}

	whereClauses := make([]string, len(pkField))
	for i, field := range pkField {
		whereClauses[i] = fmt.Sprintf("%s = $%d", QuoteName(field), i+2)
	}

	// ONLY: an inherited child holding the same key is another row.
	sqlStatement := fmt.Sprintf( // #nosec G201 -- names quoted by Resolve and QuoteName, values bound
		"UPDATE ONLY %s SET %s = $1%s WHERE %s",
		rel.SQL,
		QuoteName(fieldName),
		updateCast,
		strings.Join(whereClauses, " AND "),
	)

	args := make([]any, 0, len(pkValue)+1)
	args = append(args, newValue)
	args = append(args, pkValue...)

	tx, err := pg.DB.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(sqlStatement, args...)
	if err != nil {
		return fmt.Errorf("error in update: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("error getting affected rows: %w", err)
	}
	if rowsAffected != 1 {
		return fmt.Errorf("the key matches %d rows, not one: nothing changed", rowsAffected)
	}
	return tx.Commit()
}

func (pg *Postgres) QueryRowContext(
	ctx context.Context,
	query string,
	args ...any,
) *sql.Row {
	return pg.DB.QueryRowContext(ctx, query, args...)
}

// CreateUpdateStatement writes an UPDATE of row (nil is NULL) for the user
// to edit and run: every value a literal, every name quoted.
func (pg *Postgres) CreateUpdateStatement(tableName string, headers []string, row []any) (string, error) {
	if len(row) != len(headers) {
		return "", errors.New("no row selected")
	}
	rel, err := pg.Resolve(tableName)
	if err != nil {
		return "", err
	}
	pks, err := pg.relPrimaryKey(rel)
	if err != nil {
		return "", err
	}
	if len(pks) == 0 {
		return "", fmt.Errorf("no primary key columns found for table %s", rel.SQL)
	}
	columns, err := pg.relColumns(rel)
	if err != nil {
		return "", fmt.Errorf("error fetching column types: %w", err)
	}
	colTypes := make(map[string]string)
	for _, col := range columns {
		colTypes[col.ColumnName] = strings.ToLower(col.DataType)
	}

	var sets, wheres []string
	for i, col := range headers {
		if !slices.Contains(pks, col) {
			sets = append(sets, fmt.Sprintf("    %s = %s", QuoteName(col), sqlValue(row[i], colTypes[col])))
		}
	}
	if len(sets) == 0 {
		return "", errors.New("there are no columns to update")
	}
	for _, pk := range pks {
		idx := slices.Index(headers, pk)
		if idx < 0 {
			return "", fmt.Errorf("column %s not found in headers", pk)
		}
		if row[idx] == nil {
			wheres = append(wheres, QuoteName(pk)+" IS NULL")
			continue
		}
		wheres = append(wheres, QuoteName(pk)+" = "+sqlValue(row[idx], colTypes[pk]))
	}

	n := 0
	number := func(lines []string, sep string) string {
		width := 0
		for _, l := range lines {
			width = max(width, len(l)+len(sep))
		}
		out := make([]string, len(lines))
		for i, l := range lines {
			n++
			if i < len(lines)-1 {
				l += sep
			}
			out[i] = fmt.Sprintf("%-*s -- %d", width, l, n)
		}
		return strings.Join(out, "\n")
	}
	setBlock := number(sets, ",")
	for i := 1; i < len(wheres); i++ {
		wheres[i] = "  AND " + wheres[i]
	}
	return fmt.Sprintf("UPDATE ONLY %s\nSET\n%s\nWHERE %s\n;", rel.SQL, setBlock, number(wheres, "")), nil
}

// sqlValue is v as a literal for a column of dataType: numbers and booleans
// bare when they are what they claim, everything else quoted.
func sqlValue(v any, dataType string) string {
	if v == nil {
		return "NULL"
	}
	s := fmt.Sprint(v)
	switch {
	case strings.Contains(dataType, "int"), strings.Contains(dataType, "numeric"),
		strings.Contains(dataType, "decimal"), strings.Contains(dataType, "real"),
		strings.Contains(dataType, "double"):
		_, err := strconv.ParseFloat(s, 64)
		if err == nil && strings.Trim(s, "0123456789.eE+-") == "" { // not NaN, Inf or hex
			return s
		}
	case dataType == "boolean" && (s == "true" || s == "false"):
		return s
	}
	return quoteLiteral(s)
}

// QuerySQL executes the given SQL query and returns records with column definitions.
func (pg *Postgres) QuerySQL(query string) ([]map[string]any, []ColumnInfo, error) {
	rows, err := pg.DB.Queryx(query)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()

	// Get column names and types.
	columnNames, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, nil, err
	}
	var columns []ColumnInfo
	for i, name := range columnNames {
		dtype := ""
		if i < len(colTypes) {
			dtype = colTypes[i].DatabaseTypeName()
		}
		columns = append(columns, ColumnInfo{
			ColumnName: name,
			DataType:   dtype,
		})
	}

	var records []map[string]any
	for rows.Next() {
		record := make(map[string]any)
		values := make([]any, len(columnNames))
		pointers := make([]any, len(columnNames))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		for i, col := range columnNames {
			record[col] = values[i]
		}
		records = append(records, record)
	}
	return records, columns, rows.Err()
}

func (pg *Postgres) RenameTable(oldName, newName string) error {
	if !validIdent.MatchString(oldName) ||
		!validIdent.MatchString(newName) {
		return fmt.Errorf("invalid table name: %q or %q", oldName, newName)
	}

	query := fmt.Sprintf(
		`ALTER TABLE "%s" RENAME TO "%s";`,
		oldName, newName,
	)

	_, err := pg.DB.Exec(query)
	if err != nil {
		return fmt.Errorf("error renaming table: %w SQL: %s", err, query)
	}
	return nil
}

func (pg *Postgres) RenameColumn(tableName string, oldColName string, newColName string) error {
	if !validIdent.MatchString(tableName) ||
		!validIdent.MatchString(oldColName) ||
		!validIdent.MatchString(newColName) {
		return fmt.Errorf("invalid table or column name: %q, %q, %q", tableName, oldColName, newColName)
	}

	query := fmt.Sprintf(
		`ALTER TABLE "%s" RENAME COLUMN "%s" TO "%s";`,
		tableName, oldColName, newColName,
	)

	_, err := pg.DB.Exec(query)
	if err != nil {
		return fmt.Errorf("error renaming column: %w SQL: %s", err, query)
	}
	return nil
}

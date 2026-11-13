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

// ParseURL is url.Parse with an error that leaves the URL out: url.Parse
// quotes it whole, password and all.
func ParseURL(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	ue, ok := errors.AsType[*url.Error](err)
	if ok {
		return nil, fmt.Errorf("the url does not parse: %w", ue.Err)
	}
	return u, err
}

func New(dataBaseURL string) (*Postgres, error) {
	return NewContext(context.Background(), dataBaseURL)
}

// NewContext is New whose first connection ctx can cancel; an ssh tunnel
// takes no context, but its dial and handshake have their own bounds.
func NewContext(ctx context.Context, dataBaseURL string) (*Postgres, error) {
	pg := &Postgres{}
	u, err := ParseURL(dataBaseURL)
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

	pg.DB, err = pg.open(ctx, withDefaults(u).String())
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
func (pg *Postgres) open(ctx context.Context, dbsource string) (*sqlx.DB, error) {
	after, ok := strings.CutPrefix(dbsource, "ssh+")
	if ok {
		u, err := ParseURL(after)
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
	err = db.PingContext(ctx)
	if err != nil {
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
// sshHandshakeWait bounds the ssh handshake, key exchange and auth.
const sshHandshakeWait = 15 * time.Second

func (pg *Postgres) startTunnel(t tunnelTarget) error {
	auth, closeAgent, err := agentAuth(t.key)
	if err != nil {
		return fmt.Errorf("SSH auth: %w", err)
	}
	defer closeAgent()
	hostKeyCallback, err := knownhosts.New(os.ExpandEnv("$HOME/.ssh/known_hosts"))
	if err != nil {
		return fmt.Errorf("failed to create host key callback: %w", err)
	}
	addr := net.JoinHostPort(t.host, t.port)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to dial SSH: %w", err)
	}
	// ssh.Dial's Timeout covers the TCP connect only: a server that
	// accepts and then says nothing would hold the handshake forever
	err = conn.SetDeadline(time.Now().Add(sshHandshakeWait))
	if err != nil {
		_ = conn.Close()
		return err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:            t.user,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hostKeyCallback,
	})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("failed to dial SSH: %w", err)
	}
	err = conn.SetDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return err
	}
	sshConn := ssh.NewClient(c, chans, reqs)
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

func (pg *Postgres) ListTablesAndViews(ctx context.Context) ([]TableInfo, error) {
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
               AND array_position(i.indkey::int2[], a.attnum) - array_lower(i.indkey::int2[], 1) < i.indnkeyatts
            WHERE i.indrelid = c.oid AND i.indisprimary
        ) AS primary_key
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = ANY($1::text[])
          AND c.relkind IN ('r', 'p', 'v', 'm')
        ORDER BY table_type, c.relname;`
	var tables []TableInfo
	err := pg.DB.SelectContext(ctx, &tables, sqlStatement, pg.schemas)
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
	ctx := context.Background()
	rel, err := pg.Resolve(ctx, tableName)
	if err != nil {
		return nil, err
	}
	return pg.relColumns(ctx, rel)
}

func (pg *Postgres) relColumns(ctx context.Context, rel Rel) ([]ColumnInfo, error) {
	ex, err := pg.relColumnsEx(ctx, rel)
	if err != nil {
		return nil, err
	}
	columns := make([]ColumnInfo, len(ex))
	for i, c := range ex {
		columns[i] = ColumnInfo{ColumnName: c.ColumnName, DataType: c.DataType}
	}
	return columns, nil
}

// relColumnsEx reads pg_attribute, not information_schema.columns, which
// leaves materialized views out. data_type is named as information_schema
// names it (ARRAY, USER-DEFINED, a domain's base type): what the callers
// match on.
func (pg *Postgres) relColumnsEx(ctx context.Context, rel Rel) ([]ColumnInfoEx, error) {
	const sqlStatement = `SELECT
	a.attname AS column_name,                       -- 1
	CASE WHEN t.typcategory = 'A' THEN 'ARRAY'
	     WHEN t.typtype = 'd' THEN format_type(t.typbasetype, NULL)
	     WHEN t.typtype IN ('e', 'c', 'r', 'm') THEN 'USER-DEFINED'
	     ELSE format_type(a.atttypid, NULL)
	END AS data_type,                               -- 2
	pg_get_expr(d.adbin, d.adrelid) AS column_default, -- 3
	a.attidentity::text AS identity,                -- 4
	a.attgenerated::text AS generated               -- 5
FROM pg_attribute a
JOIN pg_type t ON t.oid = a.atttypid
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`
	var columns []ColumnInfoEx
	err := pg.DB.SelectContext(ctx, &columns, sqlStatement, rel.OID)
	if err != nil {
		return nil, err
	}
	return columns, nil
}

func (pg *Postgres) ListRecords(
	ctx context.Context,
	tableName string,
	_ string, // the primary key now comes from the catalog, quoted
	offset int,
	limit int,
	where string,
	orderBy string,
) (
	records []map[string]any,
	columns []ColumnInfo,
	total int,
	cut bool, // stopped at BytesMax before limit rows
	err error,
) {
	rel, err := pg.Resolve(ctx, tableName)
	if err != nil {
		return nil, nil, 0, false, err
	}
	if where != "" {
		where = "WHERE " + where
	}

	// COUNT(*) reads the whole table: only the first page pays for it, and
	// a later one says -1, the total its caller already has.
	// yagni: pages are OFFSET, which rereads the rows before; keyset paging
	// over an arbitrary :order is the upgrade if deep paging gets slow
	totalRecords := -1
	if offset == 0 {
		err = pg.DB.GetContext(ctx, &totalRecords, fmt.Sprintf("SELECT COUNT(*) FROM %s %s", rel.From(), where))
		if err != nil {
			return nil, nil, 0, false, err
		}
	}

	columns, err = pg.relColumns(ctx, rel)
	if err != nil {
		return nil, nil, 0, false, err
	}
	fields := make([]string, len(columns))
	for i, column := range columns {
		fields[i] = QuoteName(column.ColumnName)
	}

	pks, err := pg.relPrimaryKey(ctx, rel)
	if err != nil {
		return nil, nil, 0, false, err
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
		rel.From(),
		where,
		order,
		offset,
		limit,
	)

	rows, err := pg.DB.QueryxContext(ctx, sqlStatement)
	if err != nil {
		return nil, nil, 0, false, fmt.Errorf("error executing query: %v %v", sqlStatement, err)
	}
	defer func() { _ = rows.Close() }()

	size := 0
	for rows.Next() {
		if size > BytesMax {
			return records, columns, totalRecords, true, rows.Err()
		}
		values := make([]any, len(columns))
		for i := range values {
			values[i] = new(any)
		}
		err = rows.Scan(values...)
		if err != nil {
			return nil, nil, 0, false, err
		}
		record := make(map[string]any, len(columns))
		for i, column := range columns {
			v := displayValue(*(values[i].(*any)), column.DataType)
			record[column.ColumnName] = v
			size += sizeOf(v)
		}
		records = append(records, record)
	}
	return records, columns, totalRecords, false, rows.Err()
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

func (pg *Postgres) GetPrimaryKeyColumns(ctx context.Context, tableName string) ([]string, error) {
	rel, err := pg.Resolve(ctx, tableName)
	if err != nil {
		return nil, err
	}
	return pg.relPrimaryKey(ctx, rel)
}

func (pg *Postgres) relPrimaryKey(ctx context.Context, rel Rel) ([]string, error) {
	const query = `SELECT a.attname
	FROM pg_index i
	JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
	 -- the first indnkeyatts are the key; INCLUDE columns come after
	 AND array_position(i.indkey::int2[], a.attnum) - array_lower(i.indkey::int2[], 1) < i.indnkeyatts
	WHERE i.indrelid = $1 AND i.indisprimary
	ORDER BY array_position(i.indkey::int2[], a.attnum)`
	var pkColumns []string
	err := pg.DB.SelectContext(ctx, &pkColumns, query, rel.OID)
	if err != nil {
		return nil, fmt.Errorf("primary key of %s: %w", rel.SQL, err)
	}
	return pkColumns, nil
}

func toGenericTimestamp(input string) (time.Time, error) {
	if strings.Contains(input, "T") {
		t, err := time.Parse(time.RFC3339Nano, input)
		if err == nil {
			return t, nil
		}
		return time.Parse(time.RFC3339, input)
	}
	parts := strings.SplitN(input, " ", 2)
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("invalid timestamp format: %s", input)
	}
	newInput := parts[0] + "T" + parts[1]
	t, err := time.Parse(time.RFC3339Nano, newInput)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, newInput)
}

// ErrChanged is a cell or a row that changed in the database since it was
// read: the write that found it so wrote nothing.
var ErrChanged = errors.New("it changed in the database since it was read: nothing written")

// cellKey resolves tableName and puts its key in a WHERE whose values are
// args from $first on; pkValue as the grid shows them.
func (pg *Postgres) cellKey(ctx context.Context, tableName string, pkField []string, pkValue []any, first int) (Rel, map[string]string, string, []any, error) {
	rel, err := pg.Resolve(ctx, tableName)
	if err != nil {
		return rel, nil, "", nil, err
	}
	columns, err := pg.relColumns(ctx, rel)
	if err != nil {
		return rel, nil, "", nil, fmt.Errorf("error fetching column types: %w", err)
	}
	colTypes := make(map[string]string)
	for _, col := range columns {
		colTypes[col.ColumnName] = col.DataType
	}
	where := make([]string, len(pkField))
	args := make([]any, len(pkField))
	for i, field := range pkField {
		args[i] = pkValue[i]
		s, ok := pkValue[i].(string)
		if ok && strings.Contains(strings.ToLower(colTypes[field]), "timestamp") {
			t, err := toGenericTimestamp(s)
			if err != nil {
				return rel, nil, "", nil, fmt.Errorf("error converting pk value for %s: %w", field, err)
			}
			args[i] = t
		}
		where[i] = fmt.Sprintf("%s = $%d", QuoteName(field), first+i)
	}
	return rel, colTypes, strings.Join(where, " AND "), args, nil
}

// CellNow reads a cell as it is now: as the grid shows it (nil is NULL),
// and as text, what UpdateDataCell checks it still is.
func (pg *Postgres) CellNow(ctx context.Context, tableName, fieldName string, pkField []string, pkValue []any) (any, sql.NullString, error) {
	var text sql.NullString
	rel, colTypes, where, args, err := pg.cellKey(ctx, tableName, pkField, pkValue, 1)
	if err != nil {
		return nil, text, err
	}
	col := QuoteName(fieldName)
	q := "SELECT " + col + ", " + col + "::text FROM " + rel.From() + " WHERE " + where // #nosec G202 -- names quoted, values bound
	var v any
	err = pg.DB.QueryRowContext(ctx, q, args...).Scan(&v, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, text, errors.New("the row is gone: deleted, or its key changed")
	}
	if err != nil {
		return nil, text, err
	}
	return displayValue(v, colTypes[fieldName]), text, nil
}

// UpdateDataCell writes newValue to a cell, only while it still reads as
// old (CellNow's text): ErrChanged when someone changed it meanwhile.
func (pg *Postgres) UpdateDataCell(
	ctx context.Context,
	tableName,
	fieldName string,
	newValue any,
	pkField []string,
	pkValue []any,
	old sql.NullString,
) error {
	rel, colTypes, where, keyArgs, err := pg.cellKey(ctx, tableName, pkField, pkValue, 3)
	if err != nil {
		return err
	}
	updateCast := ""
	dt := colTypes[fieldName]
	if strings.Contains(strings.ToLower(dt), "timestamp") {
		s, ok := newValue.(string)
		if ok {
			t, err := toGenericTimestamp(s)
			if err != nil {
				return fmt.Errorf("error converting newValue timestamp: %w", err)
			}
			newValue = t
		}
		updateCast = "::" + dt
	}
	col := QuoteName(fieldName)
	sqlStatement := fmt.Sprintf( // #nosec G201 -- names quoted by Resolve and QuoteName, values bound
		"UPDATE %s SET %s = $1%s WHERE %s AND %s::text IS NOT DISTINCT FROM $2",
		rel.Target(), col, updateCast, where, col,
	)
	args := append([]any{newValue, old}, keyArgs...)

	tx, err := pg.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, sqlStatement, args...)
	if err != nil {
		return fmt.Errorf("error in update: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("error getting affected rows: %w", err)
	}
	switch {
	case rowsAffected == 0:
		return ErrChanged
	case rowsAffected != 1:
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
// CreateUpdateStatement is an UPDATE of the row whose key is in row, its
// values as the database has them now, guarded by the whole row as read:
// run after someone else changed the row, it updates nothing.
func (pg *Postgres) CreateUpdateStatement(ctx context.Context, tableName string, headers []string, row []any) (string, error) {
	if len(row) != len(headers) {
		return "", errors.New("no row selected")
	}
	rel, err := pg.Resolve(ctx, tableName)
	if err != nil {
		return "", err
	}
	pks, err := pg.relPrimaryKey(ctx, rel)
	if err != nil {
		return "", err
	}
	if len(pks) == 0 {
		return "", fmt.Errorf("no primary key columns found for table %s", rel.SQL)
	}
	pkVals := make([]any, len(pks))
	for i, pk := range pks {
		idx := slices.Index(headers, pk)
		if idx < 0 {
			return "", fmt.Errorf("column %s not found in headers", pk)
		}
		pkVals[i] = row[idx]
	}
	_, colTypes, where, args, err := pg.cellKey(ctx, tableName, pks, pkVals, 1)
	if err != nil {
		return "", err
	}
	now, rowText, err := pg.rowNow(ctx, rel, where, args, colTypes)
	if err != nil {
		return "", err
	}

	var sets, wheres []string
	for _, col := range headers {
		if !slices.Contains(pks, col) {
			sets = append(sets, fmt.Sprintf("    %s = %s", QuoteName(col), sqlValue(now[col], strings.ToLower(colTypes[col]))))
		}
	}
	if len(sets) == 0 {
		return "", errors.New("there are no columns to update")
	}
	for _, pk := range pks {
		if now[pk] == nil {
			wheres = append(wheres, QuoteName(pk)+" IS NULL")
			continue
		}
		wheres = append(wheres, QuoteName(pk)+" = "+sqlValue(now[pk], strings.ToLower(colTypes[pk])))
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
	guard := "  AND ROW(dbv_row.*)::text = " + QuoteLiteral(rowText) +
		"\n  -- the line above: the row as read; with it gone, a change made since is written over"
	return fmt.Sprintf("UPDATE %s AS dbv_row\nSET\n%s\nWHERE %s\n%s\n;", rel.Target(), setBlock, number(wheres, ""), guard), nil
}

// rowNow reads the row where picks: each column as the grid shows it, and
// the whole row as text.
func (pg *Postgres) rowNow(ctx context.Context, rel Rel, where string, args []any, colTypes map[string]string) (map[string]any, string, error) {
	q := "SELECT dbv_row.*, ROW(dbv_row.*)::text FROM " + rel.From() + " AS dbv_row WHERE " + where // #nosec G202 -- names quoted, values bound
	rows, err := pg.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, "", err
	}
	if !rows.Next() {
		err = rows.Err()
		if err == nil {
			err = errors.New("the row is gone: deleted, or its key changed")
		}
		return nil, "", err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	err = rows.Scan(ptrs...)
	if err != nil {
		return nil, "", err
	}
	out := make(map[string]any, len(cols)-1)
	for i, c := range cols[:len(cols)-1] {
		out[c] = displayValue(vals[i], colTypes[c])
	}
	text := fmt.Sprint(displayValue(vals[len(cols)-1], "text"))
	return out, text, rows.Err()
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
	return QuoteLiteral(s)
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
		err := rows.Scan(pointers...)
		if err != nil {
			return nil, nil, err
		}
		for i, col := range columnNames {
			record[col] = values[i]
		}
		records = append(records, record)
	}
	return records, columns, rows.Err()
}

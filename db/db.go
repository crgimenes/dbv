package db

import (
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
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/kevinburke/ssh_config"
	"golang.org/x/crypto/ssh"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	Storage   *Postgres
	ErrNoRows = errors.New("sql: no rows in result set")
)

type Postgres struct {
	DB      *sqlx.DB
	schemas []string
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
	Title string
	URL   string
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
	}

	pg.DB, err = open(dataBaseURL)
	return pg, err
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
func open(dbsource string) (*sqlx.DB, error) {
	if strings.HasPrefix(dbsource, "ssh+") {
		dbsource = strings.TrimPrefix(dbsource, "ssh+")
		u, err := url.Parse(dbsource)
		if err != nil {
			return nil, fmt.Errorf("failed to parse DSN: %w", err)
		}
		q := u.Query()

		sshUser := q.Get("sshuser")
		sshHost := q.Get("sshhost")
		sshPort := q.Get("sshport")
		sshKeyPath := q.Get("sshkey")
		localPort := q.Get("localport")
		sshalias := q.Get("sshalias")
		remoteTarget := q.Get("remotetarget")
		if remoteTarget == "" {
			remoteTarget = u.Host
		}

		if localPort == "" {
			return nil, fmt.Errorf("localport must be specified for SSH tunneling")
		}
		if sshPort == "" {
			sshPort = "22"
		}

		if sshalias != "" {
			configPath := os.ExpandEnv("$HOME/.ssh/config")
			f, err := os.Open(filepath.Clean(configPath))
			if err != nil {
				return nil, fmt.Errorf("failed to open SSH config: %w", err)
			}
			defer f.Close()
			sshCfg, err := ssh_config.Decode(f)
			if err != nil {
				return nil, fmt.Errorf("failed to decode SSH config: %w", err)
			}
			if sshUser == "" {
				sshUser, _ = sshCfg.Get(sshalias, "User")
				if sshUser == "" {
					sshUser = os.Getenv("USER")
				}
			}
			if sshHost == "" {
				hostname, _ := sshCfg.Get(sshalias, "Hostname")
				if hostname != "" {
					sshHost = hostname
				} else {
					sshHost = sshalias
				}
			}
			if q.Get("sshport") == "" {
				if port, _ := sshCfg.Get(sshalias, "Port"); port != "" {
					sshPort = port
				}
			}
			if sshKeyPath == "" {
				if identity, _ := sshCfg.Get(sshalias, "IdentityFile"); identity != "" {
					sshKeyPath = os.ExpandEnv(identity)
				}
			}
		}

		q.Del("sshuser")
		q.Del("sshhost")
		q.Del("sshport")
		q.Del("sshkey")
		q.Del("localport")
		q.Del("sshalias")
		q.Del("remotetarget")

		u.RawQuery = q.Encode()

		sshKeyPath, err = expandPath(sshKeyPath)
		if err != nil {
			return nil, fmt.Errorf("failed to expand SSH key path: %w", err)
		}

		key, err := os.ReadFile(filepath.Clean(sshKeyPath))
		if err != nil {
			return nil, fmt.Errorf("failed to read SSH key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("failed to parse SSH key: %w", err)
		}
		sshConfig := &ssh.ClientConfig{
			User: sshUser,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(), // TODO: Use a proper host key callback
			Timeout:         5 * time.Second,
		}
		sshConn, err := ssh.Dial("tcp", net.JoinHostPort(sshHost, sshPort), sshConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to dial SSH: %w", err)
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("localhost", localPort))
		if err != nil {
			return nil, fmt.Errorf("failed to start local listener: %w", err)
		}
		go func() {
			for {
				localConn, err := listener.Accept()
				if err != nil {
					log.Printf("failed to accept local connection: %v", err)
					continue
				}
				//remoteTarget := u.Host
				remoteConn, err := sshConn.Dial("tcp", remoteTarget)
				if err != nil {
					log.Printf("failed to dial remote target: %v", err)
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
		}()
		u.Host = net.JoinHostPort("localhost", localPort)
		dbsource = u.String()
	}

	db, err := sqlx.Open("pgx", dbsource)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(30)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func openBasicConn(dbsource string) (db *sqlx.DB, err error) {
	db, err = sqlx.Open("pgx", dbsource)
	if err != nil {
		return nil, err
	}
	err = db.Ping()
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(30)
	db.SetConnMaxLifetime(5 * time.Minute)
	return
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
	err := pg.DB.Close()
	if err != nil {
		log.Println(err)
	}
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
	defer stmt.Close()
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
            WHEN 'v' THEN 'view'
            WHEN 'm' THEN 'materialized view'
            ELSE c.relkind::text
        END AS table_type,
        pg_size_pretty(pg_total_relation_size(c.oid)) AS table_size,
        (
            SELECT string_agg(a.attname, ', ')
            FROM pg_index i
            JOIN pg_attribute a
                ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
            WHERE i.indrelid = c.oid AND i.indisprimary
        ) AS primary_key
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname IN (%s)
          AND c.relkind IN ('r', 'v', 'm')
        ORDER BY table_type,c.relname;`
	var tables []TableInfo
	schemas := "'" + strings.Join(pg.schemas, "','") + "'"
	err := pg.DB.Select(&tables, fmt.Sprintf(sqlStatement, schemas))
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
	const sqlStatement = `SELECT column_name, data_type
	FROM information_schema.columns
	WHERE table_name = $1;`
	var columns []ColumnInfo
	err := pg.DB.Select(&columns, sqlStatement, tableName)
	if err != nil {
		return nil, err
	}
	return columns, nil
}

func (pg *Postgres) ListRecords(
	tableName string,
	pkField string,
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
	var records []map[string]any
	if where != "" {
		where = "WHERE " + where
	}

	var totalRecords int
	err := pg.Get(&totalRecords, fmt.Sprintf(
		"SELECT COUNT(*) FROM %s %s",
		tableName,
		where))
	if err != nil {
		return nil, nil, 0, err
	}

	columns, err := pg.ListColumns(tableName)
	if err != nil {
		return nil, nil, 0, err
	}

	separator := ""
	fields := ""
	for i, column := range columns {
		if i > 0 {
			separator = ", "
		}
		fields += separator + fmt.Sprintf("%q", column.ColumnName)
	}

	if orderBy == "" {
		orderBy = pkField
	}
	if orderBy != "" {
		orderBy = "ORDER BY " + orderBy
	}

	sqlStatement := fmt.Sprintf(
		"SELECT %s\nFROM\n%s %s %s\nOFFSET %d\nLIMIT %d",
		fields,
		tableName,
		where,
		orderBy,
		offset,
		limit,
	)

	rows, err := pg.DB.Queryx(sqlStatement)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("error executing query: %v %v", sqlStatement, err)
	}

	for rows.Next() {
		record := make(map[string]any)
		values := make([]any, len(columns))
		for i := range values {
			values[i] = new(any)
		}
		err = rows.Scan(values...)
		if err != nil {
			return nil, nil, 0, err
		}

		for i, column := range columns {
			rawVal := *(values[i].(*any))
			var conv any
			lowerType := strings.ToLower(column.DataType)
			switch v := rawVal.(type) {
			case nil:
				conv = "NULL"
			case []byte:
				strVal := strings.TrimSpace(string(v))
				if strVal == "" {
					conv = "NULL"
				} else if lowerType != "" && strings.Contains(lowerType, "numeric") {
					if f, err := strconv.ParseFloat(strVal, 64); err == nil {
						conv = fmt.Sprintf("%.2f", f)
					} else {
						conv = strVal
					}
				} else {
					conv = strVal
				}
			case time.Time:
				conv = v.Format(time.RFC3339Nano)
			default:
				s := fmt.Sprintf("%v", v)
				if s == "<nil>" {
					conv = "NULL"
				} else if lowerType != "" && strings.Contains(lowerType, "numeric") {
					if f, err := strconv.ParseFloat(s, 64); err == nil {
						conv = fmt.Sprintf("%.2f", f)
					} else {
						conv = s
					}
				} else {
					conv = s
				}
			}
			record[column.ColumnName] = conv
		}
		records = append(records, record)
	}
	return records, columns, totalRecords, nil
}

func (pg *Postgres) GetPrimaryKeyColumns(tableName string) ([]string, error) {
	const query = `
        SELECT kcu.column_name
        FROM information_schema.table_constraints tco
        JOIN information_schema.key_column_usage kcu
             ON kcu.constraint_name = tco.constraint_name
             AND kcu.constraint_schema = tco.constraint_schema
        WHERE tco.constraint_type = 'PRIMARY KEY'
          AND tco.table_name = $1
        ORDER BY kcu.ordinal_position
    `
	rows, err := pg.DB.Query(query, tableName)
	if err != nil {
		return nil, fmt.Errorf("erro ao consultar PK: %w", err)
	}
	defer rows.Close()

	var pkColumns []string
	for rows.Next() {
		var colName string
		if err := rows.Scan(&colName); err != nil {
			return nil, fmt.Errorf("erro ao varrer resultado PK: %w", err)
		}
		pkColumns = append(pkColumns, colName)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("erro final ao varrer PK: %w", err)
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
	columns, err := pg.ListColumns(tableName)
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
		updateCast = fmt.Sprintf("::%s", dt)
	}

	whereClauses := make([]string, len(pkField))
	for i, field := range pkField {
		whereClauses[i] = fmt.Sprintf("\"%s\" = $%d", field, i+2)
	}
	whereClauseStr := strings.Join(whereClauses, " AND ")

	sqlStatement := fmt.Sprintf(
		"UPDATE \"%s\" SET \"%s\" = $1%s WHERE %s",
		tableName,
		fieldName,
		updateCast,
		whereClauseStr,
	)

	args := make([]any, 0, len(pkValue)+1)
	args = append(args, newValue)
	args = append(args, pkValue...)

	result, err := pg.DB.Exec(sqlStatement, args...)
	if err != nil {
		return fmt.Errorf("error in update: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("error getting affected rows: %w", err)
	}
	if rowsAffected == 0 {
		return errors.New("no rows updated")
	}

	return nil
}

func (pg *Postgres) QueryRowContext(
	ctx context.Context,
	query string,
	args ...any,
) *sql.Row {
	return pg.DB.QueryRowContext(ctx, query, args...)
}

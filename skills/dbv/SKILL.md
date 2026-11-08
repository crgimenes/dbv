---
name: dbv
description: Inspect PostgreSQL databases configured in dbv (init.filo in ~/Library/Application Support/dbv on macOS, ~/.config/dbv on Linux) from the command line — list connections, tables and columns, read rows, run read-only SQL, generate a Go struct or an INSERT template, and, only when the user approves, change data. Use whenever you need to look at data or schema in one of the user's databases instead of guessing, or instead of calling psql with a password you would have to handle.
---

# dbv

`dbv COMMAND [flags] [args]` answers once and exits. The reading commands
cannot write: `rows` and `query` run one statement inside a `READ ONLY`
transaction, so the server refuses any write (`cannot execute ... in a
read-only transaction`). Credentials stay in the config file; you never see
or pass a password.

## Commands

```
dbv dbs                          connections: n;title;url (password masked);views
dbv tables -db N                 name;type;size;primary_key
dbv columns -db N TABLE          name;type, in table order
dbv rows -db N [-where W] [-order O] [-limit 1000] [-offset 0] TABLE
dbv query -db N [-limit 1000] 'SELECT ...'     (or SQL on stdin)
dbv struct -db N TABLE           Go struct
dbv insert -db N TABLE           INSERT template (not executed)
dbv exec -db N -write 'SQL'      change data, all in one transaction
dbv update -db N -write -key id=7 TABLE COLUMN VALUE
                                 one cell; the key is the whole primary key
```

Flags go **before** the positional arguments (Go flag parsing stops at the
first non-flag). Common: `-db` (title or 1-based number; optional when only
one connection exists), `-json`, `-timeout 30s`, `-config FILE` (another
init.filo, such as a throwaway test database).

## How to use it as an agent

- Start with `dbv dbs` to find the connection, then pass `-db N`.
- **Use `-json` to read results.** Shape for rows/query:
  `{"columns":[{"name","type"}],"rows":[[...]],"truncated":bool}`. Rows are
  arrays in column order; NULL is `null`; json/jsonb arrive as nested JSON;
  timestamps RFC 3339, dates `YYYY-MM-DD`; numeric as string (no precision
  loss).
- `truncated: true` (and a line on stderr) means there were more rows than
  `-limit`. Narrow the query rather than raising the limit blindly.
- `-where`/`-order` take raw SQL fragments without the keyword.
- Exit 0 success, 1 runtime failure (message on stderr, prefixed
  `dbv COMMAND:`), 2 usage error.
- Slow or failing? Add `-debug`: one `dbv event: OP ms=N key=value` line
  per step on stderr (connect, query, command), stdout unchanged.
- A connection whose title says production is the user's production
  database: read only what the task needs.
- **`exec` and `update` change data. Run them only after the user approved
  that exact change in this conversation**; show the SQL or the cell and
  value first. Never on a connection whose title says production unless
  the user named it for this change. Without `-write` they refuse (exit 2):
  that refusal is the default, not an obstacle to work around.
- `update` writes only when `-key` matches exactly one row; `exec` rolls
  everything back when any statement fails. Output is the rows affected
  (`-json`: `{"rows_affected":N}`).

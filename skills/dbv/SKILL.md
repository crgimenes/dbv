---
name: dbv
description: Inspect PostgreSQL databases configured in dbv (~/.config/dbv/init.filo) from the command line — list connections, tables and columns, read rows, run read-only SQL, generate a Go struct or an INSERT template. Use whenever you need to look at data or schema in one of the user's databases instead of guessing, or instead of calling psql with a password you would have to handle.
---

# dbv

`dbv COMMAND [flags] [args]` answers once and exits. Every command is
read-only: SQL runs inside a `READ ONLY` transaction, so the server refuses
any write (`cannot execute ... in a read-only transaction`). Credentials stay
in the config file; you never see or pass a password.

## Commands

```
dbv dbs                          connections: n;title;url (password masked);views
dbv tables -db N                 name;type;size;primary_key
dbv columns -db N TABLE          name;type, in table order
dbv rows -db N [-where W] [-order O] [-limit 1000] [-offset 0] TABLE
dbv query -db N [-limit 1000] 'SELECT ...'     (or SQL on stdin)
dbv struct -db N TABLE           Go struct
dbv insert -db N TABLE           INSERT template (not executed)
```

Flags go **before** the positional arguments (Go flag parsing stops at the
first non-flag). Common: `-db` (title or 1-based number; optional when only
one connection exists), `-json`, `-timeout 30s`.

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
- A connection whose title says production is the user's production
  database: read only what the task needs.
- Writing (update a cell, exec) is not available from the CLI yet; ask the
  user to do it in the interactive `dbv`.

# dbv – Terminal-Based Database Viewer

⚠️ This software is currently under active development. Until version 1.0 is released, significant changes may occur, and features may be altered or removed without prior notice. Use in production environments with caution.

**dbv** is a fast and user-friendly terminal database viewer and editor that connects to SQL databases (with a focus on PostgreSQL). It provides a text-based interface for listing tables, browsing records, editing cells, generating SQL INSERT statements, creating Go struct definitions, exporting data to JSON, and more.

## Features

- **List Tables & Views**: Displays tables, partitioned tables, views, and materialized views along with primary keys and approximate sizes.
- **Data Browsing**: Scroll through table records with built-in pagination.
- **Cell Editing**: Edit cell values directly with automatic conversions for timestamps and numeric types.
- **Insert Statement Generation**: Create template SQL INSERT statements that include default values and respect primary key order.
- **Go Struct Generation**: Generate Go struct definitions based on table columns (mapping PostgreSQL types to Go types).
- **SQL**: `:sql` writes SQL in `$EDITOR` and runs it, several statements in one transaction (a `BEGIN` left open is rolled back); rows that come back show as a grid. Text left unchanged runs nothing.
- **Export**: `:export FILE.csv` or `FILE.json` writes every row of the current filter; `y`/`Y` copy a cell or a row to the clipboard.
- **User Views**: `.sql` files in a views directory appear in the table list; `{{name:type:title:order}}` tokens become a form to fill in before the query runs (read only).
- **SSH Tunnel**: `ssh+postgres://` URLs reach a database through ssh, reading `~/.ssh/config`.
- **Command Line for Scripts and Agents**: one-shot, read-only commands with text or JSON output.
- **Filo Configuration**: Configure database connections using [Filo](https://github.com/crgimenes/filo) scripts (e.g., `init.filo`).
- **Keyboard Shortcuts**: Navigate, filter, and execute commands with quick key bindings.

## Requirements

- **Go 1.27.1** or later (the version in `go.mod`), to build from source

## Installation

### 1. Building from Source

1. Clone the repository:
   ```bash
   git clone https://github.com/crgimenes/dbv.git
   cd dbv
   ```
2. Build the binary:
   ```bash
   go build -o dbv
   ```
3. Move the binary to a directory in your PATH (for example, `/usr/local/bin`):
   ```bash
   mv dbv /usr/local/bin/
   ```
4. Run the application by simply typing:
   ```bash
   dbv
   ```

### 2. Installing via GitHub Releases

You can also download the pre-built binaries from the [GitHub Releases](https://github.com/crgimenes/dbv/releases) page. After downloading the appropriate binary for your operating system, place it in a directory that's in your PATH.

## Configuration

By default, dbv loads database connections from a [Filo](https://github.com/crgimenes/filo) file named `init.filo`. Place this file in the platform's config directory — `~/Library/Application Support/dbv/init.filo` on macOS, `~/.config/dbv/init.filo` on Linux (or `$XDG_CONFIG_HOME/dbv`) — or in the current directory. Each connection is declared with `(database url [title] [views])`: the URL is required, the title is optional (it defaults to the URL with the password masked), and an optional third argument points to a directory of custom views. For example:

```scheme
(database "postgres://username:password@localhost:5432/mydb?sslmode=disable" "LocalDB")
(database "postgres://user:pass@server:5432/otherdb?sslmode=disable" "OtherDB")
```

> **Note:** `database` is not a built-in Filo operator — it is an application-specific function that dbv registers (via `RegisterBuiltin`) before evaluating the config file. Each `(database ...)` call appends one connection, so the file is simply a sequence of these calls, one per line.

Files named on the command line (`dbv other.filo`) replace the default
config. Unless the URL sets them, dbv adds `connect_timeout=10` and
`statement_timeout=60s`, so an unreachable server or a held lock cannot
freeze the interface.

To go through ssh, prefix the scheme with `ssh+` and say where to listen
locally; `sshalias` takes user, host, port and key from `~/.ssh/config`
(`sshuser`, `sshhost`, `sshport`, `sshkey` override them, and
`remotetarget` is the database address as seen from the ssh host):

```scheme
(database "ssh+postgres://user:pass@db:5432/app?sshalias=bastion&localport=5433&sslmode=disable" "App via bastion")
```

A user view is `NAME.sql` in the views directory (the third argument of
`database`). Tokens `{{name:type:title:order}}` are asked for in a form and
replaced by what is typed; only `name` is required:

```sql
SELECT id, name FROM users WHERE id BETWEEN {{lo::From id:1}} AND {{hi::To id:2}}
```

## Usage

### Launching dbv

After installing, simply run:
```bash
dbv
```

### Functionality

- **Table Listing & Filtering**: Easily navigate and filter available tables.
- **Data Viewing & Editing**: Scroll through records, edit cells, and generate SQL/struct/JSON using quick commands.
- **Commands**: `:where COND`, `:order`, `:show` (bring hidden columns back), `:insert`, `:struct`, `:json`, `:update` and `:export FILE` on a table; `:clear`, `:reload`, `:rename` and `:quit` on the table list; `:sql` on both. `↑↓` on the command line bring back what was typed on that screen. On a table's command line, `Tab` completes the command and, in a `:where`, column names and SQL words (`and`, `like`, `between`, `is null`...); when several match, a bar opens above the line: `Tab`/`→` and `Shift-Tab`/`←` walk it, `Enter` takes the word, `Esc` goes back to what was typed. The grid's cursor follows the column chosen, so its data shows while the condition is written. `Tab` on an empty line lists every command; the table list's `:` completes its commands too, and `Tab` in a `/` filter (table list, `-s` picker) completes table names, the list following the bar.
- **Long queries**: a spinner shows after a tenth of a second, and `Esc` cancels the query on the server.
- **Selector mode**: `dbv -s` picks a table or column name (Space), a Go struct (`S`) or an INSERT (`I`) and writes it to `/tmp/dbv_output.txt`, for an editor running beside dbv to insert.

### Command line (scripts and agents)

`dbv COMMAND [flags] [args]` answers once and exits, without the
interactive interface. The reading commands never write: `rows` and
`query` run a single statement inside a `READ ONLY` transaction, and the
others read the catalog with fixed queries. `exec` and `update` change data
and refuse to run without `-write`.

```bash
dbv dbs                                   # configured connections
dbv tables -db 1                          # tables and views
dbv columns -db 1 users
dbv rows -db 1 -where 'active' -limit 20 users
dbv query -db 1 -json 'SELECT id, name FROM users LIMIT 5'
echo 'SELECT now()' | dbv query -db 1
dbv struct -db 1 users                    # Go struct
dbv insert -db 1 users                    # INSERT template
dbv exec -db 1 -write 'DELETE FROM sessions WHERE expires < now()'
dbv update -db 1 -write -key id=7 users name 'Ana Maria'
dbv tables -config ./test.filo            # another config file
```

`update` sets one cell and changes nothing unless `-key` names the whole
primary key and matches exactly one row; `exec` runs everything in one
transaction, so a failing statement takes the earlier ones back, and it
refuses SQL that opens or ends a transaction of its own (BEGIN, COMMIT,
ROLLBACK), which would break that promise.

Text output is `;`-separated with a header line; `-json` gives
`{"columns":[...],"rows":[[...]],"truncated":bool}`. `dbv -h` lists every
flag. An agent skill lives in [skills/dbv/SKILL.md](skills/dbv/SKILL.md).

### Diagnostics

`-debug` writes one line per step to stderr, `dbv event: OP ms=N key=value
...` (connect, load, query, view, update_cell, exec, and in the interface
each key with the time it took to handle and to paint). The interface owns
the terminal, so redirect it there:

```bash
dbv -debug 2>dbv.log          # and `tail -f dbv.log` in another window
dbv rows -debug -limit 5 users
```

### Interface

The interactive screens use [filo-term-go](https://github.com/crgimenes/filo-term-go),
the same look as `edt` and the BBS: a frame, a legend of the keys that work
on each screen, and a note line at the bottom. `?` shows the help of the
screen you are on.

### Keyboard Shortcuts

Table list:

- `↑↓` / `j k`, `PgUp PgDn`, `Home End`: move
- `Enter`: open the table or view
- `/`: filter by regular expression, live (`Esc` clears)
- `y`: copy the table name to the clipboard
- `:`: command line; `?`: help
- `Esc`: back to the database menu; `q`: quit

Table data:

- `↑↓←→` / `h j k l`, `PgUp PgDn`: move; `g G`: first and last row; `Home End`: first and last column
- `Enter`, `e`, `v`: edit the cell in `$EDITOR` (saved to the table; needs a primary key)
- `p`: view the cell in `$PAGER`
- `y`, `Y`: copy the cell, or the row as tab-separated text, to the clipboard (through the terminal, OSC 52; works over ssh, and in tmux with `set -g set-clipboard on`)
- `o`, `O`: order by this column, ascending or descending; the same key again drops that column from the order (the others stay), the other key flips its direction
- On a user view or a `:sql` result, `o`/`O` sort the rows in memory (NULLs as PostgreSQL puts them; `:order` restores how they came), and `:sql` starts from the view's SQL with its parameters filled
- `x`: hide this column (`:show` brings them all back); `:sql` on a table starts as the grid's SELECT: its shown columns, `:where` and order
- `I`, `S`, `J`, `U`: INSERT template, Go struct, JSON of the row, UPDATE of the row (INSERT and UPDATE run when saved changed)
- `:`: command line; `?`: help
- `Esc`, `q`: back to the table list

## Examples

### Building from Source

```bash
git clone https://github.com/crgimenes/dbv.git
cd dbv
go build -o dbv
mv dbv /usr/local/bin/
mkdir -p ~/.config/dbv    # macOS: ~/Library/Application\ Support/dbv
cat <<EOF > ~/.config/dbv/init.filo
(database "postgres://postgres:mysecret@localhost:5432/mytestdb?sslmode=disable" "LocalTest")
EOF
dbv
```

### Using GitHub Releases

1. Download the appropriate binary from the [GitHub Releases](https://github.com/crgimenes/dbv/releases) page.
2. Place the binary in a directory that is in your PATH.
3. Run the application by typing:
   ```bash
   dbv
   ```


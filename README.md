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
- **Commands**: `:where COND`, `:order`, `:show` (bring hidden columns back), `:insert`, `:struct`, `:json`, `:update` and `:export FILE` on a table; `:clear`, `:reload`, `:rename` and `:quit` on the table list; `:sql` on both. `↑↓` on the command line bring back what was typed on that screen, in this run or an earlier one, and a view's form opens with the values it last ran with. The lines are kept (0600, at most 100 per prompt) in `~/Library/Application Support/dbv/history.json` on macOS, `$XDG_STATE_HOME/dbv/history.json` (`~/.local/state`) on Linux and the BSDs. On a table's command line, `Tab` completes the command and, in a `:where`, column names and SQL words (`and`, `like`, `between`, `is null`...); when several match, a bar opens above the line: `Tab`/`→` and `Shift-Tab`/`←` walk it, `Enter` takes the word, `Esc` goes back to what was typed. The grid's cursor follows the column chosen, so its data shows while the condition is written. `Tab` on an empty line lists every command; the table list's `:` completes its commands too, and `Tab` in a `/` filter (table list, `-s` picker) completes table names, the list following the bar.
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

Everywhere, as in other terminal programs: `Ctrl-C` quits (on a command
line it drops the line), `Ctrl-Z` suspends (`fg` resumes), `Ctrl-L`
redraws, and `Ctrl-F`/`Ctrl-B` page like `PgDn`/`PgUp`.

Table data:

- `↑↓←→` / `h j k l`, `PgUp PgDn`: move; `g G`: first and last row; `Home End`: first and last column
- `Enter`, `p`: view the cell in `$PAGER`
- `e`, `v`: edit the cell in `$EDITOR` (saved to the table; needs a primary key)
- `/`, `n`, `N`: search the rows read so far (regular expression), next, previous
- `y`, `Y`: copy the cell, or the row as tab-separated text, to the clipboard (through the terminal, OSC 52; works over ssh, and in tmux with `set -g set-clipboard on`)
- `o`, `O`: order by this column, ascending or descending; the same key again drops that column from the order (the others stay), the other key flips its direction
- On a user view or a `:sql` result, `o`/`O` sort the rows in memory (NULLs as PostgreSQL puts them; `:order` restores how they came), and `:sql` starts from the view's SQL with its parameters filled
- `n` or `:view NAME` on the table list: a new view of yours, written in `$EDITOR` and kept in the connection's views directory (the one its config names, else `views/<title>` under dbv's config directory); `:wq` keeps it, `:q!` keeps nothing. `e` edits the view under the cursor. `:view NAME` on a grid starts the view from the grid's SELECT
- `x`: hide this column (`:show` brings them all back); `:sql` on a table starts as the grid's SELECT: its shown columns, `:where` and order
- `I`, `S`, `J`, `U`: INSERT template, Go struct, JSON of the row, UPDATE of the row (INSERT and UPDATE run when saved changed)
- `:`: command line; `?`: help
- `Esc`, `q`, `:q`: back to the table list

## SQL files in Neovim

`dbv lsp` is a language server for `.sql` files. It completes table and
view names after `FROM`, `JOIN`, `UPDATE` and `INTO`; after `alias.` only
the columns of the table that alias stands for (`select * from clientes cl
where cl.` offers the columns of `clientes`, even when the `FROM` comes
after the cursor); a schema's tables after `schema.`; and the statement's
columns and SQL words elsewhere. Statements are cut at `;` or a blank line.

The database is the one dbv is on when it opened the editor (`:sql`, `e`,
a view); a file opened on its own may name one in the comment lines that
open it, by title or number as in `-db`:

```sql
-- dbv: db=2
select * from clientes cl where cl.
```

With neither, or when the database cannot be reached, it completes SQL
words alone and says why once; a loose `.sql` never breaks. The catalog is
read once per database, in one read-only query, in the background.

With coc.nvim, in `coc-settings.json`:

```json
"languageserver": { "dbv": { "command": "dbv", "args": ["lsp"], "filetypes": ["sql"] } }
```

`K` (hover) shows a column's type or a table's columns, and `:CocCommand`
(`dbv.reload`) reads the catalog again; a save does too, a minute after
the last read. With Neovim's own client (0.11 or later):

```lua
vim.lsp.config('dbv', { cmd = { 'dbv', 'lsp' }, filetypes = { 'sql' } })
vim.lsp.enable('dbv')
```

### Running a statement

`neovim/dbv.lua` runs the statement under the cursor, or the selection,
and shows its rows in dbv's grid in a split below; `q` closes it.
`:DBVRun` runs the statement at the cursor; from visual mode
(`:'<,'>DBVRun`) the selection; with a range of lines, those lines. It maps
no keys; to have one, `vim.keymap.set("n", "<leader>t", "<cmd>DBVRun<CR>")`.
Load it from your config:

```lua
-- ~/.config/nvim/lua/dbv.lua linked to the repository's neovim/dbv.lua
require "dbv"
```

`:DBVInfo` opens a float (`q` closes it) with the connection the buffer
gets and why (the dbv nvim runs under, the file's `-- dbv: db=` line, the
only one configured), its URL without the password, the views directory,
the server's version, user, database, size and table count, and the list of
connections; it is `dbv info FILE` (`dbv info -h`).

It calls `dbv show -at OFFSET FILE` (`dbv show -h`) on a copy of the
buffer; the connection is chosen as for completion. Reads run at once in a
read-only session, which the server enforces, so a `WITH` that deletes or a
function that writes is caught too; anything that writes asks `y/N`
first. The cursor just past a `;` runs that statement, as in DBeaver.

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


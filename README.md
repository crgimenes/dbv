# dbv – Terminal-Based Database Viewer

⚠️ This software is currently under active development. Until version 1.0 is released, significant changes may occur, and features may be altered or removed without prior notice. Use in production environments with caution.

**dbv** is a fast and user-friendly terminal database viewer and editor that connects to SQL databases (with a focus on PostgreSQL). It provides a text-based interface for listing tables, browsing records, editing cells, generating SQL INSERT statements, creating Go struct definitions, exporting data to JSON, and more.

## Features

- **List Tables & Views**: Displays tables, partitioned tables, views, and materialized views along with primary keys and approximate sizes.
- **Data Browsing**: Scroll through table records with built-in pagination.
- **Cell Editing**: Edit cell values directly with automatic conversions for timestamps and numeric types.
- **Insert Statement Generation**: Create template SQL INSERT statements that include default values and respect primary key order.
- **Go Struct Generation**: Generate Go struct definitions based on table columns (mapping PostgreSQL types to Go types).
- **JSON Export**: Convert table rows into JSON structures.
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
- **Commands**: `:where COND`, `:order`, `:insert`, `:struct`, `:json` and `:update` on a table; `:clear`, `:reload`, `:rename` and `:quit` on the table list.
- **Selector mode**: `dbv -s` picks a table or column name (Space), a Go struct (`S`) or an INSERT (`I`) and writes it to `/tmp/dbv_output.txt`, for an editor running beside dbv to insert.

### Command line (scripts and agents)

`dbv COMMAND [flags] [args]` answers once and exits, without the
interactive interface. No command writes: `rows` and `query` run a single
statement inside a `READ ONLY` transaction, and the others read the catalog
with fixed queries.

```bash
dbv dbs                                   # configured connections
dbv tables -db 1                          # tables and views
dbv columns -db 1 users
dbv rows -db 1 -where 'active' -limit 20 users
dbv query -db 1 -json 'SELECT id, name FROM users LIMIT 5'
echo 'SELECT now()' | dbv query -db 1
dbv struct -db 1 users                    # Go struct
dbv insert -db 1 users                    # INSERT template
```

Text output is `;`-separated with a header line; `-json` gives
`{"columns":[...],"rows":[[...]],"truncated":bool}`. `dbv -h` lists every
flag. An agent skill lives in [skills/dbv/SKILL.md](skills/dbv/SKILL.md).

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
- `:`: command line; `?`: help
- `Esc`: back to the database menu; `q`: quit

Table data:

- `↑↓←→` / `h j k l`, `PgUp PgDn`: move; `g G`: first and last row; `Home End`: first and last column
- `Enter`, `e`, `v`: edit the cell in `$EDITOR` (saved to the table; needs a primary key)
- `p`: view the cell in `$PAGER`
- `o`, `O`: order by this column, ascending or descending
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


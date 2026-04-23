# dbv – Terminal-Based Database Viewer

⚠️ This software is currently under active development. Until version 1.0 is released, significant changes may occur, and features may be altered or removed without prior notice. Use in production environments with caution.

**dbv** is a fast and user-friendly terminal database viewer and editor that connects to SQL databases (with a focus on PostgreSQL). It provides a text-based interface for listing tables, browsing records, editing cells, generating SQL INSERT statements, creating Go struct definitions, exporting data to JSON, and more.

## Features

- **List Tables & Views**: Displays tables, views, and materialized views along with primary keys and approximate sizes.
- **Data Browsing**: Scroll through table records with built-in pagination.
- **Cell Editing**: Edit cell values directly with automatic conversions for timestamps and numeric types.
- **Insert Statement Generation**: Create template SQL INSERT statements that include default values and respect primary key order.
- **Go Struct Generation**: Generate Go struct definitions based on table columns (mapping PostgreSQL types to Go types).
- **JSON Export**: Convert table rows into JSON structures.
- **Lua Configuration**: Configure database connections using Lua scripts (e.g., `init.lua`).
- **Keyboard Shortcuts**: Navigate, filter, and execute commands with quick key bindings.

## Requirements

- **Go 1.26** or later

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

By default, dbv loads database connections from a Lua file named `init.lua`. Place this file in either `~/.config/dbv/init.lua` or in the current directory. For example:

```lua
DataBases = {
    {
        title = "LocalDB",
        url   = "postgres://username:password@localhost:5432/mydb?sslmode=disable"
    },
    {
        title = "OtherDB",
        url   = "postgres://user:pass@server:5432/otherdb?sslmode=disable"
    }
}
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
- **Commands**: Use `:where`, `:insert`, `:struct`, and `:json` to interact with data.

### Keyboard Shortcuts

- `h`, `j`, `k`, `l`: Navigation
- `e`, `v`, `p`: Edit/view the current cell
- `:`: Open the command prompt
- `q` or `Esc`: Exit

## Examples

### Building from Source

```bash
git clone https://github.com/crgimenes/dbv.git
cd dbv
go build -o dbv
mv dbv /usr/local/bin/
mkdir -p ~/.config/dbv
cat <<EOF > ~/.config/dbv/init.lua
DataBases = {
    {
        title = "LocalTest",
        url   = "postgres://postgres:mysecret@localhost:5432/mytestdb?sslmode=disable"
    }
}
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


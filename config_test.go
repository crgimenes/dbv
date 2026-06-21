package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadDBConfigs verifies that a Filo config file is parsed into the
// expected ordered list of database configurations, covering an explicit
// title, the masked-URL title default, and an optional views directory.
func TestLoadDBConfigs(t *testing.T) {
	viewsDir := t.TempDir()

	cfg := `(database "postgres://u:secret@localhost:5432/mydb" "LocalDB")
(database "postgres://u:secret@server:5432/otherdb")
(database "postgres://u:secret@host:5432/withviews" "WithViews" "` + viewsDir + `")`

	dir := t.TempDir()
	path := filepath.Join(dir, "init.filo")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	configs := loadDBConfigs(path)

	if len(configs) != 3 {
		t.Fatalf("got %d configs, want 3: %+v", len(configs), configs)
	}

	// 1) explicit title is preserved, order is kept.
	if configs[0].Title != "LocalDB" {
		t.Fatalf("configs[0].Title = %q, want %q", configs[0].Title, "LocalDB")
	}
	if configs[0].URL != "postgres://u:secret@localhost:5432/mydb" {
		t.Fatalf("configs[0].URL = %q", configs[0].URL)
	}

	// 2) missing title defaults to the masked URL (password hidden).
	wantMasked := "postgres://u:...@server:5432/otherdb"
	if configs[1].Title != wantMasked {
		t.Fatalf("configs[1].Title = %q, want masked %q", configs[1].Title, wantMasked)
	}

	// 3) views path is resolved to an absolute directory.
	wantViews, _ := filepath.Abs(viewsDir)
	if configs[2].Title != "WithViews" {
		t.Fatalf("configs[2].Title = %q, want %q", configs[2].Title, "WithViews")
	}
	if configs[2].ViewsPath != wantViews {
		t.Fatalf("configs[2].ViewsPath = %q, want %q", configs[2].ViewsPath, wantViews)
	}
}

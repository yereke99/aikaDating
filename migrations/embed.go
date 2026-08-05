package migrations

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.up.sql
var files embed.FS

// UsersUp contains the original users table. Kept exported because it is the schema every test
// fixture and the store's own documentation refers to.
//
//go:embed 000001_create_users.up.sql
var UsersUp string

// Statements returns every forward migration in filename order. Each file is written to be safe to
// re-run — `IF NOT EXISTS` for schema, `NOT EXISTS` guards for data — so applying the full list on
// every start needs no migration bookkeeping table.
func Statements() ([]string, error) {
	entries, err := fs.Glob(files, "*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)
	statements := make([]string, 0, len(entries))
	for _, name := range entries {
		content, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		if strings.TrimSpace(string(content)) == "" {
			continue
		}
		statements = append(statements, string(content))
	}
	return statements, nil
}

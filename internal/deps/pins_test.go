package deps

import (
	"database/sql"
	"testing"
)

func TestSQLiteDriverRegistered(t *testing.T) {
	t.Parallel()
	for _, name := range sql.Drivers() {
		if name == "sqlite" {
			return
		}
	}
	t.Fatal("sqlite driver was not registered")
}

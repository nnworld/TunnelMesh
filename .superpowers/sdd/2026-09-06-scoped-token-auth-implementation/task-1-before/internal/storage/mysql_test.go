package storage

import (
	"context"
	"os"
	"testing"
)

func TestMySQLRepositoryContract(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	db, err := OpenMySQL(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runRepositoryContract(t, db)
}

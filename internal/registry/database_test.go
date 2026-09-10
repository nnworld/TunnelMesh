package registry

import (
	"context"
	"os"
	"testing"

	"github.com/tunnelmesh/tunnelmesh/internal/storage"
)

func TestDatabaseRegistryContract(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, ":memory:", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runRegistryContract(t, func(*testing.T) NodeRegistry { return NewDatabaseRegistry(db) })
}

func TestDatabaseAgentConnections(t *testing.T) {
	db, err := storage.Open(context.Background(), storage.DriverSQLite, ":memory:", true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runConnectionRegistryContract(t, func(*testing.T) connectionRegistry { return NewDatabaseRegistry(db) })
}

func TestMySQLDatabaseRegistryContract(t *testing.T) {
	dsn := os.Getenv("TUNNELMESH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TUNNELMESH_TEST_MYSQL_DSN is not set")
	}
	db, err := storage.OpenMySQL(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	runRegistryContract(t, func(*testing.T) NodeRegistry { return NewDatabaseRegistry(db) })
}

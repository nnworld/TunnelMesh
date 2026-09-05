// Package migrations exposes the single portable schema used by every storage driver.
package migrations

import _ "embed"

// DDL is intentionally embedded once here because go:embed cannot traverse a
// parent directory from internal/storage.
//
//go:embed ddl.sql
var DDL string

// Package migrations exposes the single portable schema used by every storage driver.
package migrations

import _ "embed"

// DDL is intentionally embedded once here because go:embed cannot traverse a
// parent directory from internal/storage.
//
//go:embed ddl.sql
var DDL string

//go:embed incremental/v0005_to_v0006/mysql.sql
var V5ToV6MySQL string

//go:embed incremental/v0005_to_v0006/sqlite.sql
var V5ToV6SQLite string

//go:embed incremental/v0006_to_v0007/mysql.sql
var V6ToV7MySQL string

//go:embed incremental/v0006_to_v0007/sqlite.sql
var V6ToV7SQLite string

//go:embed incremental/v0007_to_v0008/mysql.sql
var V7ToV8MySQL string

//go:embed incremental/v0007_to_v0008/sqlite.sql
var V7ToV8SQLite string

//go:embed incremental/v0008_to_v0009/mysql.sql
var V8ToV9MySQL string

//go:embed incremental/v0008_to_v0009/sqlite.sql
var V8ToV9SQLite string

//go:embed incremental/v0009_to_v0010/mysql.sql
var V9ToV10MySQL string

//go:embed incremental/v0009_to_v0010/sqlite.sql
var V9ToV10SQLite string

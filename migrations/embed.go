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

//go:embed incremental/v0010_to_v0011/mysql.sql
var V10ToV11MySQL string

//go:embed incremental/v0010_to_v0011/sqlite.sql
var V10ToV11SQLite string

//go:embed incremental/v0011_to_v0012/mysql.sql
var V11ToV12MySQL string

//go:embed incremental/v0011_to_v0012/sqlite.sql
var V11ToV12SQLite string

//go:embed incremental/v0012_to_v0013/mysql.sql
var V12ToV13MySQL string

//go:embed incremental/v0012_to_v0013/sqlite.sql
var V12ToV13SQLite string

//go:embed incremental/v0013_to_v0014/mysql.sql
var V13ToV14MySQL string

//go:embed incremental/v0013_to_v0014/sqlite.sql
var V13ToV14SQLite string

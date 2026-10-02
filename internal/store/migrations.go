package store

import _ "embed"

//go:embed schema.sql
var schemaV1 string

// migrations 按顺序执行的迁移脚本。新增结构变更时在末尾追加一段 SQL，
// 已执行过的版本不会重复执行（版本号记录在 schema_meta 表）。
var migrations = []string{
	schemaV1,
}

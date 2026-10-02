package store

import _ "embed"

//go:embed schema.sql
var schemaV1 string

// migrationV2 默认币种与品牌名调整。
//
// 背景：settings 表只在首次启动时写入（ON CONFLICT DO NOTHING），
// 之后每次启动都读库里的值。所以把代码里的默认值从"加元"改成"人民币"
// 对已经建过库的实例是无效的——必须显式迁移。
//
// 这里只在"从未改过这两项"时才覆盖，用户自己设置过的币种 / 公司名不会被动。
const migrationV2 = `
UPDATE settings
   SET currency = 'CNY',
       currency_symbol = '¥'
 WHERE currency = 'CAD'
   AND currency_symbol = 'C$';

UPDATE settings
   SET company_name = 'SoulFound'
 WHERE company_name IN ('加拿大冰酒', '加拿大冰酒 ERP');
`

// migrations 按顺序执行的迁移脚本。新增结构或数据变更时在末尾追加一段 SQL，
// 已执行过的版本不会重复执行（版本号记录在 schema_meta 表）。
var migrations = []string{
	schemaV1,
	migrationV2,
}

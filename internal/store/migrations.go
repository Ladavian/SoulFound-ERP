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

// migrationV3 市集改为"逐笔记录"模式。
//
// 变化：
//  1. products 增加 barcode（扫瓶身条码用；空字符串表示未设置，
//     因此部分唯一索引要带 WHERE 条件，否则多条空值会互相冲突）
//  2. 新增 market_records：市集现场每卖一单/试饮一次都记一条，
//     带时间、数量与成交价，可逐笔撤销
//  3. 把已有的汇总数量回填成逐笔记录，保证历史数据不丢
const migrationV3 = `
ALTER TABLE products ADD COLUMN barcode TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_products_barcode
    ON products(barcode) WHERE barcode <> '';

CREATE TABLE IF NOT EXISTS market_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    market_id   INTEGER NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    product_id  INTEGER NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    kind        TEXT    NOT NULL,
    qty         INTEGER NOT NULL DEFAULT 1000,
    unit_price  INTEGER NOT NULL DEFAULT 0,
    discount    INTEGER NOT NULL DEFAULT 0,
    unit_cost   INTEGER,
    channel     TEXT    NOT NULL DEFAULT 'manual',
    barcode     TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT '',
    occurred_at TEXT    NOT NULL DEFAULT '',
    created_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_market_records_market  ON market_records(market_id, id DESC);
CREATE INDEX IF NOT EXISTS idx_market_records_product ON market_records(product_id);
CREATE INDEX IF NOT EXISTS idx_market_records_kind    ON market_records(market_id, kind);

INSERT INTO market_records(market_id, product_id, kind, qty, unit_price, discount,
                           channel, note, occurred_at, created_at)
SELECT mi.market_id, mi.product_id, 'sale', mi.sold_qty, mi.unit_price, mi.discount_amt,
       'migrated', '由历史汇总数量迁移', COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59',
       COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59'
  FROM market_items mi JOIN markets m ON m.id = mi.market_id
 WHERE mi.sold_qty <> 0;

INSERT INTO market_records(market_id, product_id, kind, qty, unit_price, discount,
                           channel, note, occurred_at, created_at)
SELECT mi.market_id, mi.product_id, 'tasting', mi.tasting_qty, 0, 0,
       'migrated', '由历史汇总数量迁移', COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59',
       COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59'
  FROM market_items mi JOIN markets m ON m.id = mi.market_id
 WHERE mi.tasting_qty <> 0;

INSERT INTO market_records(market_id, product_id, kind, qty, unit_price, discount,
                           channel, note, occurred_at, created_at)
SELECT mi.market_id, mi.product_id, 'gift', mi.gift_qty, 0, 0,
       'migrated', '由历史汇总数量迁移', COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59',
       COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59'
  FROM market_items mi JOIN markets m ON m.id = mi.market_id
 WHERE mi.gift_qty <> 0;

INSERT INTO market_records(market_id, product_id, kind, qty, unit_price, discount,
                           channel, note, occurred_at, created_at)
SELECT mi.market_id, mi.product_id, 'loss', mi.loss_qty, 0, 0,
       'migrated', '由历史汇总数量迁移', COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59',
       COALESCE(NULLIF(m.end_date, ''), m.start_date) || 'T23:59:59'
  FROM market_items mi JOIN markets m ON m.id = mi.market_id
 WHERE mi.loss_qty <> 0;
`

// migrationV4 权限按功能细分。
//
// 以前权限完全由角色决定，粒度太粗。现在每个用户可以单独指定权限点：
// permissions 为空表示沿用角色默认值（保持老账号行为不变），
// 有值就完全按它来，角色退化成"预设模板 + 快速勾选"。
const migrationV4 = `
ALTER TABLE users ADD COLUMN permissions TEXT NOT NULL DEFAULT '';

ALTER TABLE settings ADD COLUMN auto_backup INTEGER NOT NULL DEFAULT 1;
ALTER TABLE settings ADD COLUMN backup_keep INTEGER NOT NULL DEFAULT 30;
ALTER TABLE settings ADD COLUMN backup_active_hours INTEGER NOT NULL DEFAULT 24;
ALTER TABLE settings ADD COLUMN backup_idle_days INTEGER NOT NULL DEFAULT 7;

CREATE INDEX IF NOT EXISTS idx_market_records_occurred ON market_records(occurred_at);
`

// migrationV5 产品档案补全。
//
// 系统不只用于冰酒，因此把"酒类专有"的信息做成可选字段，
// 另外补一个通用的「规格参数」多行文本，任何品类都能填自己的规格。
const migrationV5 = `
ALTER TABLE products ADD COLUMN cost_price INTEGER NOT NULL DEFAULT 0;
ALTER TABLE products ADD COLUMN abv INTEGER NOT NULL DEFAULT 0;
ALTER TABLE products ADD COLUMN brand TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN specs TEXT NOT NULL DEFAULT '';
`

// migrations 按顺序执行的迁移脚本。新增结构或数据变更时在末尾追加一段 SQL，
// 已执行过的版本不会重复执行（版本号记录在 schema_meta 表）。
var migrations = []string{
	schemaV1,
	migrationV2,
	migrationV3,
	migrationV4,
	migrationV5,
}

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

// migrationV6 活动费用支持「按销售额比例」计算。
//
// 市集摊位的收费方式并不统一：有的收一口价，有的按销售额扣点。
// calc 记录计算方式，rate 是万分比（300 = 3%）；一口价时用 amount。
const migrationV6 = `
ALTER TABLE market_expenses ADD COLUMN calc TEXT NOT NULL DEFAULT 'fixed';
ALTER TABLE market_expenses ADD COLUMN rate INTEGER NOT NULL DEFAULT 0;
`

// migrationV7 支持"销售出库"。
//
// 市集之外的销售（直销、批发、熟人拿货）以前完全没法记：
// 出入库登记没有销售类型，市集收银台又只服务市集。
// sale_price 记成交单价，customer_id 记卖给谁，用于统计直销收入。
const migrationV7 = `
ALTER TABLE stock_movements ADD COLUMN sale_price INTEGER NOT NULL DEFAULT 0;
ALTER TABLE stock_movements ADD COLUMN customer_id INTEGER;
CREATE INDEX IF NOT EXISTS idx_mov_reason ON stock_movements(reason, occurred_on);
`

// migrationV8 线下大团单记录。
//
// 这类订单由大仓发货、不经过本系统的仓库，因此完全不碰库存：
// 只留存销售订单信息（客户、明细、金额、状态），用于对账与留档。
const migrationV8 = `
CREATE TABLE IF NOT EXISTS group_orders (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    code          TEXT    NOT NULL UNIQUE,
    customer_id   INTEGER REFERENCES customers(id) ON DELETE SET NULL,
    customer_name TEXT    NOT NULL DEFAULT '',
    contact       TEXT    NOT NULL DEFAULT '',
    phone         TEXT    NOT NULL DEFAULT '',
    order_date    TEXT    NOT NULL DEFAULT '',
    ship_date     TEXT    NOT NULL DEFAULT '',
    warehouse     TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'draft',
    discount      INTEGER NOT NULL DEFAULT 0,
    extra_fee     INTEGER NOT NULL DEFAULT 0,
    note          TEXT    NOT NULL DEFAULT '',
    created_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at    TEXT    NOT NULL DEFAULT '',
    updated_at    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_group_orders_date   ON group_orders(order_date DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_group_orders_status ON group_orders(status);

CREATE TABLE IF NOT EXISTS group_order_items (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id     INTEGER NOT NULL REFERENCES group_orders(id) ON DELETE CASCADE,
    product_id   INTEGER REFERENCES products(id) ON DELETE SET NULL,
    product_name TEXT    NOT NULL DEFAULT '',
    sku          TEXT    NOT NULL DEFAULT '',
    qty          INTEGER NOT NULL DEFAULT 0,
    unit         TEXT    NOT NULL DEFAULT '',
    unit_price   INTEGER NOT NULL DEFAULT 0,
    note         TEXT    NOT NULL DEFAULT '',
    sort_order   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_group_items_order ON group_order_items(order_id, sort_order);
`

// migrationV9 清掉与市集名重复的场地。
//
// 早期从表格导入时把渠道名同时填进了"场地"，列表里就会出现
// 「金融街 · 金融街」这种重复显示。
const migrationV9 = `
UPDATE markets SET venue = '' WHERE venue <> '' AND venue = name;
`

// migrationV10 团单明细支持成本单价，用来算毛利。
//
// 团单不动库存（货从大仓发），所以没有现成的成本来源，
// 只能按行记一个成本单价：建单时默认取产品当前平均成本，也可以手填。
const migrationV10 = `
ALTER TABLE group_order_items ADD COLUMN unit_cost INTEGER NOT NULL DEFAULT 0;
`

// migrationV11 电商订单导入。
//
// 平台订单要能对上 ERP 的商品，靠的是「电商商品ID」——
// 淘宝导出的「商家编码」经常是空的（实测 91 行全空），
// 只有商品ID 稳定，所以把它做成产品上的一个绑定字段。
//
// 电商成本单独一个字段：平台上卖的成本口径和市集、采购不一样
// （平台扣点、活动价、赠品摊薄都在里面），不能和平均成本混用。
//
// 虚拟组套：平台上会卖「2瓶装」「A+B套装」，
// 这类在 ERP 里没有实体库存，由若干产品组合而成，
// 记在 product_bundles 里，算成本时按组成累加。
const migrationV11 = `
ALTER TABLE products ADD COLUMN ec_product_id TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN ec_sku_id TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN ec_cost INTEGER NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS idx_products_ec_id
    ON products(ec_product_id) WHERE ec_product_id <> '';
CREATE INDEX IF NOT EXISTS idx_products_ec_sku
    ON products(ec_sku_id) WHERE ec_sku_id <> '';

CREATE TABLE IF NOT EXISTS product_bundles (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    product_id   INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    component_id INTEGER NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    qty          INTEGER NOT NULL DEFAULT 1000,
    sort_order   INTEGER NOT NULL DEFAULT 0,
    UNIQUE (product_id, component_id)
);
CREATE INDEX IF NOT EXISTS idx_bundles_product ON product_bundles(product_id, sort_order);

CREATE TABLE IF NOT EXISTS ec_orders (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    platform      TEXT    NOT NULL DEFAULT 'taobao',
    order_no      TEXT    NOT NULL,
    status        TEXT    NOT NULL DEFAULT '',
    refund_status TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT '',
    paid_at       TEXT    NOT NULL DEFAULT '',
    shipped_at    TEXT    NOT NULL DEFAULT '',
    buyer_note    TEXT    NOT NULL DEFAULT '',
    seller_note   TEXT    NOT NULL DEFAULT '',
    imported_at   TEXT    NOT NULL DEFAULT '',
    UNIQUE (platform, order_no)
);
CREATE INDEX IF NOT EXISTS idx_ec_orders_time ON ec_orders(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_ec_orders_status ON ec_orders(status);

CREATE TABLE IF NOT EXISTS ec_order_items (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id          INTEGER NOT NULL REFERENCES ec_orders(id) ON DELETE CASCADE,
    platform          TEXT    NOT NULL DEFAULT 'taobao',
    sub_order_no      TEXT    NOT NULL,
    title             TEXT    NOT NULL DEFAULT '',
    ec_product_id     TEXT    NOT NULL DEFAULT '',
    ec_sku            TEXT    NOT NULL DEFAULT '',
    merchant_code     TEXT    NOT NULL DEFAULT '',
    qty               INTEGER NOT NULL DEFAULT 0,
    unit_price        INTEGER NOT NULL DEFAULT 0,
    payable_amount    INTEGER NOT NULL DEFAULT 0,
    paid_amount       INTEGER NOT NULL DEFAULT 0,
    refund_status     TEXT    NOT NULL DEFAULT '',
    refund_amount     INTEGER NOT NULL DEFAULT 0,
    item_status       TEXT    NOT NULL DEFAULT '',
    logistics_no      TEXT    NOT NULL DEFAULT '',
    logistics_company TEXT    NOT NULL DEFAULT '',
    product_id        INTEGER REFERENCES products(id) ON DELETE SET NULL,
    imported_at       TEXT    NOT NULL DEFAULT '',
    UNIQUE (platform, sub_order_no)
);
CREATE INDEX IF NOT EXISTS idx_ec_items_order   ON ec_order_items(order_id);
CREATE INDEX IF NOT EXISTS idx_ec_items_product ON ec_order_items(product_id);
CREATE INDEX IF NOT EXISTS idx_ec_items_ecid    ON ec_order_items(ec_product_id);
`

// migrationV12 电商绑定改成一张关联表 + 非酒类产品。
//
// 为什么要把 products.ec_product_id 换成 product_ec_links：
//   - 同一个产品在淘宝上可能有多个链接（不同标题、不同活动），
//     一个字段装不下，必须 1:N；
//   - 后面还要接抖音、京东、小红书、微信小店，
//     每个平台一个商品ID，靠 platform 区分。
//
// 所以绑定关系独立成表，(platform, ec_product_id, ec_sku_id) 唯一，
// 同一个平台商品ID 只能属于一个 ERP 产品。
//
// is_wine 用来区分酒类与非酒类：礼盒、海马刀、手拎袋这些
// 没有年份/容量/酒精度，表单不该逼着人填酒类参数。
const migrationV12 = `
CREATE TABLE IF NOT EXISTS product_ec_links (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    product_id    INTEGER NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    platform      TEXT    NOT NULL DEFAULT 'taobao',
    ec_product_id TEXT    NOT NULL,
    ec_sku_id     TEXT    NOT NULL DEFAULT '',
    title         TEXT    NOT NULL DEFAULT '',
    note          TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT '',
    UNIQUE (platform, ec_product_id, ec_sku_id)
);
CREATE INDEX IF NOT EXISTS idx_pel_product ON product_ec_links(product_id);
CREATE INDEX IF NOT EXISTS idx_pel_lookup  ON product_ec_links(platform, ec_product_id);

-- 把已有的单个绑定搬进关联表，然后不再使用产品上的那个字段
INSERT OR IGNORE INTO product_ec_links(product_id, platform, ec_product_id, ec_sku_id, created_at)
    SELECT id, 'taobao', ec_product_id, ec_sku_id, datetime('now')
      FROM products WHERE ec_product_id <> '';
UPDATE products SET ec_product_id = '', ec_sku_id = '';

ALTER TABLE products ADD COLUMN is_wine INTEGER NOT NULL DEFAULT 1;
`

// migrationV13 单个产品的负库存开关。
//
// 原来只有系统级开关，一刀切不合适：正装酒通常不允许卖超，
// 但礼盒、赠品、配件这些经常先卖后补，或者干脆不记库存。
// 所以每个产品加一个三态开关：
//
//	-1 跟随系统设置（默认，保持原来的行为）
//	 1 这个产品允许负库存
//	 0 这个产品禁止负库存（即使系统允许）
const migrationV13 = `
ALTER TABLE products ADD COLUMN allow_negative INTEGER NOT NULL DEFAULT -1;
`

// migrationV14 电商平台账期账单（对账单的数据源）。
//
// 平台的结算账单有多种，靠「账期 + 账单类型」区分：
//
//	收入：交易货款、淘金币合作费用、淘金币合作费用-流水
//	支出：基础软件服务费、品牌新享礼金、商家寄件服务费、
//	      淘金币软件服务费、消费券代付资金扣回、消费者体验提升计划服务费
//
// 每种文件的列都不一样，所以明细表存通用字段 + 一行原始数据(JSON)，
// 既方便对账，也能追溯、以后加平台不用改表结构。
//
// 月份归属一律跟着账单的「账期」走，不按订单月份——
// 用户明确要求以平台账单为准。
const migrationV14 = `
CREATE TABLE IF NOT EXISTS ec_statements (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    platform    TEXT    NOT NULL DEFAULT 'taobao',
    period      TEXT    NOT NULL,              -- 账期 YYYYMM，如 202608
    kind        TEXT    NOT NULL,              -- 账单类型（交易货款 / 基础软件服务费 …）
    direction   TEXT    NOT NULL,              -- income / expense
    file_name   TEXT    NOT NULL DEFAULT '',
    row_count   INTEGER NOT NULL DEFAULT 0,
    amount      INTEGER NOT NULL DEFAULT 0,    -- 该文件金额合计
    imported_at TEXT    NOT NULL DEFAULT '',
    UNIQUE (platform, period, kind)
);
CREATE INDEX IF NOT EXISTS idx_ec_stmt_period ON ec_statements(period, direction);

CREATE TABLE IF NOT EXISTS ec_statement_items (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    statement_id  INTEGER NOT NULL REFERENCES ec_statements(id) ON DELETE CASCADE,
    platform      TEXT    NOT NULL DEFAULT 'taobao',
    period        TEXT    NOT NULL DEFAULT '',
    kind          TEXT    NOT NULL DEFAULT '',
    direction     TEXT    NOT NULL DEFAULT '',
    order_no      TEXT    NOT NULL DEFAULT '',   -- 交易主订单号
    sub_order_no  TEXT    NOT NULL DEFAULT '',
    ec_product_id TEXT    NOT NULL DEFAULT '',
    ec_sku        TEXT    NOT NULL DEFAULT '',
    title         TEXT    NOT NULL DEFAULT '',
    qty           INTEGER NOT NULL DEFAULT 0,
    unit_price    INTEGER NOT NULL DEFAULT 0,
    amount        INTEGER NOT NULL DEFAULT 0,    -- 该行金额（收入为正、支出为正数，方向看 direction）
    fee_base      INTEGER NOT NULL DEFAULT 0,    -- 扣费基数
    fee_rate      TEXT    NOT NULL DEFAULT '',   -- 费率原文，如 0.60%
    refund_amount INTEGER NOT NULL DEFAULT 0,
    tracking_no   TEXT    NOT NULL DEFAULT '',   -- 运单号（运费单靠它匹配订单）
    occurred_at   TEXT    NOT NULL DEFAULT '',   -- 打款 / 扣费 / 确认收货时间
    pay_time      TEXT    NOT NULL DEFAULT '',   -- 打款时间（交易货款）
    raw           TEXT    NOT NULL DEFAULT '',   -- 原始行 JSON，便于追溯
    imported_at   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_ec_stmt_item_stmt  ON ec_statement_items(statement_id);
CREATE INDEX IF NOT EXISTS idx_ec_stmt_item_order ON ec_statement_items(platform, order_no);
CREATE INDEX IF NOT EXISTS idx_ec_stmt_item_ship  ON ec_statement_items(tracking_no);
CREATE INDEX IF NOT EXISTS idx_ec_stmt_item_prod  ON ec_statement_items(ec_product_id);
`

// migrationV15 电商结算的税率与垫付金额。
//
// 增值税按"只承担成本以上的税"来算：
//
//	应交增值税 = 销项(销售额/1.13×13%) − 进项(成本/1.13×13%)
//	             − 平台费专票抵扣(平台费/1.06×6%)
//
// 税率做成配置，不同品类/平台可能不同。
//
// 品牌新享礼金的账期账单里有两个金额：
//
//	账单金额 = 平台服务费（开票、可抵扣）
//	抽佣金额 = 服务费 + 平台替商家垫付给消费者的钱
//
// 垫付那部分**已经在货款里扣掉了**，不能再当费用计一次，
// 否则既多扣钱、又会虚增可抵扣的进项（因为只对服务费开票）。
// 所以单独存 advance_amount，只做列示与核对，不进费用合计。
const migrationV15 = `
ALTER TABLE ec_statement_items ADD COLUMN advance_amount INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ec_statement_items ADD COLUMN gross_amount INTEGER NOT NULL DEFAULT 0;

ALTER TABLE settings ADD COLUMN vat_output_rate TEXT NOT NULL DEFAULT '13';
ALTER TABLE settings ADD COLUMN vat_input_rate TEXT NOT NULL DEFAULT '13';
ALTER TABLE settings ADD COLUMN vat_platform_rate TEXT NOT NULL DEFAULT '6';
`

// migrationV16 账单行拆出 sku 的平台ID 与规格标签。
//
// 两个来源的 sku 写法完全不同：
//
//	订单导出：商品规格:1瓶装礼盒        （只有标签）
//	账期账单：6177628402264|商品规格#3B1瓶装礼盒  （平台SKU ID + 标签）
//
// SKU ID 才是平台的正经标识，但订单侧拿不到，所以两边都要能对上：
//
//	ec_sku_id  存平台 SKU ID（有就存）
//	sku_label  存规格标签（两边都有，用来跨来源匹配）
const migrationV16 = `
ALTER TABLE ec_statement_items ADD COLUMN sku_id TEXT NOT NULL DEFAULT '';
ALTER TABLE ec_statement_items ADD COLUMN sku_label TEXT NOT NULL DEFAULT '';
ALTER TABLE product_ec_links ADD COLUMN sku_label TEXT NOT NULL DEFAULT '';
`

// migrationV17 账期账单记录来源，用于"月度账单为准、对账中心只补缺"。
//
// 京东一个账期有两份来源：
//
//	月度账单    平台出的月度账单，订单与数量以它为准
//	对账中心    一单一单的明细，项目更全（商品保险服务费、运费保险服务费…）
//
// 合并规则：同一账期同一费用项，如果月度账单已经有了，
// 就不要再拿对账中心的覆盖掉——否则会把月度账单的订单范围冲掉。
// 对账中心独有的费用项（月度账单没有的）照常补进来。
const migrationV17 = `
ALTER TABLE ec_statements ADD COLUMN source TEXT NOT NULL DEFAULT 'bill';
`

// migrationV18 两份账单之间的对账校验。
//
// 京东同一账期的月度账单与对账中心都含佣金、交易服务费，
// 按订单号逐笔比对，不一致就亮出来，避免账目有错却看不出来。
// 订单号以月度账单为准（用户明确）。
const migrationV18 = `
CREATE TABLE IF NOT EXISTS ec_statement_checks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    platform        TEXT    NOT NULL DEFAULT '',
    period          TEXT    NOT NULL DEFAULT '',
    kind            TEXT    NOT NULL DEFAULT '',
    order_no        TEXT    NOT NULL DEFAULT '',
    primary_source  TEXT    NOT NULL DEFAULT '',   -- 以谁为准（月度账单）
    primary_amount  INTEGER NOT NULL DEFAULT 0,
    other_source    TEXT    NOT NULL DEFAULT '',   -- 另一份来源（对账中心）
    other_amount    INTEGER NOT NULL DEFAULT 0,
    status          TEXT    NOT NULL DEFAULT '',   -- same / diff / only_primary / only_other
    checked_at      TEXT    NOT NULL DEFAULT '',
    UNIQUE (platform, period, kind, order_no, other_source)
);
CREATE INDEX IF NOT EXISTS idx_ec_checks_lookup ON ec_statement_checks(platform, period, status);
`

// migrations 按顺序执行的迁移脚本。新增结构或数据变更时在末尾追加一段 SQL，
// 已执行过的版本不会重复执行（版本号记录在 schema_meta 表）。
var migrations = []string{
	schemaV1,
	migrationV2,
	migrationV3,
	migrationV4,
	migrationV5,
	migrationV6,
	migrationV7,
	migrationV8,
	migrationV9,
	migrationV10,
	migrationV11,
	migrationV12,
	migrationV13,
	migrationV14,
	migrationV15,
	migrationV16,
	migrationV17,
	migrationV18,
}

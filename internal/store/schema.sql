-- 加拿大冰酒 ERP · 数据库结构 v1
-- 约定：
--   金额列 = INTEGER，单位 1/10000 货币单位（C$35.00 -> 350000）
--   数量列 = INTEGER，单位 1/1000 件（12 瓶 -> 12000）
--   日期列 = TEXT 'YYYY-MM-DD'，时间戳 = TEXT RFC3339

-- ---------------------------------------------------------------- 全局设置
CREATE TABLE IF NOT EXISTS settings (
    id                      INTEGER PRIMARY KEY CHECK (id = 1),
    company_name            TEXT    NOT NULL DEFAULT '加拿大冰酒',
    currency                TEXT    NOT NULL DEFAULT 'CAD',
    currency_symbol         TEXT    NOT NULL DEFAULT 'C$',
    default_low_qty         INTEGER NOT NULL DEFAULT 6000,
    default_booth_fee       INTEGER NOT NULL DEFAULT 0,
    allow_negative_stock    INTEGER NOT NULL DEFAULT 0,
    updated_at              TEXT    NOT NULL DEFAULT ''
);

-- ---------------------------------------------------------------- 用户
CREATE TABLE IF NOT EXISTS users (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    username        TEXT    NOT NULL UNIQUE,
    password_hash   TEXT    NOT NULL,
    full_name       TEXT    NOT NULL DEFAULT '',
    role            TEXT    NOT NULL DEFAULT 'staff',
    is_active       INTEGER NOT NULL DEFAULT 1,
    last_login_at   TEXT    NOT NULL DEFAULT '',
    created_at      TEXT    NOT NULL DEFAULT ''
);

-- ---------------------------------------------------------------- 供应商 / 客户
CREATE TABLE IF NOT EXISTS suppliers (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL,
    contact_name  TEXT    NOT NULL DEFAULT '',
    phone         TEXT    NOT NULL DEFAULT '',
    email         TEXT    NOT NULL DEFAULT '',
    country       TEXT    NOT NULL DEFAULT '加拿大',
    address       TEXT    NOT NULL DEFAULT '',
    notes         TEXT    NOT NULL DEFAULT '',
    is_active     INTEGER NOT NULL DEFAULT 1,
    created_at    TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS customers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    phone       TEXT    NOT NULL DEFAULT '',
    email       TEXT    NOT NULL DEFAULT '',
    address     TEXT    NOT NULL DEFAULT '',
    notes       TEXT    NOT NULL DEFAULT '',
    is_active   INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT    NOT NULL DEFAULT ''
);

-- ---------------------------------------------------------------- 产品
CREATE TABLE IF NOT EXISTS products (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    sku               TEXT    NOT NULL UNIQUE,
    name              TEXT    NOT NULL,
    name_en           TEXT    NOT NULL DEFAULT '',
    category          TEXT    NOT NULL DEFAULT '冰酒',
    vintage           INTEGER NOT NULL DEFAULT 0,
    volume_ml         INTEGER NOT NULL DEFAULT 0,
    unit              TEXT    NOT NULL DEFAULT '瓶',
    bottles_per_case  INTEGER NOT NULL DEFAULT 6,
    sale_price        INTEGER NOT NULL DEFAULT 0,
    low_stock_qty     INTEGER NOT NULL DEFAULT 6000,
    supplier_id       INTEGER REFERENCES suppliers(id) ON DELETE SET NULL,
    image_url         TEXT    NOT NULL DEFAULT '',
    notes             TEXT    NOT NULL DEFAULT '',
    is_active         INTEGER NOT NULL DEFAULT 1,
    stock_qty         INTEGER NOT NULL DEFAULT 0,
    avg_cost          INTEGER NOT NULL DEFAULT 0,
    stock_value       INTEGER NOT NULL DEFAULT 0,
    created_at        TEXT    NOT NULL DEFAULT '',
    updated_at        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_products_supplier ON products(supplier_id);
CREATE INDEX IF NOT EXISTS idx_products_active   ON products(is_active, category);

-- ---------------------------------------------------------------- 库存流水（台账）
CREATE TABLE IF NOT EXISTS stock_movements (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    product_id      INTEGER NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    occurred_on     TEXT    NOT NULL,
    qty             INTEGER NOT NULL,             -- 正数入库、负数出库
    unit_cost       INTEGER NOT NULL DEFAULT 0,
    total_cost      INTEGER NOT NULL DEFAULT 0,
    qty_after       INTEGER NOT NULL DEFAULT 0,
    avg_cost_after  INTEGER NOT NULL DEFAULT 0,
    value_after     INTEGER NOT NULL DEFAULT 0,
    reason          TEXT    NOT NULL,
    ref_type        TEXT    NOT NULL DEFAULT '',
    ref_id          INTEGER,
    ref_code        TEXT    NOT NULL DEFAULT '',
    note            TEXT    NOT NULL DEFAULT '',
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_mov_product ON stock_movements(product_id, occurred_on, id);
CREATE INDEX IF NOT EXISTS idx_mov_ref     ON stock_movements(ref_type, ref_id);
CREATE INDEX IF NOT EXISTS idx_mov_date    ON stock_movements(occurred_on, id);

-- ---------------------------------------------------------------- 采购入库
CREATE TABLE IF NOT EXISTS purchases (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    code           TEXT    NOT NULL UNIQUE,
    supplier_id    INTEGER REFERENCES suppliers(id) ON DELETE SET NULL,
    purchase_date  TEXT    NOT NULL,
    currency       TEXT    NOT NULL DEFAULT 'CAD',
    status         TEXT    NOT NULL DEFAULT 'draft',
    alloc_method   TEXT    NOT NULL DEFAULT 'amount',
    goods_amount   INTEGER NOT NULL DEFAULT 0,
    shipping_cost  INTEGER NOT NULL DEFAULT 0,
    tariff_cost    INTEGER NOT NULL DEFAULT 0,
    other_cost     INTEGER NOT NULL DEFAULT 0,
    total_cost     INTEGER NOT NULL DEFAULT 0,
    notes          TEXT    NOT NULL DEFAULT '',
    created_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    confirmed_at   TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL DEFAULT '',
    updated_at     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_purchases_date   ON purchases(purchase_date DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_purchases_status ON purchases(status);
CREATE INDEX IF NOT EXISTS idx_purchases_supp   ON purchases(supplier_id);

CREATE TABLE IF NOT EXISTS purchase_items (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    purchase_id       INTEGER NOT NULL REFERENCES purchases(id) ON DELETE CASCADE,
    product_id        INTEGER NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    qty               INTEGER NOT NULL DEFAULT 0,
    unit_price        INTEGER NOT NULL DEFAULT 0,
    amount            INTEGER NOT NULL DEFAULT 0,
    extra_alloc       INTEGER NOT NULL DEFAULT 0,
    landed_unit_cost  INTEGER NOT NULL DEFAULT 0,
    note              TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_purchase_items_purchase ON purchase_items(purchase_id);
CREATE INDEX IF NOT EXISTS idx_purchase_items_product  ON purchase_items(product_id);

-- ---------------------------------------------------------------- 市集活动
CREATE TABLE IF NOT EXISTS markets (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    code           TEXT    NOT NULL UNIQUE,
    name           TEXT    NOT NULL,
    venue          TEXT    NOT NULL DEFAULT '',
    city           TEXT    NOT NULL DEFAULT '',
    organizer      TEXT    NOT NULL DEFAULT '',
    start_date     TEXT    NOT NULL,
    end_date       TEXT    NOT NULL,
    status         TEXT    NOT NULL DEFAULT 'planned',
    notes          TEXT    NOT NULL DEFAULT '',
    revenue        INTEGER NOT NULL DEFAULT 0,
    cogs_sold      INTEGER NOT NULL DEFAULT 0,
    tasting_cost   INTEGER NOT NULL DEFAULT 0,
    loss_cost      INTEGER NOT NULL DEFAULT 0,
    expense_total  INTEGER NOT NULL DEFAULT 0,
    net_profit     INTEGER NOT NULL DEFAULT 0,
    created_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    settled_at     TEXT    NOT NULL DEFAULT '',
    settled_by     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at     TEXT    NOT NULL DEFAULT '',
    updated_at     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_markets_date   ON markets(start_date DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_markets_status ON markets(status);

CREATE TABLE IF NOT EXISTS market_items (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    market_id       INTEGER NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    product_id      INTEGER NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    carried_qty     INTEGER NOT NULL DEFAULT 0,
    tasting_qty     INTEGER NOT NULL DEFAULT 0,
    sold_qty        INTEGER NOT NULL DEFAULT 0,
    gift_qty        INTEGER NOT NULL DEFAULT 0,
    loss_qty        INTEGER NOT NULL DEFAULT 0,
    unit_price      INTEGER NOT NULL DEFAULT 0,
    discount_amt    INTEGER NOT NULL DEFAULT 0,
    unit_cost       INTEGER,                      -- 结算时的成本快照，NULL 表示未结算
    note            TEXT    NOT NULL DEFAULT '',
    sort_order      INTEGER NOT NULL DEFAULT 0,
    UNIQUE (market_id, product_id)
);
CREATE INDEX IF NOT EXISTS idx_market_items_market  ON market_items(market_id, sort_order, id);
CREATE INDEX IF NOT EXISTS idx_market_items_product ON market_items(product_id);

CREATE TABLE IF NOT EXISTS market_expenses (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    market_id   INTEGER NOT NULL REFERENCES markets(id) ON DELETE CASCADE,
    category    TEXT    NOT NULL DEFAULT 'other',
    amount      INTEGER NOT NULL DEFAULT 0,
    note        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_market_expenses_market ON market_expenses(market_id);

-- ---------------------------------------------------------------- 操作日志
CREATE TABLE IF NOT EXISTS activity_logs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER,
    username    TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    entity      TEXT    NOT NULL DEFAULT '',
    entity_id   INTEGER,
    detail      TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_logs_created ON activity_logs(created_at DESC, id DESC);

-- ---------------------------------------------------------------- 单据号序列
CREATE TABLE IF NOT EXISTS doc_seq (
    scope  TEXT    NOT NULL,
    day    TEXT    NOT NULL,
    seq    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (scope, day)
);

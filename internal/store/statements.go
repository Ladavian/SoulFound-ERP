package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"icewine-erp/internal/model"
)

// ReplaceStatement 覆盖保存一个「平台 + 账期 + 账单类型」的账单。
//
// 同一份账单重复导入时整体替换，避免重复计账。
func (s *Store) ReplaceStatement(ctx context.Context, tx DBTX, st *model.EcStatement, items []model.EcStatementItem) error {
	var existing int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM ec_statements WHERE platform = ? AND period = ? AND kind = ?`,
		st.Platform, st.Period, st.Kind).Scan(&existing)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `DELETE FROM ec_statement_items WHERE statement_id = ?`, existing); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE ec_statements SET direction = ?, file_name = ?, row_count = ?, amount = ?, imported_at = ?
			 WHERE id = ?`,
			st.Direction, st.FileName, st.RowCount, int64(st.Amount), Now(), existing); err != nil {
			return err
		}
		st.ID = existing
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx,
			`INSERT INTO ec_statements(platform, period, kind, direction, file_name, row_count, amount, imported_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			st.Platform, st.Period, st.Kind, st.Direction, st.FileName, st.RowCount, int64(st.Amount), Now())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		st.ID = id
	default:
		return err
	}

	for i := range items {
		it := &items[i]
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO ec_statement_items(statement_id, platform, period, kind, direction,
			        order_no, sub_order_no, ec_product_id, ec_sku, title, qty, unit_price,
			        amount, fee_base, fee_rate, refund_amount, tracking_no, occurred_at,
			        pay_time, raw, imported_at, advance_amount, gross_amount,
			        sku_id, sku_label)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			st.ID, st.Platform, st.Period, st.Kind, st.Direction,
			it.OrderNo, it.SubOrderNo, it.EcProductID, it.EcSKU, it.Title,
			int64(it.Qty), int64(it.UnitPrice), int64(it.Amount), int64(it.FeeBase),
			it.FeeRate, int64(it.RefundAmount), it.TrackingNo, it.OccurredAt,
			it.PayTime, it.Raw, Now(), int64(it.Advance), int64(it.GrossAmount),
			it.SKUId, it.SKULabel); err != nil {
			return err
		}
	}
	return nil
}

// ListStatements 某个账期的全部账单。
func (s *Store) ListStatements(ctx context.Context, platform, period string) ([]model.EcStatement, error) {
	query := `SELECT id, platform, period, kind, direction, file_name, row_count, amount, imported_at
	          FROM ec_statements WHERE platform = ?`
	args := []any{platform}
	if period != "" {
		query += ` AND period = ?`
		args = append(args, period)
	}
	query += ` ORDER BY period DESC, direction DESC, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EcStatement
	for rows.Next() {
		var st model.EcStatement
		if err := rows.Scan(&st.ID, &st.Platform, &st.Period, &st.Kind, &st.Direction,
			&st.FileName, &st.RowCount, &st.Amount, &st.ImportedAt); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// StatementPeriods 有哪些账期（下拉用）。
func (s *Store) StatementPeriods(ctx context.Context, platform string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT period FROM ec_statements WHERE platform = ? ORDER BY period DESC`, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// StatementItemFilter 账单明细查询条件。
type StatementItemFilter struct {
	Platform  string
	Period    string
	Kind      string
	Direction string
	OrderNo   string
	Limit     int
	Offset    int
}

// ListStatementItems 账单明细。
func (s *Store) ListStatementItems(ctx context.Context, f StatementItemFilter) ([]model.EcStatementItem, error) {
	var (
		conds []string
		args  []any
	)
	conds = append(conds, "si.platform = ?")
	args = append(args, f.Platform)
	if f.Period != "" {
		conds = append(conds, "si.period = ?")
		args = append(args, f.Period)
	}
	if f.Kind != "" {
		conds = append(conds, "si.kind = ?")
		args = append(args, f.Kind)
	}
	if f.Direction != "" {
		conds = append(conds, "si.direction = ?")
		args = append(args, f.Direction)
	}
	if f.OrderNo != "" {
		conds = append(conds, "si.order_no = ?")
		args = append(args, f.OrderNo)
	}
	query := `SELECT ` + stmtItemCols + stmtItemFrom + ` WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY si.id`
	if f.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EcStatementItem
	for rows.Next() {
		it, err := scanStmtItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

// 账单明细统一按「订单号 → 归属产品」带出产品成本。
//
// 关联链路：账单行.order_no → ec_orders.order_no → ec_order_items.product_id
// 运费单没有订单号，用 tracking_no → ec_order_items.logistics_no。
const stmtItemCols = `si.id, si.statement_id, si.platform, si.period, si.kind, si.direction,
	si.order_no, si.sub_order_no, si.ec_product_id, si.ec_sku, si.title, si.qty,
	si.unit_price, si.amount, si.fee_base, si.fee_rate, si.refund_amount,
	si.tracking_no, si.occurred_at, si.pay_time, si.raw, si.imported_at,
	si.advance_amount, si.gross_amount, si.sku_id, si.sku_label,
	oi.product_id, COALESCE(p.name, ''), COALESCE(p.sku, ''), COALESCE(p.ec_cost, 0)`

// statementOrderJoin 账单行 → 订单明细的关联。
//
// 关键：平台账单里的「订单号」是**主订单号**，不是子订单号，
// 一张主订单可能有多条子订单（多商品/多规格）。
// 所以先按主订单号找到订单，再在订单内按优先级挑一条明细：
//
//	子订单号（最精确）→ 规格标签 → 空规格。
//
// 之前直接拿主订单号去比子订单号，多商品订单永远匹配不上，
// 成本被算成 0——用户就是在 1010091951049 这个礼盒上发现的。
const statementOrderJoin = `
	LEFT JOIN ec_orders eo
	       ON eo.platform = si.platform AND eo.order_no = si.order_no
	LEFT JOIN ec_order_items oi ON oi.id = (
	    SELECT x.id FROM ec_order_items x
	     WHERE x.order_id = eo.id
	       AND ( (si.sub_order_no <> '' AND x.sub_order_no = si.sub_order_no)
	          -- 用绑定的 SKU ID / 规格标签反查产品，再挑出属于该产品的明细行。
	          -- 比对比规格文本可靠：账单写「…|商品规格#3B1瓶装礼盒」，
	          -- 订单导出写「商品规格:1瓶装礼盒」，文本形式并不一致。
	          OR (x.product_id IS NOT NULL AND x.product_id = (
	                SELECT l.product_id FROM product_ec_links l
	                 WHERE l.platform = si.platform
	                   AND l.ec_product_id = si.ec_product_id
	                   AND ( (si.sku_id <> '' AND l.ec_sku_id = si.sku_id)
	                      OR (si.sku_label <> '' AND (l.ec_sku_id = si.sku_label OR l.sku_label = si.sku_label))
	                      OR (si.sku_id = '' AND si.sku_label = '' AND l.ec_sku_id = '' AND l.sku_label = '') )
	                 LIMIT 1 ) )
	          OR (si.sku_label = '' AND x.ec_sku = '') )
	     ORDER BY CASE
	         WHEN si.sub_order_no <> '' AND x.sub_order_no = si.sub_order_no THEN 0
	         WHEN si.ec_product_id <> '' AND x.product_id IS NOT NULL THEN 1
	         ELSE 2 END
	     LIMIT 1 )
	LEFT JOIN products p ON p.id = oi.product_id`

const stmtItemFrom = ` FROM ec_statement_items si
	` + statementOrderJoin

func scanStmtItem(row interface{ Scan(...any) error }) (*model.EcStatementItem, error) {
	var it model.EcStatementItem
	if err := row.Scan(&it.ID, &it.StatementID, &it.Platform, &it.Period, &it.Kind, &it.Direction,
		&it.OrderNo, &it.SubOrderNo, &it.EcProductID, &it.EcSKU, &it.Title, &it.Qty,
		&it.UnitPrice, &it.Amount, &it.FeeBase, &it.FeeRate, &it.RefundAmount,
		&it.TrackingNo, &it.OccurredAt, &it.PayTime, &it.Raw, &it.ImportedAt,
		&it.Advance, &it.GrossAmount, &it.SKUId, &it.SKULabel,
		&it.ProductID, &it.ProductName, &it.ProductSKU, &it.EcCost); err != nil {
		return nil, err
	}
	return &it, nil
}

// Reconcile 汇总某个账期。
func (s *Store) Reconcile(ctx context.Context, platform, period string) (model.EcReconcile, error) {
	r := model.EcReconcile{
		Platform:        platform,
		Period:          period,
		IncomeByKind:    map[string]model.Money{},
		ExpenseByKind:   map[string]model.Money{},
		IncomeKindRows:  map[string]int{},
		ExpenseKindRows: map[string]int{},
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, direction, COUNT(*), COALESCE(SUM(amount), 0)
		   FROM ec_statement_items WHERE platform = ? AND period = ?
		  GROUP BY kind, direction`, platform, period)
	if err != nil {
		return r, err
	}
	for rows.Next() {
		var (
			kind, dir string
			n         int
			sum       model.Money
		)
		if err := rows.Scan(&kind, &dir, &n, &sum); err != nil {
			rows.Close()
			return r, err
		}
		if dir == "income" {
			r.IncomeTotal += sum
			r.IncomeItems += n
			r.IncomeByKind[kind] += sum
			r.IncomeKindRows[kind] += n
		} else {
			r.ExpenseTotal += sum
			r.ExpenseItems += n
			r.ExpenseByKind[kind] += sum
			r.ExpenseKindRows[kind] += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return r, err
	}

	// 产品成本：只按「交易货款」行算一次。
	// 账单里一张订单有多行（货款/服务费/礼金/淘金币），
	// 如果每行都乘一次数量，成本会被重复累加好几倍——
	// 之前对账单页显示 -9685 就是这个原因。
	// 另外「缺成本」只在货款行上统计，否则每张订单会被报好几次。
	costRows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(SUM(si.qty * COALESCE(p.ec_cost, 0) / 1000), 0),
		        COALESCE(SUM(CASE WHEN oi.product_id IS NOT NULL AND p.ec_cost > 0 THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN oi.product_id IS NULL OR p.ec_cost <= 0 THEN 1 ELSE 0 END), 0)
		   FROM ec_statement_items si
		   `+statementOrderJoin+`
		  WHERE si.platform = ? AND si.period = ? AND si.kind = ?`,
		platform, period, model.StmtGoodsPayment)
	if err != nil {
		return r, err
	}
	if costRows.Next() {
		if err := costRows.Scan(&r.CostTotal, &r.CostKnownRows, &r.CostMissing); err != nil {
			costRows.Close()
			return r, err
		}
	}
	costRows.Close()

	// 运费单里没有对应订单的运单数
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(CASE WHEN oi.id IS NULL THEN 1 ELSE 0 END), 0), COUNT(*)
		   FROM ec_statement_items si
		   LEFT JOIN ec_order_items oi ON oi.logistics_no = si.tracking_no AND si.tracking_no <> ''
		  WHERE si.platform = ? AND si.period = ? AND si.kind = ?`,
		platform, period, model.StmtShipping).Scan(&r.UnmatchedShip, &r.TotalOrders); err != nil {
		return r, err
	}
	return r, nil
}

// StatementByKindRows 某个账单类型一个账期的明细（对账页展开用）。
func (s *Store) StatementByKindRows(ctx context.Context, platform, period, kind string) ([]model.EcStatementItem, error) {
	return s.ListStatementItems(ctx, StatementItemFilter{
		Platform: platform, Period: period, Kind: kind,
	})
}

// SettlementInput 生成结算表所需的数据（一个账期）。
type SettlementOrder struct {
	OrderNo     string
	SubOrderNo  string
	Title       string
	EcProductID string
	ProductID   *int64
	ProductName string
	Qty         model.Qty
	Goods       model.Money // 货款（平台打款的商品款）
	Subsidy     model.Money // 平台补贴（淘金币等，算收入）
	Advance     model.Money // 平台代付垫支（要从货款扣回）
	UnitCost    model.Money // 上游供货价（产品成本）
	FeePaid     model.Money // 平台费用（开票、可抵扣那部分）
}

// SettlementCost 按商品的成本与收入汇总行。
type SettlementProduct struct {
	ProductID   *int64
	Name        string
	EcProductID string
	Qty         model.Qty
	Revenue     model.Money
	Cost        model.Money
}

// StatementSettlement 取一个账期的结算数据源。
//
// 收入与成本按「账单行 → 订单明细 → 产品」关联；
// 账单里没有订单号的行（如整期一笔的体验服务费）单独作为期间费用返回。
func (s *Store) StatementSettlement(ctx context.Context, platform, period string) (
	orders []SettlementOrder, periodFees map[string]model.Money, err error) {

	periodFees = map[string]model.Money{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT si.kind, si.direction, si.order_no, si.sub_order_no, si.title, si.ec_product_id,
		       COALESCE(si.qty, 0), si.amount, si.advance_amount,
		       oi.product_id, COALESCE(p.name, ''), COALESCE(p.ec_cost, 0)
		  FROM ec_statement_items si
		  `+statementOrderJoin+`
		 WHERE si.platform = ? AND si.period = ?
		 ORDER BY si.id`, platform, period)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	index := map[string]*SettlementOrder{}
	var seq []string
	for rows.Next() {
		var (
			kind, dir, orderNo, subNo, title, ecID string
			qty                                    model.Qty
			amount                                 model.Money
			advance                                model.Money
			productID                              *int64
			productName                            string
			unitCost                               model.Money
		)
		if err := rows.Scan(&kind, &dir, &orderNo, &subNo, &title, &ecID,
			&qty, &amount, &advance, &productID, &productName, &unitCost); err != nil {
			return nil, nil, err
		}
		// 没有订单号的：整期一笔的费用或收入，单独归集
		if orderNo == "" {
			periodFees[kind] += amount
			continue
		}
		row, ok := index[orderNo]
		if !ok {
			row = &SettlementOrder{OrderNo: orderNo, SubOrderNo: subNo}
			index[orderNo] = row
			seq = append(seq, orderNo)
		}
		if title != "" {
			row.Title = title
		}
		if ecID != "" {
			row.EcProductID = ecID
		}
		if productID != nil {
			row.ProductID = productID
			row.ProductName = productName
			row.UnitCost = unitCost
		}
		// 数量只认「交易货款」行：
		// 费用行也有数量（基础软件服务费、消费券等各是 1），
		// 如果让它们覆盖，2 瓶的订单会被改成 1 瓶，成本就少算一半。
		if qty > 0 && kind == model.StmtGoodsPayment {
			row.Qty = qty
		}
		switch {
		case kind == model.StmtGoodsPayment:
			row.Goods += amount
		case dir == "income":
			row.Subsidy += amount
		default:
			row.FeePaid += amount
		}
		// 平台代付垫支：从收入里扣回（不是费用、不开票）
		row.Advance += advance
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for _, k := range seq {
		orders = append(orders, *index[k])
	}
	return orders, periodFees, nil
}

// StatementSettlementProducts 按商品汇总（销量/销售额/成本）。
func (s *Store) StatementSettlementProducts(ctx context.Context, platform, period string) ([]SettlementProduct, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT oi.product_id, COALESCE(p.name, ''), si.ec_product_id,
		       COALESCE(SUM(si.qty), 0), COALESCE(SUM(si.amount), 0),
		       COALESCE(SUM(si.qty * COALESCE(p.ec_cost, 0) / 1000), 0)
		  FROM ec_statement_items si
		  `+statementOrderJoin+`
		 WHERE si.platform = ? AND si.period = ? AND si.kind = ?
		 GROUP BY COALESCE(CAST(oi.product_id AS TEXT), ''), si.ec_product_id, COALESCE(p.name, '')
		 ORDER BY SUM(si.amount) DESC`, platform, period, model.StmtGoodsPayment)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SettlementProduct
	for rows.Next() {
		var r SettlementProduct
		if err := rows.Scan(&r.ProductID, &r.Name, &r.EcProductID, &r.Qty, &r.Revenue, &r.Cost); err != nil {
			return nil, err
		}
		if r.Name == "" && r.EcProductID != "" {
			r.Name = "商品ID " + r.EcProductID + "（未绑定产品）"
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StatementKey 已导入的账单标识。
type StatementKey struct {
	Platform string
	Period   string
}

// StatementKeys 所有已导入的账单（按平台 + 账期）。
//
// 不同平台的结算节奏不一样：淘宝按月，别的平台可能两三个月结一次，
// 所以合并对账单要能一次把「各平台各自的账期」都装进来。
func (s *Store) StatementKeys(ctx context.Context) ([]StatementKey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT platform, period FROM ec_statements ORDER BY period DESC, platform`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatementKey
	for rows.Next() {
		var k StatementKey
		if err := rows.Scan(&k.Platform, &k.Period); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

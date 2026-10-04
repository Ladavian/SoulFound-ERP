package store

import (
	"context"
	"strings"

	"icewine-erp/internal/model"
)

const movementCols = `m.id, m.product_id, p.name, p.sku, m.occurred_on, m.qty,
	m.unit_cost, m.total_cost, m.qty_after, m.avg_cost_after, m.value_after,
	m.reason, m.ref_type, m.ref_id, m.ref_code, m.note, m.created_by,
	COALESCE(NULLIF(u.full_name, ''), u.username, ''), m.created_at,
	m.sale_price, m.customer_id, COALESCE(NULLIF(c.name, ''), '')`

const movementFrom = ` FROM stock_movements m
	JOIN products p ON p.id = m.product_id
	LEFT JOIN users u ON u.id = m.created_by
	LEFT JOIN customers c ON c.id = m.customer_id`

func scanMovement(row interface{ Scan(...any) error }) (*model.StockMovement, error) {
	var mv model.StockMovement
	if err := row.Scan(&mv.ID, &mv.ProductID, &mv.ProductName, &mv.ProductSKU, &mv.OccurredOn,
		&mv.Qty, &mv.UnitCost, &mv.TotalCost, &mv.QtyAfter, &mv.AvgCostAfter, &mv.ValueAfter,
		&mv.Reason, &mv.RefType, &mv.RefID, &mv.RefCode, &mv.Note, &mv.CreatedBy,
		&mv.CreatedByName, &mv.CreatedAt,
		&mv.SalePrice, &mv.CustomerID, &mv.CustomerName); err != nil {
		return nil, err
	}
	return &mv, nil
}

// MovementFilter 库存流水筛选条件。
type MovementFilter struct {
	ProductID int64
	Reason    string
	RefType   string
	RefID     int64
	From      string
	To        string
	Keyword   string
	Direction string // in / out
	Limit     int
	Offset    int
}

func (f MovementFilter) build() (string, []any) {
	var (
		where []string
		args  []any
	)
	if f.ProductID > 0 {
		where = append(where, "m.product_id = ?")
		args = append(args, f.ProductID)
	}
	if f.Reason != "" {
		where = append(where, "m.reason = ?")
		args = append(args, f.Reason)
	}
	if f.RefType != "" {
		where = append(where, "m.ref_type = ?")
		args = append(args, f.RefType)
	}
	if f.RefID > 0 {
		where = append(where, "m.ref_id = ?")
		args = append(args, f.RefID)
	}
	if f.From != "" {
		where = append(where, "m.occurred_on >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "m.occurred_on <= ?")
		args = append(args, f.To)
	}
	switch f.Direction {
	case "in":
		where = append(where, "m.qty > 0")
	case "out":
		where = append(where, "m.qty < 0")
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		where = append(where, "(p.name LIKE ? OR p.sku LIKE ? OR m.ref_code LIKE ? OR m.note LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like, like)
	}
	return whereClause(where), args
}

// ListMovements 查询库存流水。
func (s *Store) ListMovements(ctx context.Context, f MovementFilter) ([]model.StockMovement, error) {
	where, args := f.build()
	query := `SELECT ` + movementCols + movementFrom + where + ` ORDER BY m.occurred_on DESC, m.id DESC`
	if f.Limit <= 0 {
		f.Limit = 100
	}
	query += " LIMIT ? OFFSET ?"
	args = append(args, f.Limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.StockMovement
	for rows.Next() {
		mv, err := scanMovement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *mv)
	}
	return out, rows.Err()
}

// CountMovements 统计流水条数（分页用）。
func (s *Store) CountMovements(ctx context.Context, f MovementFilter) (int, error) {
	where, args := f.build()
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stock_movements m JOIN products p ON p.id = m.product_id`+where, args...).Scan(&n)
	return n, err
}

// InsertMovement 写入一条库存流水。
func (s *Store) InsertMovement(ctx context.Context, tx DBTX, m *model.StockMovement) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO stock_movements(product_id, occurred_on, qty, unit_cost, total_cost,
		        qty_after, avg_cost_after, value_after, reason, ref_type, ref_id, ref_code,
		        note, created_by, created_at, sale_price, customer_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ProductID, m.OccurredOn, int64(m.Qty), int64(m.UnitCost), int64(m.TotalCost),
		int64(m.QtyAfter), int64(m.AvgCostAfter), int64(m.ValueAfter), m.Reason, m.RefType,
		m.RefID, m.RefCode, m.Note, m.CreatedBy, Now(),
		int64(m.SalePrice), m.CustomerID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// movementRawCols 内部查询用的列（不关联产品/用户表）。
const movementRawCols = `id, product_id, occurred_on, qty, unit_cost, total_cost, qty_after,
	avg_cost_after, value_after, reason, ref_type, ref_id, ref_code, note, created_by, created_at`

func scanMovementRaw(row interface{ Scan(...any) error }) (*model.StockMovement, error) {
	var mv model.StockMovement
	err := row.Scan(&mv.ID, &mv.ProductID, &mv.OccurredOn, &mv.Qty, &mv.UnitCost, &mv.TotalCost,
		&mv.QtyAfter, &mv.AvgCostAfter, &mv.ValueAfter, &mv.Reason, &mv.RefType, &mv.RefID,
		&mv.RefCode, &mv.Note, &mv.CreatedBy, &mv.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &mv, nil
}

// MovementsByRef 查询某单据产生的全部流水（用于撤销）。
func (s *Store) MovementsByRef(ctx context.Context, tx DBTX, refType string, refID int64) ([]model.StockMovement, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+movementRawCols+` FROM stock_movements
		 WHERE ref_type = ? AND ref_id = ? ORDER BY id`, refType, refID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.StockMovement
	for rows.Next() {
		mv, err := scanMovementRaw(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *mv)
	}
	return out, rows.Err()
}

// DeleteMovementsByRef 删除某单据的流水（仅在撤销时使用）。
func (s *Store) DeleteMovementsByRef(ctx context.Context, tx DBTX, refType string, refID int64) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM stock_movements WHERE ref_type = ? AND ref_id = ?`, refType, refID)
	return err
}

// ProductMovementsAsc 某产品的流水，按「过账顺序」（id 升序）返回，用于重算库存。
//
// 这里刻意不按 occurred_on 排序：移动加权平均是在每次过账那一刻增量计算的，
// 只按 id 重放才能精确复现当时的成本。单据的业务日期（occurred_on）可能因为
// 补录而被倒填，如果按业务日期重放，历史成本会被改写，重算结果也会与日常
// 记账结果不一致。
func (s *Store) ProductMovementsAsc(ctx context.Context, tx DBTX, productID int64) ([]model.StockMovement, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+movementRawCols+` FROM stock_movements
		 WHERE product_id = ? ORDER BY id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.StockMovement
	for rows.Next() {
		mv, err := scanMovementRaw(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *mv)
	}
	return out, rows.Err()
}

// UpdateMovementSnapshot 重算库存时回写流水上的结存快照。
func (s *Store) UpdateMovementSnapshot(ctx context.Context, tx DBTX, id int64, qtyAfter model.Qty, avgAfter, valueAfter model.Money) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE stock_movements SET qty_after = ?, avg_cost_after = ?, value_after = ? WHERE id = ?`,
		int64(qtyAfter), int64(avgAfter), int64(valueAfter), id)
	return err
}

// ResetProductStock 归零某产品的库存缓存（重算前调用）。
func (s *Store) ResetProductStock(ctx context.Context, tx DBTX, productID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE products SET stock_qty = 0, avg_cost = 0, stock_value = 0 WHERE id = ?`, productID)
	return err
}

// SumStock 全部库存合计。
func (s *Store) SumStock(ctx context.Context) (qty model.Qty, value model.Money, err error) {
	var (
		q int64
		v int64
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(stock_qty), 0), COALESCE(SUM(stock_value), 0)
		 FROM products WHERE is_active = 1`).Scan(&q, &v)
	return model.Qty(q), model.Money(v), err
}

// CategoryStock 品类库存汇总。
type CategoryStock struct {
	Category string
	SKUCount int
	Qty      model.Qty
	Value    model.Money
}

// StockByCategory 按品类汇总库存。
func (s *Store) StockByCategory(ctx context.Context) ([]CategoryStock, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT category, COUNT(*), COALESCE(SUM(stock_qty), 0), COALESCE(SUM(stock_value), 0)
		 FROM products WHERE is_active = 1 GROUP BY category ORDER BY 4 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CategoryStock
	for rows.Next() {
		var (
			c CategoryStock
			q int64
			v int64
		)
		if err := rows.Scan(&c.Category, &c.SKUCount, &q, &v); err != nil {
			return nil, err
		}
		c.Qty = model.Qty(q)
		c.Value = model.Money(v)
		out = append(out, c)
	}
	return out, rows.Err()
}

// DirectSaleSummary 一段时间内"销售出库"（市集之外的直销）的汇总。
type DirectSaleSummary struct {
	Count  int
	Qty    model.Qty
	Amount model.Money
	Cost   model.Money
}

// Profit 直销毛利 = 销售额 − 售出成本。
func (d DirectSaleSummary) Profit() model.Money { return d.Amount - d.Cost }

// DirectSaleSummaryBetween 按发生日期统计直销出库。
//
// 数量存的是 1/1000、单价存的是 1/10000，所以金额要整体除以 1000
// 才是 1/10000 为单位的 Money，避免逐行取整丢精度。
func (s *Store) DirectSaleSummaryBetween(ctx context.Context, from, to string) (DirectSaleSummary, error) {
	var out DirectSaleSummary
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(-qty), 0),
		       COALESCE(SUM(-qty * sale_price) / 1000, 0),
		       COALESCE(SUM(-qty * unit_cost) / 1000, 0)
		  FROM stock_movements
		 WHERE reason = ? AND occurred_on >= ? AND occurred_on <= ?`,
		model.ReasonDirectSale, from, to).
		Scan(&out.Count, &out.Qty, &out.Amount, &out.Cost)
	return out, err
}

// OutboundRow 某一种出库原因的汇总。
type OutboundRow struct {
	Reason  string
	Count   int
	Qty     model.Qty
	Amount  model.Money // 只有销售出库有金额
	Percent int         // 占全部出库的比例（数量口径）
}

// OutboundByReasonBetween 统计一段时间内各原因的出库构成。
//
// 出库不止市集：还有直销、赠送、损耗、盘点调减等，
// 这里按原因汇总，页面据此展示"货都从哪些渠道出去"。
func (s *Store) OutboundByReasonBetween(ctx context.Context, from, to string) ([]OutboundRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT reason,
		       COUNT(*),
		       COALESCE(SUM(-qty), 0),
		       COALESCE(SUM(CASE WHEN reason = ? THEN -qty * sale_price / 1000 ELSE 0 END), 0)
		  FROM stock_movements
		 WHERE qty < 0 AND occurred_on >= ? AND occurred_on <= ?
		 GROUP BY reason
		 ORDER BY 3 DESC`, model.ReasonDirectSale, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OutboundRow
	var total model.Qty
	for rows.Next() {
		var r OutboundRow
		if err := rows.Scan(&r.Reason, &r.Count, &r.Qty, &r.Amount); err != nil {
			return nil, err
		}
		total += r.Qty
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if total > 0 {
			out[i].Percent = int(float64(out[i].Qty)/float64(total)*100 + 0.5)
		}
	}
	return out, nil
}

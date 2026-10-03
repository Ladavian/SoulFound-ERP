package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"icewine-erp/internal/model"
)

const marketCols = `m.id, m.code, m.name, m.venue, m.city, m.organizer, m.start_date, m.end_date,
	m.status, m.notes, m.revenue, m.cogs_sold, m.tasting_cost, m.loss_cost, m.expense_total,
	m.net_profit, m.created_by, COALESCE(NULLIF(u.full_name, ''), u.username, ''), m.settled_at,
	COALESCE(NULLIF(su.full_name, ''), su.username, ''), m.created_at, m.updated_at`

const marketFrom = ` FROM markets m
	LEFT JOIN users u  ON u.id  = m.created_by
	LEFT JOIN users su ON su.id = m.settled_by`

func scanMarket(row interface{ Scan(...any) error }) (*model.Market, error) {
	var (
		m         model.Market
		createdBy sql.NullInt64
	)
	err := row.Scan(&m.ID, &m.Code, &m.Name, &m.Venue, &m.City, &m.Organizer, &m.StartDate,
		&m.EndDate, &m.Status, &m.Notes, &m.Revenue, &m.CogsSold, &m.TastingCost, &m.LossCost,
		&m.ExpenseTotal, &m.NetProfit, &createdBy, &m.CreatedByName, &m.SettledAt,
		&m.SettledByName, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	m.CreatedBy = ptrInt(createdBy)
	return &m, nil
}

// MarketFilter 市集筛选条件。
type MarketFilter struct {
	Keyword string
	Status  string
	From    string
	To      string
	Limit   int
	Sort    string
}

// ListMarkets 查询市集列表（含明细与费用）。
func (s *Store) ListMarkets(ctx context.Context, f MarketFilter) ([]model.Market, error) {
	var (
		where []string
		args  []any
	)
	if st := strings.TrimSpace(f.Status); st != "" {
		where = append(where, "m.status = ?")
		args = append(args, st)
	}
	if f.From != "" {
		where = append(where, "m.end_date >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "m.start_date <= ?")
		args = append(args, f.To)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		where = append(where, "(m.name LIKE ? OR m.code LIKE ? OR m.venue LIKE ? OR m.city LIKE ? OR m.organizer LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like, like, like)
	}

	order := "m.start_date DESC, m.id DESC"
	switch f.Sort {
	case "profit":
		order = "(m.revenue - m.cogs_sold - m.tasting_cost - m.loss_cost - m.expense_total) DESC, m.start_date DESC"
	case "revenue":
		order = "m.revenue DESC, m.start_date DESC"
	case "oldest":
		order = "m.start_date ASC, m.id ASC"
	}

	query := `SELECT ` + marketCols + marketFrom + whereClause(where) + " ORDER BY " + order
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Market
	for rows.Next() {
		m, err := scanMarket(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachMarketItems(ctx, out); err != nil {
		return nil, err
	}
	if err := s.attachMarketExpenses(ctx, out); err != nil {
		return out, err
	}
	return out, s.attachMarketRecords(ctx, out)
}

// MarketByID 查询单场市集。
func (s *Store) MarketByID(ctx context.Context, id int64) (*model.Market, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+marketCols+marketFrom+` WHERE m.id = ?`, id)
	m, err := scanMarket(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items, err := s.MarketItems(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	expenses, err := s.MarketExpenses(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	records, err := s.MarketRecords(ctx, m.ID, 0)
	if err != nil {
		return nil, err
	}
	m.Items = items
	m.Expenses = expenses
	m.Records = records
	return m, nil
}

// MarketItems 查询市集明细行（关联产品，带当前售价/成本/库存）。
func (s *Store) MarketItems(ctx context.Context, marketID int64) ([]model.MarketItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mi.id, mi.market_id, mi.product_id, p.name, p.sku, p.category,
		        mi.carried_qty, mi.tasting_qty, mi.sold_qty, mi.gift_qty, mi.loss_qty,
		        mi.unit_price, mi.discount_amt, mi.unit_cost, mi.note, mi.sort_order,
		        p.sale_price, p.avg_cost, p.stock_qty, p.unit, p.image_url
		 FROM market_items mi JOIN products p ON p.id = mi.product_id
		 WHERE mi.market_id = ? ORDER BY mi.sort_order, mi.id`, marketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMarketItems(rows)
}

func scanMarketItems(rows *sql.Rows) ([]model.MarketItem, error) {
	var out []model.MarketItem
	for rows.Next() {
		var it model.MarketItem
		if err := rows.Scan(&it.ID, &it.MarketID, &it.ProductID, &it.ProductName, &it.ProductSKU,
			&it.Category, &it.CarriedQty, &it.TastingQty, &it.SoldQty, &it.GiftQty, &it.LossQty,
			&it.UnitPrice, &it.DiscountAmt, &it.UnitCost, &it.Note, &it.SortOrder,
			&it.SalePrice, &it.AvgCost, &it.StockQty, &it.Unit, &it.ImageURL); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// attachMarketItems 批量补齐市集明细。
func (s *Store) attachMarketItems(ctx context.Context, list []model.Market) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(list))
	index := make(map[int64]int, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
		index[list[i].ID] = i
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT mi.id, mi.market_id, mi.product_id, p.name, p.sku, p.category,
		        mi.carried_qty, mi.tasting_qty, mi.sold_qty, mi.gift_qty, mi.loss_qty,
		        mi.unit_price, mi.discount_amt, mi.unit_cost, mi.note, mi.sort_order,
		        p.sale_price, p.avg_cost, p.stock_qty, p.unit, p.image_url
		 FROM market_items mi JOIN products p ON p.id = mi.product_id
		 WHERE mi.market_id IN (`+placeholders(len(ids))+`) ORDER BY mi.sort_order, mi.id`,
		idArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	items, err := scanMarketItems(rows)
	if err != nil {
		return err
	}
	for _, it := range items {
		if i, ok := index[it.MarketID]; ok {
			list[i].Items = append(list[i].Items, it)
		}
	}
	return nil
}

// MarketExpenses 查询市集费用。
func (s *Store) MarketExpenses(ctx context.Context, marketID int64) ([]model.MarketExpense, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, market_id, category, amount, calc, rate, note FROM market_expenses
		 WHERE market_id = ? ORDER BY id`, marketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.MarketExpense
	for rows.Next() {
		var e model.MarketExpense
		if err := rows.Scan(&e.ID, &e.MarketID, &e.Category, &e.Amount, &e.Calc, &e.Rate, &e.Note); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) attachMarketExpenses(ctx context.Context, list []model.Market) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(list))
	index := make(map[int64]int, len(list))
	for i := range list {
		ids = append(ids, list[i].ID)
		index[list[i].ID] = i
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, market_id, category, amount, calc, rate, note FROM market_expenses
		 WHERE market_id IN (`+placeholders(len(ids))+`) ORDER BY id`, idArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var e model.MarketExpense
		if err := rows.Scan(&e.ID, &e.MarketID, &e.Category, &e.Amount, &e.Calc, &e.Rate, &e.Note); err != nil {
			return err
		}
		if i, ok := index[e.MarketID]; ok {
			list[i].Expenses = append(list[i].Expenses, e)
		}
	}
	return rows.Err()
}

// CreateMarket 新建市集。
func (s *Store) CreateMarket(ctx context.Context, tx DBTX, m *model.Market) (int64, error) {
	now := Now()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO markets(code, name, venue, city, organizer, start_date, end_date,
		        status, notes, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Code, m.Name, m.Venue, m.City, m.Organizer, m.StartDate, m.EndDate,
		m.Status, m.Notes, m.CreatedBy, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMarket 更新市集基本信息。
func (s *Store) UpdateMarket(ctx context.Context, tx DBTX, m *model.Market) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE markets SET name = ?, venue = ?, city = ?, organizer = ?, start_date = ?,
		        end_date = ?, status = ?, notes = ?, updated_at = ?
		 WHERE id = ?`,
		m.Name, m.Venue, m.City, m.Organizer, m.StartDate, m.EndDate, m.Status, m.Notes,
		Now(), m.ID)
	return err
}

// SaveMarketTotals 写入损益统计快照（不改状态）。
func (s *Store) SaveMarketTotals(ctx context.Context, tx DBTX, id int64, t model.MarketTotals) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE markets SET revenue = ?, cogs_sold = ?, tasting_cost = ?, loss_cost = ?,
		        expense_total = ?, net_profit = ?, updated_at = ?
		 WHERE id = ?`,
		int64(t.Revenue), int64(t.CogsSold), int64(t.TastingCost), int64(t.LossCost),
		int64(t.ExpenseCost), int64(t.NetProfit), Now(), id)
	return err
}

// MarkMarketSettled 标记市集为已结算并写入快照。
func (s *Store) MarkMarketSettled(ctx context.Context, tx DBTX, id int64, settledAt string, settledBy *int64, t model.MarketTotals) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE markets SET revenue = ?, cogs_sold = ?, tasting_cost = ?, loss_cost = ?,
		        expense_total = ?, net_profit = ?, status = ?, settled_at = ?, settled_by = ?,
		        updated_at = ?
		 WHERE id = ?`,
		int64(t.Revenue), int64(t.CogsSold), int64(t.TastingCost), int64(t.LossCost),
		int64(t.ExpenseCost), int64(t.NetProfit), model.MarketSettled, settledAt, settledBy,
		Now(), id)
	return err
}

// MarkMarketUnsettled 撤销结算：回到进行中并清空结算信息。
func (s *Store) MarkMarketUnsettled(ctx context.Context, tx DBTX, id int64, t model.MarketTotals) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE markets SET revenue = ?, cogs_sold = ?, tasting_cost = ?, loss_cost = ?,
		        expense_total = ?, net_profit = ?, status = ?, settled_at = '', settled_by = NULL,
		        updated_at = ?
		 WHERE id = ?`,
		int64(t.Revenue), int64(t.CogsSold), int64(t.TastingCost), int64(t.LossCost),
		int64(t.ExpenseCost), int64(t.NetProfit), model.MarketOngoing, Now(), id)
	return err
}

// MarketStatusTx 事务内读取市集状态（用于防止重复结算）。
func (s *Store) MarketStatusTx(ctx context.Context, tx DBTX, id int64) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM markets WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

// PurchaseStatusTx 事务内读取采购单状态。
func (s *Store) PurchaseStatusTx(ctx context.Context, tx DBTX, id int64) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM purchases WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

// SetMarketStatus 只更新状态。
func (s *Store) SetMarketStatus(ctx context.Context, tx DBTX, id int64, status string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE markets SET status = ?, updated_at = ? WHERE id = ?`, status, Now(), id)
	return err
}

// DeleteMarket 删除市集（明细与费用级联删除）。
func (s *Store) DeleteMarket(ctx context.Context, tx DBTX, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM markets WHERE id = ?`, id)
	return err
}

// MarketItemByID 按 ID 查询市集明细行。
func (s *Store) MarketItemByID(ctx context.Context, id int64) (*model.MarketItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT mi.id, mi.market_id, mi.product_id, p.name, p.sku, p.category,
		        mi.carried_qty, mi.tasting_qty, mi.sold_qty, mi.gift_qty, mi.loss_qty,
		        mi.unit_price, mi.discount_amt, mi.unit_cost, mi.note, mi.sort_order,
		        p.sale_price, p.avg_cost, p.stock_qty, p.unit, p.image_url
		 FROM market_items mi JOIN products p ON p.id = mi.product_id
		 WHERE mi.id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanMarketItems(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

// AddMarketItem 添加明细行（已存在则忽略）。
func (s *Store) AddMarketItem(ctx context.Context, tx DBTX, it *model.MarketItem) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO market_items(market_id, product_id, carried_qty, tasting_qty, sold_qty,
		        gift_qty, loss_qty, unit_price, discount_amt, note, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(market_id, product_id) DO NOTHING`,
		it.MarketID, it.ProductID, int64(it.CarriedQty), int64(it.TastingQty), int64(it.SoldQty),
		int64(it.GiftQty), int64(it.LossQty), int64(it.UnitPrice), int64(it.DiscountAmt),
		it.Note, it.SortOrder)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if id == 0 {
		// 冲突未插入，取回已有行 ID
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM market_items WHERE market_id = ? AND product_id = ?`,
			it.MarketID, it.ProductID).Scan(&id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// UpdateMarketItem 更新明细行数量与单价。
func (s *Store) UpdateMarketItem(ctx context.Context, tx DBTX, it *model.MarketItem) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE market_items SET carried_qty = ?, tasting_qty = ?, sold_qty = ?, gift_qty = ?,
		        loss_qty = ?, unit_price = ?, discount_amt = ?, note = ?
		 WHERE id = ?`,
		int64(it.CarriedQty), int64(it.TastingQty), int64(it.SoldQty), int64(it.GiftQty),
		int64(it.LossQty), int64(it.UnitPrice), int64(it.DiscountAmt), it.Note, it.ID)
	return err
}

// SetMarketItemCost 写入结算成本快照。
func (s *Store) SetMarketItemCost(ctx context.Context, tx DBTX, itemID int64, unitCost model.Money) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE market_items SET unit_cost = ? WHERE id = ?`, int64(unitCost), itemID)
	return err
}

// ClearMarketItemCosts 清除成本快照（撤销结算时）。
func (s *Store) ClearMarketItemCosts(ctx context.Context, tx DBTX, marketID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE market_items SET unit_cost = NULL WHERE market_id = ?`, marketID)
	return err
}

// DeleteMarketItem 删除明细行。
func (s *Store) DeleteMarketItem(ctx context.Context, tx DBTX, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM market_items WHERE id = ?`, id)
	return err
}

// MaxMarketItemSort 当前最大排序号。
func (s *Store) MaxMarketItemSort(ctx context.Context, tx DBTX, marketID int64) (int, error) {
	var n sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT MAX(sort_order) FROM market_items WHERE market_id = ?`, marketID).Scan(&n)
	return int(nzInt(n)), err
}

// ReplaceMarketExpenses 覆盖式保存市集费用。
func (s *Store) ReplaceMarketExpenses(ctx context.Context, tx DBTX, marketID int64, list []model.MarketExpense) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM market_expenses WHERE market_id = ?`, marketID); err != nil {
		return err
	}
	for _, e := range list {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO market_expenses(market_id, category, amount, calc, rate, note)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			marketID, e.Category, int64(e.Amount), calcOrDefault(e.Calc), e.Rate, e.Note); err != nil {
			return err
		}
	}
	return nil
}

// CountActiveMarkets 进行中的市集数量。
func (s *Store) CountActiveMarkets(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM markets WHERE status IN (?, ?)`,
		model.MarketPlanned, model.MarketOngoing).Scan(&n)
	return n, err
}

// CountMarkets 市集总数与已结算数。
func (s *Store) CountMarkets(ctx context.Context) (total int, settled int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
		 FROM markets`, model.MarketSettled).Scan(&total, &settled)
	return
}

// calcOrDefault 费用计算方式缺省为一口价。
func calcOrDefault(calc string) string {
	if calc == model.ExpenseCalcPercent {
		return model.ExpenseCalcPercent
	}
	return model.ExpenseCalcFixed
}

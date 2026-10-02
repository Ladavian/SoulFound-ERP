package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"icewine-erp/internal/model"
)

const purchaseCols = `p.id, p.code, p.supplier_id, COALESCE(s.name, ''), p.purchase_date,
	p.currency, p.status, p.alloc_method, p.goods_amount, p.shipping_cost, p.tariff_cost, p.other_cost,
	p.total_cost, p.notes, p.created_by, COALESCE(NULLIF(u.full_name, ''), u.username, ''),
	p.confirmed_at, p.created_at, p.updated_at`

const purchaseFrom = ` FROM purchases p
	LEFT JOIN suppliers s ON s.id = p.supplier_id
	LEFT JOIN users u ON u.id = p.created_by`

func scanPurchase(row interface{ Scan(...any) error }) (*model.Purchase, error) {
	var (
		p         model.Purchase
		supplier  sql.NullInt64
		createdBy sql.NullInt64
	)
	err := row.Scan(&p.ID, &p.Code, &supplier, &p.SupplierName, &p.PurchaseDate,
		&p.Currency, &p.Status, &p.AllocMethod, &p.GoodsAmount, &p.ShippingCost, &p.TariffCost, &p.OtherCost,
		&p.TotalCost, &p.Notes, &createdBy, &p.CreatedByName, &p.ConfirmedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.SupplierID = ptrInt(supplier)
	p.CreatedBy = ptrInt(createdBy)
	return &p, nil
}

// PurchaseFilter 采购单筛选条件。
type PurchaseFilter struct {
	Keyword    string
	Status     string
	SupplierID int64
	From       string
	To         string
	Limit      int
}

// ListPurchases 查询采购单列表（含明细行）。
func (s *Store) ListPurchases(ctx context.Context, f PurchaseFilter) ([]model.Purchase, error) {
	var (
		where []string
		args  []any
	)
	if st := strings.TrimSpace(f.Status); st != "" {
		where = append(where, "p.status = ?")
		args = append(args, st)
	}
	if f.SupplierID > 0 {
		where = append(where, "p.supplier_id = ?")
		args = append(args, f.SupplierID)
	}
	if f.From != "" {
		where = append(where, "p.purchase_date >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "p.purchase_date <= ?")
		args = append(args, f.To)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		where = append(where, `(p.code LIKE ? OR p.notes LIKE ? OR s.name LIKE ? OR EXISTS (`+
			`SELECT 1 FROM purchase_items pi JOIN products pr ON pr.id = pi.product_id `+
			`WHERE pi.purchase_id = p.id AND (pr.name LIKE ? OR pr.sku LIKE ?)))`)
		like := "%" + kw + "%"
		args = append(args, like, like, like, like, like)
	}

	query := `SELECT ` + purchaseCols + purchaseFrom + whereClause(where) +
		` ORDER BY p.purchase_date DESC, p.id DESC`
	if f.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, f.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Purchase
	for rows.Next() {
		p, err := scanPurchase(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.attachPurchaseItems(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PurchaseByID 查询单张采购单（含明细）。
func (s *Store) PurchaseByID(ctx context.Context, id int64) (*model.Purchase, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+purchaseCols+purchaseFrom+` WHERE p.id = ?`, id)
	p, err := scanPurchase(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items, err := s.PurchaseItems(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	p.Items = items
	return p, nil
}

// PurchaseItems 查询采购明细。
func (s *Store) PurchaseItems(ctx context.Context, purchaseID int64) ([]model.PurchaseItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pi.id, pi.purchase_id, pi.product_id, p.name, p.sku, pi.qty, pi.unit_price,
		        pi.amount, pi.extra_alloc, pi.landed_unit_cost, pi.note
		 FROM purchase_items pi JOIN products p ON p.id = pi.product_id
		 WHERE pi.purchase_id = ? ORDER BY pi.id`, purchaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.PurchaseItem
	for rows.Next() {
		var it model.PurchaseItem
		if err := rows.Scan(&it.ID, &it.PurchaseID, &it.ProductID, &it.ProductName, &it.ProductSKU,
			&it.Qty, &it.UnitPrice, &it.Amount, &it.ExtraAlloc, &it.LandedUnitCost, &it.Note); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// attachPurchaseItems 批量补齐采购单明细，避免 N+1 查询。
func (s *Store) attachPurchaseItems(ctx context.Context, list []model.Purchase) error {
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
		`SELECT pi.id, pi.purchase_id, pi.product_id, p.name, p.sku, pi.qty, pi.unit_price,
		        pi.amount, pi.extra_alloc, pi.landed_unit_cost, pi.note
		 FROM purchase_items pi JOIN products p ON p.id = pi.product_id
		 WHERE pi.purchase_id IN (`+placeholders(len(ids))+`) ORDER BY pi.id`, idArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var it model.PurchaseItem
		if err := rows.Scan(&it.ID, &it.PurchaseID, &it.ProductID, &it.ProductName, &it.ProductSKU,
			&it.Qty, &it.UnitPrice, &it.Amount, &it.ExtraAlloc, &it.LandedUnitCost, &it.Note); err != nil {
			return err
		}
		if i, ok := index[it.PurchaseID]; ok {
			list[i].Items = append(list[i].Items, it)
		}
	}
	return rows.Err()
}

// CreatePurchase 新建采购单（头部）。
func (s *Store) CreatePurchase(ctx context.Context, tx DBTX, p *model.Purchase) (int64, error) {
	now := Now()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO purchases(code, supplier_id, purchase_date, currency, status, alloc_method,
		        goods_amount, shipping_cost, tariff_cost, other_cost, total_cost,
		        notes, created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Code, p.SupplierID, p.PurchaseDate, p.Currency, p.Status, p.AllocMethod,
		int64(p.GoodsAmount), int64(p.ShippingCost), int64(p.TariffCost), int64(p.OtherCost),
		int64(p.TotalCost), p.Notes, p.CreatedBy, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePurchase 更新采购单头部。
func (s *Store) UpdatePurchase(ctx context.Context, tx DBTX, p *model.Purchase) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE purchases SET supplier_id = ?, purchase_date = ?, currency = ?, status = ?,
		        alloc_method = ?, goods_amount = ?, shipping_cost = ?, tariff_cost = ?,
		        other_cost = ?, total_cost = ?, notes = ?, updated_at = ?
		 WHERE id = ?`,
		p.SupplierID, p.PurchaseDate, p.Currency, p.Status, p.AllocMethod,
		int64(p.GoodsAmount), int64(p.ShippingCost), int64(p.TariffCost), int64(p.OtherCost),
		int64(p.TotalCost), p.Notes, Now(), p.ID)
	return err
}

// ReplacePurchaseItems 覆盖式保存采购明细。
func (s *Store) ReplacePurchaseItems(ctx context.Context, tx DBTX, purchaseID int64, items []model.PurchaseItem) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM purchase_items WHERE purchase_id = ?`, purchaseID); err != nil {
		return err
	}
	for _, it := range items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO purchase_items(purchase_id, product_id, qty, unit_price, amount,
			        extra_alloc, landed_unit_cost, note)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			purchaseID, it.ProductID, int64(it.Qty), int64(it.UnitPrice), int64(it.Amount),
			int64(it.ExtraAlloc), int64(it.LandedUnitCost), it.Note); err != nil {
			return err
		}
	}
	return nil
}

// SetPurchaseStatus 更新采购单状态。
func (s *Store) SetPurchaseStatus(ctx context.Context, tx DBTX, id int64, status, confirmedAt string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE purchases SET status = ?, confirmed_at = ?, updated_at = ? WHERE id = ?`,
		status, confirmedAt, Now(), id)
	return err
}

// DeletePurchase 删除采购单（明细级联删除；已入库需先撤销）。
func (s *Store) DeletePurchase(ctx context.Context, tx DBTX, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM purchases WHERE id = ?`, id)
	return err
}

// CountDraftPurchases 草稿采购单数量。
func (s *Store) CountDraftPurchases(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purchases WHERE status = ?`, model.PurchaseDraft).Scan(&n)
	return n, err
}

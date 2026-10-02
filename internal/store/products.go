package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"icewine-erp/internal/model"
)

const productCols = `p.id, p.sku, p.name, p.name_en, p.category, p.vintage, p.volume_ml,
	p.unit, p.bottles_per_case, p.sale_price, p.low_stock_qty, p.supplier_id,
	COALESCE(s.name, ''), p.image_url, p.notes, p.barcode, p.is_active,
	p.stock_qty, p.avg_cost, p.stock_value, p.created_at, p.updated_at`

const productFrom = ` FROM products p LEFT JOIN suppliers s ON s.id = p.supplier_id`

func scanProduct(row interface{ Scan(...any) error }) (*model.Product, error) {
	var (
		p          model.Product
		supplierID sql.NullInt64
		isActive   int64
	)
	err := row.Scan(&p.ID, &p.SKU, &p.Name, &p.NameEn, &p.Category, &p.Vintage, &p.VolumeML,
		&p.Unit, &p.BottlesPerCase, &p.SalePrice, &p.LowStockQty, &supplierID,
		&p.SupplierName, &p.ImageURL, &p.Notes, &p.Barcode, &isActive,
		&p.StockQty, &p.AvgCost, &p.StockValue, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.SupplierID = ptrInt(supplierID)
	p.IsActive = i2b(isActive)
	return &p, nil
}

// ProductFilter 产品列表筛选条件。
type ProductFilter struct {
	Keyword         string
	Category        string
	SupplierID      int64
	LowStockOnly    bool
	IncludeInactive bool
	Sort            string
}

// ListProducts 查询产品列表。
func (s *Store) ListProducts(ctx context.Context, f ProductFilter) ([]model.Product, error) {
	var (
		where []string
		args  []any
	)
	if !f.IncludeInactive {
		where = append(where, "p.is_active = 1")
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		where = append(where, "(p.name LIKE ? OR p.sku LIKE ? OR p.name_en LIKE ? OR p.notes LIKE ? OR p.barcode LIKE ?)")
		like := "%" + kw + "%"
		args = append(args, like, like, like, like, like)
	}
	if f.Category != "" {
		where = append(where, "p.category = ?")
		args = append(args, f.Category)
	}
	if f.SupplierID > 0 {
		where = append(where, "p.supplier_id = ?")
		args = append(args, f.SupplierID)
	}
	if f.LowStockOnly {
		where = append(where, "p.stock_qty <= p.low_stock_qty")
	}

	order := "p.category, p.name"
	switch f.Sort {
	case "stock":
		order = "p.stock_qty DESC, p.name"
	case "stock_asc":
		order = "p.stock_qty ASC, p.name"
	case "value":
		order = "p.stock_value DESC, p.name"
	case "price":
		order = "p.sale_price DESC, p.name"
	case "sku":
		order = "p.sku"
	case "updated":
		order = "p.updated_at DESC, p.id DESC"
	}

	query := `SELECT ` + productCols + productFrom
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY " + order

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ProductByID 按 ID 查询产品。
func (s *Store) ProductByID(ctx context.Context, id int64) (*model.Product, error) {
	return productByID(ctx, s.db, id)
}

// ProductByIDTx 在事务内按 ID 查询产品。
func (s *Store) ProductByIDTx(ctx context.Context, tx DBTX, id int64) (*model.Product, error) {
	return productByID(ctx, tx, id)
}

func productByID(ctx context.Context, q DBTX, id int64) (*model.Product, error) {
	row := q.QueryRowContext(ctx, `SELECT `+productCols+productFrom+` WHERE p.id = ?`, id)
	p, err := scanProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// ProductsForUpdate 在事务内按 ID 批量锁定读取产品（SQLite 事务本身串行，此处仅统一入口）。
func (s *Store) ProductsForUpdate(ctx context.Context, tx DBTX, ids []int64) (map[int64]*model.Product, error) {
	out := make(map[int64]*model.Product, len(ids))
	for _, id := range ids {
		p, err := productByID(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if p != nil {
			out[id] = p
		}
	}
	return out, nil
}

// ProductBySKU 按 SKU 查询产品。
func (s *Store) ProductBySKU(ctx context.Context, sku string) (*model.Product, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+productCols+productFrom+` WHERE p.sku = ?`, sku)
	p, err := scanProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// ProductsByIDs 批量查询产品。
func (s *Store) ProductsByIDs(ctx context.Context, ids []int64) (map[int64]model.Product, error) {
	out := map[int64]model.Product{}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+productCols+productFrom+` WHERE p.id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out[p.ID] = *p
	}
	return out, rows.Err()
}

// ListProductOptions 用于下拉选择的产品列表（仅启用中的）。
func (s *Store) ListProductOptions(ctx context.Context) ([]model.Product, error) {
	return s.ListProducts(ctx, ProductFilter{})
}

// CreateProduct 新建产品。
func (s *Store) CreateProduct(ctx context.Context, p *model.Product) (int64, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO products(sku, name, name_en, category, vintage, volume_ml, unit,
		        bottles_per_case, sale_price, low_stock_qty, supplier_id, image_url,
		        notes, barcode, is_active, stock_qty, avg_cost, stock_value, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?)`,
		p.SKU, p.Name, p.NameEn, p.Category, p.Vintage, p.VolumeML, p.Unit,
		p.BottlesPerCase, int64(p.SalePrice), int64(p.LowStockQty), p.SupplierID, p.ImageURL,
		p.Notes, p.Barcode, b2i(p.IsActive), now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateProduct 更新产品档案（不触碰库存与成本字段，那些由库存服务维护）。
func (s *Store) UpdateProduct(ctx context.Context, p *model.Product) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE products SET sku = ?, name = ?, name_en = ?, category = ?, vintage = ?,
		        volume_ml = ?, unit = ?, bottles_per_case = ?, sale_price = ?,
		        low_stock_qty = ?, supplier_id = ?, image_url = ?, notes = ?, barcode = ?,
		        is_active = ?, updated_at = ?
		 WHERE id = ?`,
		p.SKU, p.Name, p.NameEn, p.Category, p.Vintage, p.VolumeML, p.Unit,
		p.BottlesPerCase, int64(p.SalePrice), int64(p.LowStockQty), p.SupplierID,
		p.ImageURL, p.Notes, p.Barcode, b2i(p.IsActive), Now(), p.ID)
	return err
}

// SetProductActive 启用/停用产品。
func (s *Store) SetProductActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE products SET is_active = ?, updated_at = ? WHERE id = ?`, b2i(active), Now(), id)
	return err
}

// DeleteProduct 删除产品；若已产生库存流水则拒绝删除。
func (s *Store) DeleteProduct(ctx context.Context, id int64) error {
	var moves, items int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM stock_movements WHERE product_id = ?`, id).Scan(&moves); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM purchase_items WHERE product_id = ?`, id).Scan(&items); err != nil {
		return err
	}
	if moves > 0 || items > 0 {
		return fmt.Errorf("该产品已有 %d 条库存流水，不能删除；请改为「停用」以保留历史数据", moves)
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM products WHERE id = ?`, id)
	return err
}

// CountProducts 产品数量统计。
func (s *Store) CountProducts(ctx context.Context) (total int, lowStock int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(CASE WHEN stock_qty <= low_stock_qty THEN 1 ELSE 0 END), 0)
		 FROM products WHERE is_active = 1`).Scan(&total, &lowStock)
	return
}

// LowStockProducts 低库存产品。
func (s *Store) LowStockProducts(ctx context.Context, limit int) ([]model.Product, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+productCols+productFrom+`
		 WHERE p.is_active = 1 AND p.stock_qty <= p.low_stock_qty
		 ORDER BY (p.stock_qty - p.low_stock_qty) ASC, p.name LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CategoriesInUse 已使用的品类。
func (s *Store) CategoriesInUse(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT category FROM products WHERE category <> '' ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateProductStock 由库存服务调用，写入产品的库存/成本缓存。
func (s *Store) UpdateProductStock(ctx context.Context, tx DBTX, id int64, qty model.Qty, avg model.Money, value model.Money) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE products SET stock_qty = ?, avg_cost = ?, stock_value = ?, updated_at = ?
		 WHERE id = ?`, int64(qty), int64(avg), int64(value), Now(), id)
	return err
}

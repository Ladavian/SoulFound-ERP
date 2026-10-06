package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"icewine-erp/internal/model"
)

// ---------------------------------------------------------------- 虚拟组套

const bundleCols = `b.id, b.product_id, b.component_id, b.qty, b.sort_order,
	c.name, c.sku, c.unit, c.ec_cost, c.avg_cost`

const bundleFrom = ` FROM product_bundles b
	JOIN products c ON c.id = b.component_id`

func scanBundle(row interface{ Scan(...any) error }) (*model.ProductBundle, error) {
	var b model.ProductBundle
	err := row.Scan(&b.ID, &b.ProductID, &b.ComponentID, &b.Qty, &b.SortOrder,
		&b.ComponentName, &b.ComponentSKU, &b.ComponentUnit, &b.ComponentEC, &b.ComponentAvg)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// BundlesOf 某个组套产品的组成。
func (s *Store) BundlesOf(ctx context.Context, productID int64) ([]model.ProductBundle, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+bundleCols+bundleFrom+` WHERE b.product_id = ? ORDER BY b.sort_order, b.id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProductBundle
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// AllBundles 全部组套（按产品分组，供产品列表显示"组套"标记）。
func (s *Store) AllBundles(ctx context.Context) (map[int64][]model.ProductBundle, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bundleCols+bundleFrom+` ORDER BY b.product_id, b.sort_order, b.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]model.ProductBundle{}
	for rows.Next() {
		b, err := scanBundle(rows)
		if err != nil {
			return nil, err
		}
		out[b.ProductID] = append(out[b.ProductID], *b)
	}
	return out, rows.Err()
}

// ReplaceBundles 覆盖保存某个产品的组套组成。
func (s *Store) ReplaceBundles(ctx context.Context, tx DBTX, productID int64, list []model.ProductBundle) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_bundles WHERE product_id = ?`, productID); err != nil {
		return err
	}
	for i, b := range list {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO product_bundles(product_id, component_id, qty, sort_order)
			 VALUES (?, ?, ?, ?)`,
			productID, b.ComponentID, int64(b.Qty), i+1); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- 平台订单

// EcOrderFilter 平台订单查询条件。
type EcOrderFilter struct {
	Platform  string
	Status    string
	Keyword   string // 订单号 / 商品标题 / 买家留言
	From      string
	To        string
	Unmatched bool // 只看还有未匹配商品的订单
	Limit     int
	Offset    int
}

func (f EcOrderFilter) where() (string, []any) {
	var (
		conds []string
		args  []any
	)
	if f.Platform != "" {
		conds = append(conds, "o.platform = ?")
		args = append(args, f.Platform)
	}
	if f.Status != "" {
		conds = append(conds, "o.status = ?")
		args = append(args, f.Status)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		conds = append(conds, `(o.order_no LIKE ? OR o.buyer_note LIKE ? OR o.seller_note LIKE ?
			OR EXISTS (SELECT 1 FROM ec_order_items i WHERE i.order_id = o.id
			           AND (i.title LIKE ? OR i.sub_order_no LIKE ? OR i.ec_product_id LIKE ?)))`)
		args = append(args, like, like, like, like, like, like)
	}
	if f.From != "" {
		conds = append(conds, "date(o.created_at) >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		conds = append(conds, "date(o.created_at) <= ?")
		args = append(args, f.To)
	}
	if f.Unmatched {
		conds = append(conds, `EXISTS (SELECT 1 FROM ec_order_items i
			WHERE i.order_id = o.id AND i.product_id IS NULL)`)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListEcOrders 平台订单列表（含明细）。
func (s *Store) ListEcOrders(ctx context.Context, f EcOrderFilter) ([]model.EcOrder, error) {
	where, args := f.where()
	query := `SELECT o.id, o.platform, o.order_no, o.status, o.refund_status, o.created_at,
	                 o.paid_at, o.shipped_at, o.buyer_note, o.seller_note, o.imported_at
	          FROM ec_orders o` + where + ` ORDER BY o.created_at DESC, o.id DESC`
	if f.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.EcOrder
	for rows.Next() {
		var o model.EcOrder
		if err := rows.Scan(&o.ID, &o.Platform, &o.OrderNo, &o.Status, &o.RefundStatus,
			&o.CreatedAt, &o.PaidAt, &o.ShippedAt, &o.BuyerNote, &o.SellerNote, &o.ImportedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.attachEcItems(ctx, out)
}

// CountEcOrders 统计条数。
func (s *Store) CountEcOrders(ctx context.Context, f EcOrderFilter) (int, error) {
	where, args := f.where()
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ec_orders o`+where, args...).Scan(&n)
	return n, err
}

// EcOrderByID 单张订单（含明细）。
func (s *Store) EcOrderByID(ctx context.Context, id int64) (*model.EcOrder, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, platform, order_no, status, refund_status, created_at,
		        paid_at, shipped_at, buyer_note, seller_note, imported_at
		   FROM ec_orders WHERE id = ?`, id)
	var o model.EcOrder
	err := row.Scan(&o.ID, &o.Platform, &o.OrderNo, &o.Status, &o.RefundStatus,
		&o.CreatedAt, &o.PaidAt, &o.ShippedAt, &o.BuyerNote, &o.SellerNote, &o.ImportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := []model.EcOrder{o}
	if err := s.attachEcItems(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

const ecItemCols = `i.id, i.order_id, i.platform, i.sub_order_no, i.title, i.ec_product_id,
	i.ec_sku, i.merchant_code, i.qty, i.unit_price, i.payable_amount, i.paid_amount,
	i.refund_status, i.refund_amount, i.item_status, i.logistics_no, i.logistics_company,
	i.product_id, i.imported_at,
	COALESCE(p.name, ''), COALESCE(p.sku, ''), COALESCE(p.ec_cost, 0)`

const ecItemFrom = ` FROM ec_order_items i
	LEFT JOIN products p ON p.id = i.product_id`

// attachEcItems 批量补齐明细，避免 N+1。
func (s *Store) attachEcItems(ctx context.Context, list []model.EcOrder) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(list))
	index := map[int64]int{}
	for i := range list {
		ids = append(ids, list[i].ID)
		index[list[i].ID] = i
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+ecItemCols+ecItemFrom+` WHERE i.order_id IN (`+placeholders(len(ids))+`)
		  ORDER BY i.id`, idArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanEcItem(rows)
		if err != nil {
			return err
		}
		if i, ok := index[it.OrderID]; ok {
			list[i].Items = append(list[i].Items, *it)
		}
	}
	return rows.Err()
}

func scanEcItem(row interface{ Scan(...any) error }) (*model.EcOrderItem, error) {
	var it model.EcOrderItem
	if err := row.Scan(&it.ID, &it.OrderID, &it.Platform, &it.SubOrderNo, &it.Title,
		&it.EcProductID, &it.EcSKU, &it.MerchantCode, &it.Qty, &it.UnitPrice,
		&it.PayableAmount, &it.PaidAmount, &it.RefundStatus, &it.RefundAmount,
		&it.ItemStatus, &it.LogisticsNo, &it.LogisticsCo, &it.ProductID, &it.ImportedAt,
		&it.ProductName, &it.ProductSKU, &it.EcCost); err != nil {
		return nil, err
	}
	return &it, nil
}

// UpsertEcOrder 写入或更新一张平台订单，返回订单 ID 与是否新建。
func (s *Store) UpsertEcOrder(ctx context.Context, tx DBTX, o *model.EcOrder) (int64, bool, error) {
	var existing int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM ec_orders WHERE platform = ? AND order_no = ?`,
		o.Platform, o.OrderNo).Scan(&existing)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE ec_orders SET status = ?, refund_status = ?, created_at = ?, paid_at = ?,
			        shipped_at = ?, buyer_note = ?, seller_note = ?, imported_at = ?
			 WHERE id = ?`,
			o.Status, o.RefundStatus, o.CreatedAt, o.PaidAt, o.ShippedAt,
			o.BuyerNote, o.SellerNote, Now(), existing); err != nil {
			return 0, false, err
		}
		return existing, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO ec_orders(platform, order_no, status, refund_status, created_at,
		        paid_at, shipped_at, buyer_note, seller_note, imported_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.Platform, o.OrderNo, o.Status, o.RefundStatus, o.CreatedAt,
		o.PaidAt, o.ShippedAt, o.BuyerNote, o.SellerNote, Now())
	if err != nil {
		return 0, false, err
	}
	id, err := res.LastInsertId()
	return id, true, err
}

// UpsertEcItem 写入或更新一条订单明细。
func (s *Store) UpsertEcItem(ctx context.Context, tx DBTX, it *model.EcOrderItem) (int64, bool, error) {
	var existing int64
	err := tx.QueryRowContext(ctx,
		`SELECT id FROM ec_order_items WHERE platform = ? AND sub_order_no = ?`,
		it.Platform, it.SubOrderNo).Scan(&existing)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE ec_order_items SET order_id = ?, title = ?, ec_product_id = ?, ec_sku = ?,
			        merchant_code = ?, qty = ?, unit_price = ?, payable_amount = ?, paid_amount = ?,
			        refund_status = ?, refund_amount = ?, item_status = ?, logistics_no = ?,
			        logistics_company = ?, product_id = ?, imported_at = ?
			 WHERE id = ?`,
			it.OrderID, it.Title, it.EcProductID, it.EcSKU, it.MerchantCode, int64(it.Qty),
			int64(it.UnitPrice), int64(it.PayableAmount), int64(it.PaidAmount),
			it.RefundStatus, int64(it.RefundAmount), it.ItemStatus, it.LogisticsNo,
			it.LogisticsCo, it.ProductID, Now(), existing); err != nil {
			return 0, false, err
		}
		return existing, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO ec_order_items(order_id, platform, sub_order_no, title, ec_product_id,
		        ec_sku, merchant_code, qty, unit_price, payable_amount, paid_amount,
		        refund_status, refund_amount, item_status, logistics_no, logistics_company,
		        product_id, imported_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.OrderID, it.Platform, it.SubOrderNo, it.Title, it.EcProductID,
		it.EcSKU, it.MerchantCode, int64(it.Qty), int64(it.UnitPrice), int64(it.PayableAmount),
		int64(it.PaidAmount), it.RefundStatus, int64(it.RefundAmount), it.ItemStatus,
		it.LogisticsNo, it.LogisticsCo, it.ProductID, Now())
	if err != nil {
		return 0, false, err
	}
	id, err := res.LastInsertId()
	return id, true, err
}

// ---------------------------------------------------------------- 平台商品绑定

const ecLinkCols = `l.id, l.product_id, l.platform, l.ec_product_id, l.ec_sku_id,
	l.title, l.note, l.created_at, p.name, p.sku`

const ecLinkFrom = ` FROM product_ec_links l
	JOIN products p ON p.id = l.product_id`

func scanEcLink(row interface{ Scan(...any) error }) (*model.ProductEcLink, error) {
	var l model.ProductEcLink
	if err := row.Scan(&l.ID, &l.ProductID, &l.Platform, &l.EcProductID, &l.EcSKUId,
		&l.Title, &l.Note, &l.CreatedAt, &l.ProductName, &l.ProductSKU); err != nil {
		return nil, err
	}
	return &l, nil
}

// ProductLinks 某个产品绑定的全部平台商品。
func (s *Store) ProductLinks(ctx context.Context, productID int64) ([]model.ProductEcLink, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+ecLinkCols+ecLinkFrom+` WHERE l.product_id = ?
		  ORDER BY l.platform, l.id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProductEcLink
	for rows.Next() {
		l, err := scanEcLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// AllProductLinks 全部绑定关系（按产品分组，列表页显示"已绑 N 个链接"）。
func (s *Store) AllProductLinks(ctx context.Context) (map[int64][]model.ProductEcLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ecLinkCols+ecLinkFrom+` ORDER BY l.product_id, l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]model.ProductEcLink{}
	for rows.Next() {
		l, err := scanEcLink(rows)
		if err != nil {
			return nil, err
		}
		out[l.ProductID] = append(out[l.ProductID], *l)
	}
	return out, rows.Err()
}

// ProductByEcLink 按平台商品ID + 规格 找绑定的产品。
//
// 一个商品ID 下可能有多个规格（淘宝的「商品属性」），
// 各规格可以绑到不同产品，所以匹配必须带上规格：
//   - 订单行有规格 → 按（平台, 商品ID, 规格）精确匹配；
//   - 订单行没有规格 → 只有该商品ID **只绑了一条** 时才认，
//     绑了多条就是歧义，宁可留空让人去绑，也不要猜错。
func (s *Store) ProductByEcLink(ctx context.Context, platform, ecID, sku string) (*model.Product, error) {
	ecID = strings.TrimSpace(ecID)
	if ecID == "" {
		return nil, nil
	}
	sku = model.NormalizeEcSKU(sku)
	if sku != "" {
		row := s.db.QueryRowContext(ctx,
			`SELECT `+productCols+productFrom+`
			  WHERE p.id = (SELECT product_id FROM product_ec_links
			                 WHERE platform = ? AND ec_product_id = ? AND ec_sku_id = ? LIMIT 1)`,
			platform, ecID, sku)
		p, err := scanProduct(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return p, err
	}
	// 没有规格：只有唯一一条绑定时才能确定
	rows, err := s.db.QueryContext(ctx,
		`SELECT product_id FROM product_ec_links WHERE platform = ? AND ec_product_id = ?`,
		platform, ecID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != 1 {
		return nil, nil // 没绑或绑了多条（歧义）
	}
	return s.ProductByID(ctx, ids[0])
}

// EcLinkByID 单条绑定。
func (s *Store) EcLinkByID(ctx context.Context, id int64) (*model.ProductEcLink, error) {
	return ecLinkByID(ctx, s.db, id)
}

// EcLinkByIDTx 事务内单条绑定。
func (s *Store) EcLinkByIDTx(ctx context.Context, tx DBTX, id int64) (*model.ProductEcLink, error) {
	return ecLinkByID(ctx, tx, id)
}

func ecLinkByID(ctx context.Context, q DBTX, id int64) (*model.ProductEcLink, error) {
	row := q.QueryRowContext(ctx, `SELECT `+ecLinkCols+ecLinkFrom+` WHERE l.id = ?`, id)
	l, err := scanEcLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

// EcLinkByEcID 按平台 + 商品ID + 规格 查绑定。
func (s *Store) EcLinkByEcID(ctx context.Context, platform, ecID, sku string) (*model.ProductEcLink, error) {
	return ecLinkByEcID(ctx, s.db, platform, ecID, sku)
}

// EcLinkByEcIDTx 事务内查绑定。
func (s *Store) EcLinkByEcIDTx(ctx context.Context, tx DBTX, platform, ecID, sku string) (*model.ProductEcLink, error) {
	return ecLinkByEcID(ctx, tx, platform, ecID, sku)
}

func ecLinkByEcID(ctx context.Context, q DBTX, platform, ecID, sku string) (*model.ProductEcLink, error) {
	ecID = strings.TrimSpace(ecID)
	if ecID == "" {
		return nil, nil
	}
	row := q.QueryRowContext(ctx,
		`SELECT `+ecLinkCols+ecLinkFrom+`
		  WHERE l.platform = ? AND l.ec_product_id = ? AND l.ec_sku_id = ? LIMIT 1`,
		platform, ecID, model.NormalizeEcSKU(sku))
	l, err := scanEcLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

// ProductLinksTx 事务内查某个产品的绑定。
func (s *Store) ProductLinksTx(ctx context.Context, tx DBTX, productID int64) ([]model.ProductEcLink, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+ecLinkCols+ecLinkFrom+` WHERE l.product_id = ? ORDER BY l.platform, l.id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProductEcLink
	for rows.Next() {
		l, err := scanEcLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// AddEcLink 新增一条产品与平台商品的绑定。
//
// 同一个平台商品ID 只能属于一个产品，冲突时返回已有的那条，
// 让上层给出"已绑给谁"的提示。
func (s *Store) AddEcLink(ctx context.Context, tx DBTX, l model.ProductEcLink) (*model.ProductEcLink, error) {
	// 查重必须走 tx：连接池只有 1 个连接，事务占着它，
	// 这里再走 s.db 会自死锁（第一版就是这么挂的）。
	if existing, err := ecLinkByEcID(ctx, tx, l.Platform, l.EcProductID, l.EcSKUId); err != nil {
		return nil, err
	} else if existing != nil && existing.ProductID != l.ProductID {
		return existing, nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO product_ec_links(product_id, platform, ec_product_id, ec_sku_id, title, note, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(platform, ec_product_id, ec_sku_id)
		 DO UPDATE SET product_id = excluded.product_id, title = excluded.title, note = excluded.note`,
		l.ProductID, l.Platform, l.EcProductID, l.EcSKUId, l.Title, l.Note, Now())
	if err != nil {
		return nil, err
	}
	return nil, nil
}

// DeleteEcLink 解除一条绑定，并把它匹配过的订单明细松绑。
func (s *Store) DeleteEcLink(ctx context.Context, tx DBTX, id int64) error {
	l, err := ecLinkByID(ctx, tx, id)
	if err != nil || l == nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM product_ec_links WHERE id = ?`, id); err != nil {
		return err
	}
	// 同一个（商品ID + 规格）可能还有别的绑定（一般不会，唯一索引挡着），
	// 但同一商品ID 的**其它规格**属于别的产品，不能一起松绑。
	var still int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_ec_links
		  WHERE platform = ? AND ec_product_id = ? AND ec_sku_id = ?`,
		l.Platform, l.EcProductID, l.EcSKUId).Scan(&still); err != nil {
		return err
	}
	if still > 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE ec_order_items SET product_id = NULL
		  WHERE platform = ? AND ec_product_id = ? AND ec_sku = ? AND product_id = ?`,
		l.Platform, l.EcProductID, l.EcSKUId, l.ProductID)
	return err
}

// BindEcOrderItems 把某个平台商品ID 下未匹配的订单明细一次性绑到产品。
func (s *Store) BindEcOrderItems(ctx context.Context, tx DBTX, platform, ecID, sku string, productID int64) (int, error) {
	res, err := tx.ExecContext(ctx,
		`UPDATE ec_order_items SET product_id = ?
		  WHERE product_id IS NULL AND platform = ? AND ec_product_id = ? AND ec_sku = ?`,
		productID, platform, ecID, model.NormalizeEcSKU(sku))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// EcItemByID 单条订单明细。
func (s *Store) EcItemByID(ctx context.Context, id int64) (*model.EcOrderItem, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+ecItemCols+ecItemFrom+` WHERE i.id = ?`, id)
	it, err := scanEcItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return it, err
}

// BindEcItem 把一条订单明细绑到产品上。
func (s *Store) BindEcItem(ctx context.Context, tx DBTX, itemID, productID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE ec_order_items SET product_id = ? WHERE id = ?`, productID, itemID)
	return err
}

// BindEcProductID 把电商商品ID写到产品上，并顺带把历史未匹配的行一起绑好。
//
// 返回顺带补齐的历史行数：同一个商品ID 往往在多次导入里都出现过，
// 绑一次就都该对上，不要让人一行行点。
func (s *Store) BindEcProductID(ctx context.Context, tx DBTX, ecID string, productID int64) (int, error) {
	ecID = strings.TrimSpace(ecID)
	if ecID == "" {
		return 0, nil
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE products SET ec_product_id = ?, updated_at = ? WHERE id = ?`,
		ecID, Now(), productID); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE ec_order_items SET product_id = ?
		 WHERE product_id IS NULL AND ec_product_id = ?`, productID, ecID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// EcUnmatched 汇总还没绑定产品的电商商品（按电商商品ID + 规格归并）。
func (s *Store) EcUnmatched(ctx context.Context, platform string, limit int) ([]model.EcUnmatchedItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT i.ec_product_id, i.ec_sku, MAX(i.title), MAX(i.merchant_code),
		        COALESCE(SUM(i.qty), 0), COUNT(DISTINCT i.order_id)
		   FROM ec_order_items i
		  WHERE i.product_id IS NULL AND i.platform = ?
		  GROUP BY i.ec_product_id, i.ec_sku
		  ORDER BY COUNT(DISTINCT i.order_id) DESC, MAX(i.id) DESC
		  LIMIT ?`, platform, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.EcUnmatchedItem
	for rows.Next() {
		var u model.EcUnmatchedItem
		if err := rows.Scan(&u.EcProductID, &u.EcSKU, &u.Title, &u.MerchantCode,
			&u.Qty, &u.Orders); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// EcOrderStatusCounts 各状态的订单数（筛选用）。
func (s *Store) EcOrderStatusCounts(ctx context.Context, platform string) ([]model.Option, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM ec_orders WHERE platform = ?
		  GROUP BY status ORDER BY COUNT(*) DESC`, platform)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Option
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out = append(out, model.Option{Value: st, Label: ecStatusLabel(st, n)})
	}
	return out, rows.Err()
}

func ecStatusLabel(status string, n int) string {
	if status == "" {
		status = "未知"
	}
	return status
}

// CountEcUnmatched 还有多少行没绑产品。
func (s *Store) CountEcUnmatched(ctx context.Context, platform string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ec_order_items WHERE product_id IS NULL AND platform = ?`,
		platform).Scan(&n)
	return n, err
}

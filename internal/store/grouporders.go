package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"icewine-erp/internal/model"
)

const groupCols = `g.id, g.code, g.customer_id, g.customer_name, g.contact, g.phone,
	g.order_date, g.ship_date, g.warehouse, g.status, g.discount, g.extra_fee,
	g.note, g.created_by, COALESCE(NULLIF(u.full_name, ''), u.username, ''),
	g.created_at, g.updated_at`

const groupFrom = ` FROM group_orders g
	LEFT JOIN users u ON u.id = g.created_by`

func scanGroupOrder(row interface{ Scan(...any) error }) (*model.GroupOrder, error) {
	var o model.GroupOrder
	err := row.Scan(&o.ID, &o.Code, &o.CustomerID, &o.CustomerName, &o.Contact, &o.Phone,
		&o.OrderDate, &o.ShipDate, &o.Warehouse, &o.Status, &o.Discount, &o.ExtraFee,
		&o.Note, &o.CreatedBy, &o.CreatedByName, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// GroupOrderFilter 团单列表筛选条件。
type GroupOrderFilter struct {
	Keyword string
	Status  string
	From    string
	To      string
	Sort    string
	Limit   int
	Offset  int
}

func (f GroupOrderFilter) where() (string, []any) {
	var (
		conds []string
		args  []any
	)
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		like := "%" + kw + "%"
		conds = append(conds, `(g.code LIKE ? OR g.customer_name LIKE ? OR g.contact LIKE ?
			OR g.phone LIKE ? OR g.warehouse LIKE ? OR g.note LIKE ?
			OR EXISTS (SELECT 1 FROM group_order_items gi WHERE gi.order_id = g.id
			           AND (gi.product_name LIKE ? OR gi.sku LIKE ?)))`)
		args = append(args, like, like, like, like, like, like, like, like)
	}
	if f.Status != "" {
		conds = append(conds, "g.status = ?")
		args = append(args, f.Status)
	}
	if f.From != "" {
		conds = append(conds, "g.order_date >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		conds = append(conds, "g.order_date <= ?")
		args = append(args, f.To)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (f GroupOrderFilter) order() string {
	switch f.Sort {
	case "oldest":
		return " ORDER BY g.order_date ASC, g.id ASC"
	case "amount":
		return ` ORDER BY (SELECT COALESCE(SUM(qty * unit_price), 0) FROM group_order_items` +
			` WHERE order_id = g.id) - g.discount + g.extra_fee DESC, g.id DESC`
	default:
		return " ORDER BY g.order_date DESC, g.id DESC"
	}
}

// ListGroupOrders 查询团单列表（含明细，便于列表直接算金额）。
func (s *Store) ListGroupOrders(ctx context.Context, f GroupOrderFilter) ([]model.GroupOrder, error) {
	where, args := f.where()
	query := `SELECT ` + groupCols + groupFrom + where + f.order()
	if f.Limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.GroupOrder
	for rows.Next() {
		o, err := scanGroupOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.attachGroupItems(ctx, out)
}

// CountGroupOrders 统计条数（分页用）。
func (s *Store) CountGroupOrders(ctx context.Context, f GroupOrderFilter) (int, error) {
	where, args := f.where()
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_orders g`+where, args...).Scan(&n)
	return n, err
}

// GroupOrderByID 按 ID 查询团单（含明细）。
func (s *Store) GroupOrderByID(ctx context.Context, id int64) (*model.GroupOrder, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+groupCols+groupFrom+` WHERE g.id = ?`, id)
	o, err := scanGroupOrder(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	list := []model.GroupOrder{*o}
	if err := s.attachGroupItems(ctx, list); err != nil {
		return nil, err
	}
	return &list[0], nil
}

// GroupOrderStatus 只取状态，用于写操作前的校验。
func (s *Store) GroupOrderStatus(ctx context.Context, tx DBTX, id int64) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM group_orders WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, err
}

// attachGroupItems 批量补齐明细，避免 N+1。
func (s *Store) attachGroupItems(ctx context.Context, list []model.GroupOrder) error {
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
		`SELECT id, order_id, product_id, product_name, sku, qty, unit, unit_price,
		        unit_cost, note, sort_order
		   FROM group_order_items WHERE order_id IN (`+placeholders(len(ids))+`)
		  ORDER BY sort_order, id`, idArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var it model.GroupOrderItem
		if err := rows.Scan(&it.ID, &it.OrderID, &it.ProductID, &it.ProductName, &it.SKU,
			&it.Qty, &it.Unit, &it.UnitPrice, &it.UnitCost, &it.Note, &it.SortOrder); err != nil {
			return err
		}
		if i, ok := index[it.OrderID]; ok {
			list[i].Items = append(list[i].Items, it)
		}
	}
	return rows.Err()
}

// InsertGroupOrder 写入团单主表。
func (s *Store) InsertGroupOrder(ctx context.Context, tx DBTX, o *model.GroupOrder) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO group_orders(code, customer_id, customer_name, contact, phone,
		        order_date, ship_date, warehouse, status, discount, extra_fee, note,
		        created_by, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.Code, o.CustomerID, o.CustomerName, o.Contact, o.Phone,
		o.OrderDate, o.ShipDate, o.Warehouse, o.Status, int64(o.Discount), int64(o.ExtraFee),
		o.Note, o.CreatedBy, Now(), Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateGroupOrder 更新团单主表。
func (s *Store) UpdateGroupOrder(ctx context.Context, tx DBTX, o *model.GroupOrder) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE group_orders SET customer_id = ?, customer_name = ?, contact = ?, phone = ?,
		        order_date = ?, ship_date = ?, warehouse = ?, discount = ?, extra_fee = ?,
		        note = ?, updated_at = ?
		 WHERE id = ?`,
		o.CustomerID, o.CustomerName, o.Contact, o.Phone,
		o.OrderDate, o.ShipDate, o.Warehouse, int64(o.Discount), int64(o.ExtraFee),
		o.Note, Now(), o.ID)
	return err
}

// SetGroupOrderStatus 更新状态。
func (s *Store) SetGroupOrderStatus(ctx context.Context, tx DBTX, id int64, status string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE group_orders SET status = ?, updated_at = ? WHERE id = ?`, status, Now(), id)
	return err
}

// ReplaceGroupItems 覆盖保存团单明细。
func (s *Store) ReplaceGroupItems(ctx context.Context, tx DBTX, orderID int64, items []model.GroupOrderItem) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_order_items WHERE order_id = ?`, orderID); err != nil {
		return err
	}
	for i, it := range items {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO group_order_items(order_id, product_id, product_name, sku, qty, unit,
			        unit_price, unit_cost, note, sort_order)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			orderID, it.ProductID, it.ProductName, it.SKU, int64(it.Qty), it.Unit,
			int64(it.UnitPrice), int64(it.UnitCost), it.Note, i+1); err != nil {
			return err
		}
	}
	return nil
}

// DeleteGroupOrder 删除团单（明细级联删除）。
func (s *Store) DeleteGroupOrder(ctx context.Context, tx DBTX, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM group_orders WHERE id = ?`, id)
	return err
}

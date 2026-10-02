package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"icewine-erp/internal/model"
)

// recordCols 逐笔记录查询列（带产品与操作人信息）。
const recordCols = `mr.id, mr.market_id, mr.product_id, p.name, p.sku, mr.kind, mr.qty,
	mr.unit_price, mr.discount, mr.unit_cost, mr.channel, mr.barcode, mr.note,
	mr.occurred_at, mr.created_by, COALESCE(NULLIF(u.full_name, ''), u.username, ''),
	mr.created_at, p.avg_cost`

const recordFrom = ` FROM market_records mr
	JOIN products p ON p.id = mr.product_id
	LEFT JOIN users u ON u.id = mr.created_by`

func scanRecord(row interface{ Scan(...any) error }) (*model.MarketRecord, error) {
	var r model.MarketRecord
	err := row.Scan(&r.ID, &r.MarketID, &r.ProductID, &r.ProductName, &r.ProductSKU,
		&r.Kind, &r.Qty, &r.UnitPrice, &r.Discount, &r.UnitCost, &r.Channel, &r.Barcode,
		&r.Note, &r.OccurredAt, &r.CreatedBy, &r.CreatedByName, &r.CreatedAt, &r.AvgCost)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// MarketRecords 查询某场市集的全部逐笔记录（新的在前）。
func (s *Store) MarketRecords(ctx context.Context, marketID int64, limit int) ([]model.MarketRecord, error) {
	query := `SELECT ` + recordCols + recordFrom + ` WHERE mr.market_id = ? ORDER BY mr.id DESC`
	args := []any{marketID}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.MarketRecord
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// MarketRecordsTx 在事务内查询逐笔记录（按时间正序，便于结算过账）。
func (s *Store) MarketRecordsTx(ctx context.Context, tx DBTX, marketID int64) ([]model.MarketRecord, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+recordCols+recordFrom+` WHERE mr.market_id = ? ORDER BY mr.id`, marketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.MarketRecord
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// attachMarketRecords 批量补齐市集的逐笔记录，避免 N+1 查询。
func (s *Store) attachMarketRecords(ctx context.Context, list []model.Market) error {
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
		`SELECT `+recordCols+recordFrom+` WHERE mr.market_id IN (`+placeholders(len(ids))+`)
		 ORDER BY mr.id DESC`, idArgs(ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return err
		}
		if i, ok := index[r.MarketID]; ok {
			list[i].Records = append(list[i].Records, *r)
		}
	}
	return rows.Err()
}

// MarketRecordByID 按 ID 查询一笔记录。
func (s *Store) MarketRecordByID(ctx context.Context, id int64) (*model.MarketRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+recordCols+recordFrom+` WHERE mr.id = ?`, id)
	r, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// InsertMarketRecord 写入一笔现场记录。
func (s *Store) InsertMarketRecord(ctx context.Context, tx DBTX, r *model.MarketRecord) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO market_records(market_id, product_id, kind, qty, unit_price, discount,
		        unit_cost, channel, barcode, note, occurred_at, created_by, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.MarketID, r.ProductID, r.Kind, int64(r.Qty), int64(r.UnitPrice), int64(r.Discount),
		r.UnitCost, r.Channel, r.Barcode, r.Note, r.OccurredAt, r.CreatedBy, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateMarketRecord 修改一笔记录的数量与成交价。
func (s *Store) UpdateMarketRecord(ctx context.Context, tx DBTX, r *model.MarketRecord) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE market_records SET kind = ?, qty = ?, unit_price = ?, discount = ?, note = ?
		 WHERE id = ?`,
		r.Kind, int64(r.Qty), int64(r.UnitPrice), int64(r.Discount), r.Note, r.ID)
	return err
}

// DeleteMarketRecord 删除一笔记录（撤销误操作）。
func (s *Store) DeleteMarketRecord(ctx context.Context, tx DBTX, id int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM market_records WHERE id = ?`, id)
	return err
}

// DeleteMarketRecordsOfProduct 删除某场市集里某个产品的全部记录。
func (s *Store) DeleteMarketRecordsOfProduct(ctx context.Context, tx DBTX, marketID, productID int64) error {
	_, err := tx.ExecContext(ctx,
		`DELETE FROM market_records WHERE market_id = ? AND product_id = ?`, marketID, productID)
	return err
}

// SetMarketRecordCost 把结算成本快照写入某产品在本场的全部记录。
func (s *Store) SetMarketRecordCost(ctx context.Context, tx DBTX, marketID, productID int64, cost model.Money) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE market_records SET unit_cost = ? WHERE market_id = ? AND product_id = ?`,
		int64(cost), marketID, productID)
	return err
}

// ClearMarketRecordCosts 清除本场全部成本快照（撤销结算时）。
func (s *Store) ClearMarketRecordCosts(ctx context.Context, tx DBTX, marketID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE market_records SET unit_cost = NULL WHERE market_id = ?`, marketID)
	return err
}

// RecomputeMarketItemAggregates 依据逐笔记录重算 market_items 上的汇总数量。
//
// 汇总数量只是缓存：界面列表、结算过账读它，事实来源始终是 market_records。
// 每次增删改记录后都要调用一次，保证两边一致。
func (s *Store) RecomputeMarketItemAggregates(ctx context.Context, tx DBTX, marketID int64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE market_items SET sold_qty = 0, tasting_qty = 0, gift_qty = 0, loss_qty = 0
		 WHERE market_id = ?`, marketID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE market_items SET
		  sold_qty    = COALESCE((SELECT SUM(r.qty) FROM market_records r
		                  WHERE r.market_id = market_items.market_id
		                    AND r.product_id = market_items.product_id AND r.kind = 'sale'), 0),
		  tasting_qty = COALESCE((SELECT SUM(r.qty) FROM market_records r
		                  WHERE r.market_id = market_items.market_id
		                    AND r.product_id = market_items.product_id AND r.kind = 'tasting'), 0),
		  gift_qty    = COALESCE((SELECT SUM(r.qty) FROM market_records r
		                  WHERE r.market_id = market_items.market_id
		                    AND r.product_id = market_items.product_id AND r.kind = 'gift'), 0),
		  loss_qty    = COALESCE((SELECT SUM(r.qty) FROM market_records r
		                  WHERE r.market_id = market_items.market_id
		                    AND r.product_id = market_items.product_id AND r.kind = 'loss'), 0)
		WHERE market_id = ?`, marketID)
	return err
}

// RecordKindTotals 某场市集按类型汇总的数量，用于快速校验。
func (s *Store) RecordKindTotals(ctx context.Context, marketID int64) (map[string]model.Qty, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, COALESCE(SUM(qty), 0) FROM market_records
		 WHERE market_id = ? GROUP BY kind`, marketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]model.Qty{}
	for rows.Next() {
		var (
			kind string
			qty  int64
		)
		if err := rows.Scan(&kind, &qty); err != nil {
			return nil, err
		}
		out[kind] = model.Qty(qty)
	}
	return out, rows.Err()
}

// ProductByBarcode 按条码查产品（扫码出库用）。
func (s *Store) ProductByBarcode(ctx context.Context, barcode string) (*model.Product, error) {
	code := strings.TrimSpace(barcode)
	if code == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+productCols+productFrom+` WHERE p.barcode = ? AND p.is_active = 1`, code)
	p, err := scanProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// ClearProductBarcode 清空某产品的条码。
func (s *Store) ClearProductBarcode(ctx context.Context, productID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE products SET barcode = '', updated_at = ? WHERE id = ?`, Now(), productID)
	return err
}

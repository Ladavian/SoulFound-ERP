package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// MarketRecordInput 现场记录一笔的输入。
type MarketRecordInput struct {
	ProductID int64
	Kind      string
	Qty       model.Qty
	UnitPrice model.Money
	Discount  model.Money
	Channel   string // manual / scan
	Barcode   string
	Note      string
}

// Validate 校验类型与数量。
func (in MarketRecordInput) Validate() error {
	if _, ok := model.RecordKindLabels[in.Kind]; !ok {
		return UserErrf("记录类型不正确")
	}
	if in.ProductID <= 0 {
		return UserErrf("请选择产品")
	}
	if in.Qty <= 0 {
		return UserErrf("数量必须大于 0")
	}
	if in.UnitPrice < 0 || in.Discount < 0 {
		return UserErrf("价格与优惠不能为负数")
	}
	if in.Qty > model.MustQty("100000") {
		return UserErrf("单笔数量过大，请检查是否输错")
	}
	return nil
}

// AddMarketRecord 记一笔现场流水：卖出一单、试饮一瓶、赠送或损耗一瓶。
//
// 这是市集模块的主入口。产品若还没上架到本场市集，会自动补一条明细行，
// 避免现场"扫了却记不上"——数量为 0 时收银台会提示，补填带去数量即可。
func (s *Service) AddMarketRecord(ctx context.Context, marketID int64, in MarketRecordInput, user *model.User) (int64, error) {
	if err := in.Validate(); err != nil {
		return 0, err
	}
	m, err := s.Store.MarketByID(ctx, marketID)
	if err != nil {
		return 0, err
	}
	if m == nil {
		return 0, UserErrf("市集不存在")
	}
	if m.IsSettled() {
		return 0, UserErrf("该市集已结算，不能再记账。如需修改请先「撤销结算」")
	}

	product, err := s.Store.ProductByID(ctx, in.ProductID)
	if err != nil {
		return 0, err
	}
	if product == nil {
		return 0, UserErrf("产品不存在")
	}

	// 价格缺省时按明细行的售价，再退回产品售价
	price := in.UnitPrice
	if price == 0 {
		for _, it := range m.Items {
			if it.ProductID == in.ProductID {
				price = it.UnitPrice
				break
			}
		}
	}
	if price == 0 {
		price = product.SalePrice
	}

	var userID *int64
	if user != nil {
		uid := user.ID
		userID = &uid
	}

	// 产品不在本场市集里就先上架
	onMarket := false
	for _, it := range m.Items {
		if it.ProductID == in.ProductID {
			onMarket = true
			break
		}
	}
	if !onMarket {
		if _, err := s.AddMarketProduct(ctx, marketID, in.ProductID, user); err != nil {
			return 0, err
		}
		// 上架后无需再取价格：下面用已解析好的 price 直接写入记录
	}

	channel := in.Channel
	if channel == "" {
		channel = "manual"
	}

	var newID int64
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("该市集已结算，不能再记账")
		}
		id, err := s.Store.InsertMarketRecord(ctx, tx, &model.MarketRecord{
			MarketID:   marketID,
			ProductID:  in.ProductID,
			Kind:       in.Kind,
			Qty:        in.Qty,
			UnitPrice:  price,
			Discount:   in.Discount,
			Channel:    channel,
			Barcode:    strings.TrimSpace(in.Barcode),
			Note:       strings.TrimSpace(in.Note),
			OccurredAt: store.Now(),
			CreatedBy:  userID,
		})
		if err != nil {
			return err
		}
		newID = id
		return s.Store.RecomputeMarketItemAggregates(ctx, tx, marketID)
	})
	if err != nil {
		return 0, err
	}
	return newID, s.RefreshMarketTotals(ctx, marketID)
}

// UpdateMarketRecordRow 修改一笔记录（改数量、成交价、优惠或备注）。
func (s *Service) UpdateMarketRecordRow(ctx context.Context, marketID, recordID int64, up MarketRecordInput, user *model.User) error {
	rec, err := s.Store.MarketRecordByID(ctx, recordID)
	if err != nil {
		return err
	}
	if rec == nil || rec.MarketID != marketID {
		return UserErrf("记录不存在")
	}
	if up.Kind != "" {
		rec.Kind = up.Kind
	}
	if err := (MarketRecordInput{
		ProductID: rec.ProductID, Kind: rec.Kind,
		Qty: up.Qty, UnitPrice: up.UnitPrice, Discount: up.Discount,
	}).Validate(); err != nil {
		return err
	}
	rec.Qty = up.Qty
	rec.UnitPrice = up.UnitPrice
	rec.Discount = up.Discount
	rec.Note = strings.TrimSpace(up.Note)

	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("该市集已结算，不能修改记录")
		}
		if err := s.Store.UpdateMarketRecord(ctx, tx, rec); err != nil {
			return err
		}
		return s.Store.RecomputeMarketItemAggregates(ctx, tx, marketID)
	})
	if err != nil {
		return err
	}
	return s.RefreshMarketTotals(ctx, marketID)
}

// DeleteMarketRecordRow 撤销一笔记录（记错了就删掉）。
func (s *Service) DeleteMarketRecordRow(ctx context.Context, marketID, recordID int64, user *model.User) error {
	rec, err := s.Store.MarketRecordByID(ctx, recordID)
	if err != nil {
		return err
	}
	if rec == nil || rec.MarketID != marketID {
		return UserErrf("记录不存在")
	}
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("该市集已结算，不能撤销记录")
		}
		if err := s.Store.DeleteMarketRecord(ctx, tx, recordID); err != nil {
			return err
		}
		return s.Store.RecomputeMarketItemAggregates(ctx, tx, marketID)
	})
	if err != nil {
		return err
	}
	return s.RefreshMarketTotals(ctx, marketID)
}

// ScanResult 扫码结果。
type ScanResult struct {
	Product *model.Product
	Code    string
}

// LookupBarcode 按条码找产品。
func (s *Service) LookupBarcode(ctx context.Context, code string) (*ScanResult, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, UserErrf("没有读到条码内容")
	}
	p, err := s.Store.ProductByBarcode(ctx, code)
	if err != nil {
		return nil, err
	}
	return &ScanResult{Product: p, Code: code}, nil
}

// BindProductBarcode 把条码绑定到产品。
//
// 现场扫到没录过的瓶子时，确认是哪个产品后直接绑定，
// 下次再扫就能自动识别。已绑到别的产品时会报错，避免互相覆盖。
func (s *Service) BindProductBarcode(ctx context.Context, productID int64, barcode string, user *model.User) error {
	code := strings.TrimSpace(barcode)
	if code == "" {
		return UserErrf("条码不能为空")
	}
	if len(code) > 64 {
		return UserErrf("条码过长")
	}
	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return err
	}
	if product == nil {
		return UserErrf("产品不存在")
	}
	owner, err := s.Store.ProductByBarcode(ctx, code)
	if err != nil {
		return err
	}
	if owner != nil && owner.ID != productID {
		return UserErrf("该条码已经绑给了「%s」，请先到产品档案里解除", owner.Name)
	}
	updated := *product
	updated.Barcode = code
	return s.Store.UpdateProduct(ctx, &updated)
}

// MarketRecordSummary 收银台顶部与按产品的小结。
type MarketRecordSummary struct {
	Orders     int                       // 销售笔数
	SoldQty    model.Qty                 // 售出瓶数
	TastingQty model.Qty                 // 试饮瓶数
	GiftQty    model.Qty                 // 赠送瓶数
	LossQty    model.Qty                 // 损耗瓶数
	Revenue    model.Money               // 销售额
	ByProduct  []MarketRecordSummaryLine // 按产品汇总
}

// MarketRecordSummaryLine 按产品汇总的一行。
type MarketRecordSummaryLine struct {
	ProductID   int64
	ProductName string
	SoldQty     model.Qty
	TastingQty  model.Qty
	GiftQty     model.Qty
	LossQty     model.Qty
	Orders      int
	Revenue     model.Money
}

// SummarizeMarketRecords 汇总现场流水。
func SummarizeMarketRecords(records []model.MarketRecord) MarketRecordSummary {
	var out MarketRecordSummary
	index := map[int64]int{}
	for _, r := range records {
		i, ok := index[r.ProductID]
		if !ok {
			out.ByProduct = append(out.ByProduct, MarketRecordSummaryLine{
				ProductID: r.ProductID, ProductName: r.ProductName,
			})
			i = len(out.ByProduct) - 1
			index[r.ProductID] = i
		}
		line := &out.ByProduct[i]
		switch r.Kind {
		case model.RecordSale:
			out.Orders++
			out.SoldQty += r.Qty
			out.Revenue += r.Amount()
			line.Orders++
			line.SoldQty += r.Qty
			line.Revenue += r.Amount()
		case model.RecordTasting:
			out.TastingQty += r.Qty
			line.TastingQty += r.Qty
		case model.RecordGift:
			out.GiftQty += r.Qty
			line.GiftQty += r.Qty
		case model.RecordLoss:
			out.LossQty += r.Qty
			line.LossQty += r.Qty
		}
	}
	return out
}

// OutQtyOf 某产品在本场已出库的数量（销售 + 试饮 + 赠送 + 损耗）。
func OutQtyOf(records []model.MarketRecord, productID int64) model.Qty {
	var q model.Qty
	for _, r := range records {
		if r.ProductID == productID {
			q += r.Qty
		}
	}
	return q
}

// StockWarning 判断某产品是否已经超出带去数量，用于收银台提示。
func StockWarning(item model.MarketItem, records []model.MarketRecord) string {
	out := OutQtyOf(records, item.ProductID)
	if item.CarriedQty <= 0 {
		if out > 0 {
			return fmt.Sprintf("尚未填写带去数量，已记 %s", out.String())
		}
		return ""
	}
	if out > item.CarriedQty {
		return fmt.Sprintf("已超出带去数量 %s", (out - item.CarriedQty).String())
	}
	return ""
}

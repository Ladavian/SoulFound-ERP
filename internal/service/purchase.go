package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// PurchaseItemInput 采购明细输入。
type PurchaseItemInput struct {
	ProductID int64
	Qty       model.Qty
	UnitPrice model.Money
	Note      string
}

// PurchaseInput 采购单输入。
type PurchaseInput struct {
	ID           int64
	SupplierID   *int64
	PurchaseDate string
	Currency     string
	AllocMethod  string
	Notes        string
	ShippingCost model.Money
	TariffCost   model.Money
	OtherCost    model.Money
	Items        []PurchaseItemInput
}

// buildPurchase 校验输入并计算附加费用分摊与到岸成本。
func (s *Service) buildPurchase(in PurchaseInput) (*model.Purchase, error) {
	p := &model.Purchase{
		ID:           in.ID,
		SupplierID:   in.SupplierID,
		PurchaseDate: strings.TrimSpace(in.PurchaseDate),
		Currency:     strings.TrimSpace(in.Currency),
		AllocMethod:  in.AllocMethod,
		Notes:        strings.TrimSpace(in.Notes),
		ShippingCost: in.ShippingCost,
		TariffCost:   in.TariffCost,
		OtherCost:    in.OtherCost,
	}
	if p.PurchaseDate == "" {
		p.PurchaseDate = store.Today()
	}
	if _, err := time.Parse("2006-01-02", p.PurchaseDate); err != nil {
		return nil, UserErrf("采购日期格式不正确，应为 2025-10-02 这样的格式")
	}
	if p.Currency == "" {
		p.Currency = s.Cfg.Currency
	}
	if p.AllocMethod != model.AllocByQty {
		p.AllocMethod = model.AllocByAmount
	}
	if p.ShippingCost < 0 || p.TariffCost < 0 || p.OtherCost < 0 {
		return nil, UserErrf("运费、关税与其他费用不能为负数")
	}

	items := make([]model.PurchaseItem, 0, len(in.Items))
	for _, raw := range in.Items {
		if raw.ProductID <= 0 || raw.Qty == 0 {
			continue
		}
		if raw.Qty < 0 {
			return nil, UserErrf("入库数量必须大于 0")
		}
		if raw.UnitPrice < 0 {
			return nil, UserErrf("采购单价不能为负数")
		}
		items = append(items, model.PurchaseItem{
			ProductID: raw.ProductID,
			Qty:       raw.Qty,
			UnitPrice: raw.UnitPrice,
			Amount:    model.MulQty(raw.Qty, raw.UnitPrice),
			Note:      strings.TrimSpace(raw.Note),
		})
	}
	if len(items) == 0 {
		return nil, UserErrf("请至少添加一行有效的采购明细（选择产品并填写数量）")
	}
	for _, it := range items {
		p.GoodsAmount += it.Amount
	}
	allocateExtra(items, p.ExtraCost(), p.AllocMethod)
	p.Items = items
	p.TotalCost = p.GoodsAmount + p.ExtraCost()
	return p, nil
}

// allocateExtra 把附加费用分摊到各明细行，并计算到岸成本单价。
//
// 采用「最大余额法」的简化版：前 n-1 行按比例取整，最后一行取剩余金额，
// 保证分摊合计恰好等于费用总额，不会出现分位差额。
func allocateExtra(items []model.PurchaseItem, extra model.Money, method string) {
	if len(items) == 0 {
		return
	}
	var (
		totalAmount model.Money
		totalQty    model.Qty
	)
	for _, it := range items {
		totalAmount += it.Amount
		totalQty += it.Qty
	}
	useQty := method == model.AllocByQty || totalAmount == 0

	var allocated model.Money
	for i := range items {
		switch {
		case extra == 0:
			items[i].ExtraAlloc = 0
		case i == len(items)-1:
			items[i].ExtraAlloc = extra - allocated
		case useQty && totalQty > 0:
			items[i].ExtraAlloc = model.DivByQty(model.MulQty(items[i].Qty, extra), totalQty)
			allocated += items[i].ExtraAlloc
		case !useQty && totalAmount > 0:
			items[i].ExtraAlloc = extra.MulRatio(float64(items[i].Amount) / float64(totalAmount))
			allocated += items[i].ExtraAlloc
		default:
			items[i].ExtraAlloc = 0
		}
	}
	for i := range items {
		if items[i].Qty != 0 {
			items[i].LandedUnitCost = model.DivByQty(items[i].Amount+items[i].ExtraAlloc, items[i].Qty)
		}
	}
}

// AllocatePreview 供表单实时预览分摊结果。
func AllocatePreview(items []model.PurchaseItem, extra model.Money, method string) []model.PurchaseItem {
	out := make([]model.PurchaseItem, len(items))
	copy(out, items)
	allocateExtra(out, extra, method)
	return out
}

// SavePurchase 新建或修改采购单（仅草稿可改）。
func (s *Service) SavePurchase(ctx context.Context, in PurchaseInput, user *model.User) (int64, error) {
	p, err := s.buildPurchase(in)
	if err != nil {
		return 0, err
	}
	if user != nil {
		id := user.ID
		p.CreatedBy = &id
	}
	var newID int64
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if p.ID > 0 {
			cur, err := s.Store.PurchaseStatusTx(ctx, tx, p.ID)
			if errors.Is(err, store.ErrNotFound) {
				return UserErrf("采购单不存在")
			}
			if err != nil {
				return err
			}
			if cur != model.PurchaseDraft {
				return UserErrf("只有草稿状态的采购单可以修改；已入库请先「撤销入库」")
			}
			if err := s.Store.UpdatePurchase(ctx, tx, p); err != nil {
				return err
			}
			if err := s.Store.ReplacePurchaseItems(ctx, tx, p.ID, p.Items); err != nil {
				return err
			}
			newID = p.ID
		} else {
			code, err := s.Store.NextCode(ctx, tx, "PO", "PO", time.Now())
			if err != nil {
				return err
			}
			p.Code = code
			p.Status = model.PurchaseDraft
			id, err := s.Store.CreatePurchase(ctx, tx, p)
			if err != nil {
				return err
			}
			if err := s.Store.ReplacePurchaseItems(ctx, tx, id, p.Items); err != nil {
				return err
			}
			newID = id
		}
		return s.Store.Log(ctx, tx, user, "保存采购单", "purchase", &newID, p.Code)
	})
	return newID, err
}

// ConfirmPurchase 确认入库：按到岸成本写入库存，重算移动加权平均成本。
func (s *Service) ConfirmPurchase(ctx context.Context, id int64, user *model.User) error {
	p, err := s.Store.PurchaseByID(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return UserErrf("采购单不存在")
	}
	if p.IsConfirmed() {
		return UserErrf("该采购单已经入库了")
	}
	if p.Status == model.PurchaseVoid {
		return UserErrf("已作废的采购单不能入库")
	}
	if len(p.Items) == 0 {
		return UserErrf("采购单没有明细，无法入库")
	}

	items := make([]model.PurchaseItem, len(p.Items))
	copy(items, p.Items)
	allocateExtra(items, p.ExtraCost(), p.AllocMethod)

	cfg, err := s.Store.Settings(ctx)
	if err != nil {
		return err
	}
	var userID *int64
	if user != nil {
		uid := user.ID
		userID = &uid
	}

	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.PurchaseStatusTx(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("采购单不存在")
		}
		if err != nil {
			return err
		}
		if st != model.PurchaseDraft {
			return UserErrf("该采购单状态已变化，请刷新页面后重试")
		}
		if err := s.Store.ReplacePurchaseItems(ctx, tx, id, items); err != nil {
			return err
		}
		poster := s.NewPoster(ctx, tx, cfg.AllowNegative)
		note := "采购入库"
		if p.SupplierName != "" {
			note = p.SupplierName + " 采购入库"
		}
		for _, it := range items {
			if _, err := poster.Post(PostRequest{
				ProductID:  it.ProductID,
				Qty:        it.Qty,
				UnitCost:   it.LandedUnitCost,
				OccurredOn: p.PurchaseDate,
				Reason:     model.ReasonPurchase,
				RefType:    "purchase",
				RefID:      &id,
				RefCode:    p.Code,
				Note:       note,
				UserID:     userID,
			}); err != nil {
				return err
			}
		}
		if err := s.Store.SetPurchaseStatus(ctx, tx, id, model.PurchaseConfirmed, store.Now()); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "采购入库", "purchase", &id, p.Code)
	})
}

// UnconfirmPurchase 撤销入库：删除该单产生的流水并重算受影响产品。
func (s *Service) UnconfirmPurchase(ctx context.Context, id int64, user *model.User) error {
	p, err := s.Store.PurchaseByID(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return UserErrf("采购单不存在")
	}
	if !p.IsConfirmed() {
		return UserErrf("该采购单尚未入库，无需撤销")
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.PurchaseStatusTx(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("采购单不存在")
		}
		if err != nil {
			return err
		}
		if st != model.PurchaseConfirmed {
			return UserErrf("该采购单状态已变化，请刷新页面后重试")
		}
		moves, err := s.Store.MovementsByRef(ctx, tx, "purchase", id)
		if err != nil {
			return err
		}
		affected := make(map[int64]bool, len(moves))
		for _, m := range moves {
			affected[m.ProductID] = true
		}
		if err := s.Store.DeleteMovementsByRef(ctx, tx, "purchase", id); err != nil {
			return err
		}
		if err := s.RebuildAffected(ctx, tx, affected); err != nil {
			return err
		}
		if err := s.Store.SetPurchaseStatus(ctx, tx, id, model.PurchaseDraft, ""); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "撤销入库", "purchase", &id, p.Code)
	})
}

// VoidPurchase 作废采购单（仅草稿）。
func (s *Service) VoidPurchase(ctx context.Context, id int64, user *model.User) error {
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.PurchaseStatusTx(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("采购单不存在")
		}
		if err != nil {
			return err
		}
		if st != model.PurchaseDraft {
			return UserErrf("只有草稿状态的采购单可以作废")
		}
		if err := s.Store.SetPurchaseStatus(ctx, tx, id, model.PurchaseVoid, ""); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "作废采购单", "purchase", &id, "")
	})
}

// DeletePurchase 删除采购单（仅草稿）。
func (s *Service) DeletePurchase(ctx context.Context, id int64, user *model.User) error {
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.PurchaseStatusTx(ctx, tx, id)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("采购单不存在")
		}
		if err != nil {
			return err
		}
		if st != model.PurchaseDraft {
			return UserErrf("已入库的采购单不能删除，请先「撤销入库」")
		}
		if err := s.Store.DeletePurchase(ctx, tx, id); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "删除采购单", "purchase", &id, "")
	})
}

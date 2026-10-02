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

// MarketInput 市集基本信息输入。
type MarketInput struct {
	ID        int64
	Name      string
	Venue     string
	City      string
	Organizer string
	StartDate string
	EndDate   string
	Notes     string
}

// ComputeMarketTotals 依据明细与费用计算一场市集的损益。
//
// 计算口径（与界面上展示的公式完全一致）：
//
//	销售额   = Σ(销售数量 × 单价 - 优惠)
//	售出成本 = Σ(销售数量 × 单位成本)
//	试饮成本 = Σ(试饮数量 × 单位成本)
//	赠送损耗 = Σ((赠送 + 损耗) × 单位成本)
//	净利润   = 销售额 - 售出成本 - 试饮成本 - 赠送损耗 - 活动费用
//
// 已结算的市集使用结算时的成本快照，未结算的按产品当前平均成本估算。
func ComputeMarketTotals(items []model.MarketItem, expenses []model.MarketExpense) model.MarketTotals {
	var t model.MarketTotals
	for _, it := range items {
		t.Revenue += it.Revenue()
		t.CogsSold += it.Cogs()
		t.TastingCost += it.TastingCost()
		t.LossCost += it.LossCost()
	}
	for _, e := range expenses {
		t.ExpenseCost += e.Amount
	}
	t.NetProfit = t.Revenue - t.CogsSold - t.TastingCost - t.LossCost - t.ExpenseCost
	return t
}

// liveItems 复制明细并清空成本快照（用于估算态计算）。
func liveItems(items []model.MarketItem) []model.MarketItem {
	out := make([]model.MarketItem, len(items))
	copy(out, items)
	for i := range out {
		out[i].UnitCost = nil
	}
	return out
}

// SaveMarket 新建或修改市集。
func (s *Service) SaveMarket(ctx context.Context, in MarketInput, user *model.User) (int64, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return 0, UserErrf("请填写市集名称，例如「多伦多圣诞市集」")
	}
	start := strings.TrimSpace(in.StartDate)
	end := strings.TrimSpace(in.EndDate)
	if start == "" {
		start = store.Today()
	}
	if end == "" {
		end = start
	}
	startDate, err := time.Parse("2006-01-02", start)
	if err != nil {
		return 0, UserErrf("开始日期格式不正确")
	}
	endDate, err := time.Parse("2006-01-02", end)
	if err != nil {
		return 0, UserErrf("结束日期格式不正确")
	}
	if endDate.Before(startDate) {
		return 0, UserErrf("结束日期不能早于开始日期")
	}

	m := &model.Market{
		ID:        in.ID,
		Name:      name,
		Venue:     strings.TrimSpace(in.Venue),
		City:      strings.TrimSpace(in.City),
		Organizer: strings.TrimSpace(in.Organizer),
		StartDate: start,
		EndDate:   end,
		Notes:     strings.TrimSpace(in.Notes),
	}
	if user != nil {
		id := user.ID
		m.CreatedBy = &id
	}

	var newID int64
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if m.ID > 0 {
			st, err := s.Store.MarketStatusTx(ctx, tx, m.ID)
			if errors.Is(err, store.ErrNotFound) {
				return UserErrf("市集不存在")
			}
			if err != nil {
				return err
			}
			if st == model.MarketSettled {
				return UserErrf("已结算的市集不能修改，请先「撤销结算」")
			}
			m.Status = st
			if err := s.Store.UpdateMarket(ctx, tx, m); err != nil {
				return err
			}
			newID = m.ID
		} else {
			code, err := s.Store.NextCode(ctx, tx, "MK", "MK", time.Now())
			if err != nil {
				return err
			}
			m.Code = code
			m.Status = model.MarketPlanned
			id, err := s.Store.CreateMarket(ctx, tx, m)
			if err != nil {
				return err
			}
			newID = id
		}
		return s.Store.Log(ctx, tx, user, "保存市集", "market", &newID, m.Name)
	})
	return newID, err
}

// SetMarketStatus 切换计划中 / 进行中。
func (s *Service) SetMarketStatus(ctx context.Context, marketID int64, status string, user *model.User) error {
	if status != model.MarketPlanned && status != model.MarketOngoing {
		return UserErrf("不支持的状态")
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("已结算的市集不能改状态，请先「撤销结算」")
		}
		return s.Store.SetMarketStatus(ctx, tx, marketID, status)
	})
}

// AddMarketProduct 往市集里添加一个产品（单价默认取当前售价）。
func (s *Service) AddMarketProduct(ctx context.Context, marketID, productID int64, user *model.User) (int64, error) {
	if productID <= 0 {
		return 0, UserErrf("请选择要添加的产品")
	}
	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return 0, err
	}
	if product == nil {
		return 0, UserErrf("产品不存在")
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
			return UserErrf("已结算的市集不能修改明细，请先「撤销结算」")
		}
		sortOrder, err := s.Store.MaxMarketItemSort(ctx, tx, marketID)
		if err != nil {
			return err
		}
		id, err := s.Store.AddMarketItem(ctx, tx, &model.MarketItem{
			MarketID:  marketID,
			ProductID: productID,
			UnitPrice: product.SalePrice,
			SortOrder: sortOrder + 1,
		})
		if err != nil {
			return err
		}
		newID = id
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newID, s.RefreshMarketTotals(ctx, marketID)
}

// MarketItemUpdate 单行明细的修改内容。
type MarketItemUpdate struct {
	CarriedQty  model.Qty
	TastingQty  model.Qty
	SoldQty     model.Qty
	GiftQty     model.Qty
	LossQty     model.Qty
	UnitPrice   model.Money
	DiscountAmt model.Money
	Note        string
}

// Validate 校验数量与金额。
func (u MarketItemUpdate) Validate() error {
	if u.CarriedQty < 0 || u.TastingQty < 0 || u.SoldQty < 0 || u.GiftQty < 0 || u.LossQty < 0 {
		return UserErrf("数量不能为负数")
	}
	if u.UnitPrice < 0 {
		return UserErrf("售价不能为负数")
	}
	if u.DiscountAmt < 0 {
		return UserErrf("优惠金额不能为负数")
	}
	if u.DiscountAmt > model.MulQty(u.SoldQty, u.UnitPrice) {
		return UserErrf("优惠金额不能超过该行销售额")
	}
	if u.CarriedQty > 0 && u.OutQty() > u.CarriedQty {
		return UserErrf("试饮 + 销售 + 赠送 + 损耗（%s）超过了带去的数量（%s）", u.OutQty(), u.CarriedQty)
	}
	return nil
}

// OutQty 总出库量。
func (u MarketItemUpdate) OutQty() model.Qty {
	return u.TastingQty + u.SoldQty + u.GiftQty + u.LossQty
}

// UpdateMarketItemRow 更新一行市集明细。
func (s *Service) UpdateMarketItemRow(ctx context.Context, marketID, itemID int64, up MarketItemUpdate, user *model.User) error {
	if err := up.Validate(); err != nil {
		return err
	}
	item, err := s.Store.MarketItemByID(ctx, itemID)
	if err != nil {
		return err
	}
	if item == nil || item.MarketID != marketID {
		return UserErrf("明细行不存在")
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
			return UserErrf("已结算的市集不能修改，请先「撤销结算」")
		}
		return s.Store.UpdateMarketItem(ctx, tx, &model.MarketItem{
			ID:          itemID,
			CarriedQty:  up.CarriedQty,
			TastingQty:  up.TastingQty,
			SoldQty:     up.SoldQty,
			GiftQty:     up.GiftQty,
			LossQty:     up.LossQty,
			UnitPrice:   up.UnitPrice,
			DiscountAmt: up.DiscountAmt,
			Note:        strings.TrimSpace(up.Note),
		})
	})
	if err != nil {
		return err
	}
	return s.RefreshMarketTotals(ctx, marketID)
}

// DeleteMarketItemRow 删除一行市集明细。
func (s *Service) DeleteMarketItemRow(ctx context.Context, marketID, itemID int64, user *model.User) error {
	err := s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("已结算的市集不能修改，请先「撤销结算」")
		}
		return s.Store.DeleteMarketItem(ctx, tx, itemID)
	})
	if err != nil {
		return err
	}
	return s.RefreshMarketTotals(ctx, marketID)
}

// SaveMarketExpenses 覆盖保存市集费用。
func (s *Service) SaveMarketExpenses(ctx context.Context, marketID int64, expenses []model.MarketExpense, user *model.User) error {
	clean := make([]model.MarketExpense, 0, len(expenses))
	for _, e := range expenses {
		if e.Amount == 0 && strings.TrimSpace(e.Note) == "" {
			continue
		}
		if e.Amount < 0 {
			return UserErrf("费用金额不能为负数")
		}
		if _, ok := model.ExpenseCategoryLabels[e.Category]; !ok {
			e.Category = model.ExpenseOther
		}
		e.Note = strings.TrimSpace(e.Note)
		clean = append(clean, model.MarketExpense{Category: e.Category, Amount: e.Amount, Note: e.Note})
	}
	err := s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("已结算的市集不能修改费用，请先「撤销结算」")
		}
		return s.Store.ReplaceMarketExpenses(ctx, tx, marketID, clean)
	})
	if err != nil {
		return err
	}
	return s.RefreshMarketTotals(ctx, marketID)
}

// RefreshMarketTotals 重新计算并缓存市集损益（列表排序与快照用）。
func (s *Service) RefreshMarketTotals(ctx context.Context, marketID int64) error {
	m, err := s.Store.MarketByID(ctx, marketID)
	if err != nil || m == nil {
		return err
	}
	totals := ComputeMarketTotals(m.Items, m.Expenses)
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		return s.Store.SaveMarketTotals(ctx, tx, marketID, totals)
	})
}

// SettleMarket 结算市集：扣减库存、锁定成本快照、固化损益。
func (s *Service) SettleMarket(ctx context.Context, marketID int64, user *model.User) error {
	m, err := s.Store.MarketByID(ctx, marketID)
	if err != nil {
		return err
	}
	if m == nil {
		return UserErrf("市集不存在")
	}
	if m.IsSettled() {
		return UserErrf("该市集已经结算过了")
	}
	if len(m.Items) == 0 {
		return UserErrf("请先添加产品并填写试饮/销售数量，然后再结算")
	}
	active := false
	for _, it := range m.Items {
		if it.OutQty() != 0 {
			active = true
			break
		}
	}
	if !active {
		return UserErrf("所有产品的数量都是 0，没有需要扣减的库存")
	}

	cfg, err := s.Store.Settings(ctx)
	if err != nil {
		return err
	}
	var userID *int64
	if user != nil {
		uid := user.ID
		userID = &uid
	}

	occurred := m.EndDate
	if occurred == "" {
		occurred = m.StartDate
	}

	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("该市集已经结算过了，请刷新页面")
		}

		poster := s.NewPoster(ctx, tx, cfg.AllowNegative)
		ref := marketID
		items := make([]model.MarketItem, len(m.Items))
		copy(items, m.Items)

		for i := range items {
			it := &items[i]
			cost, err := poster.CostOf(it.ProductID)
			if err != nil {
				return err
			}
			it.UnitCost = &cost

			base := PostRequest{
				ProductID:  it.ProductID,
				OccurredOn: occurred,
				RefType:    "market",
				RefID:      &ref,
				RefCode:    m.Code,
				Note:       m.Name,
				UserID:     userID,
			}
			type line struct {
				qty    model.Qty
				reason string
			}
			lines := []line{
				{it.SoldQty, model.ReasonMarketSale},
				{it.TastingQty, model.ReasonMarketTasting},
				{it.GiftQty, model.ReasonMarketGift},
				{it.LossQty, model.ReasonMarketLoss},
			}
			for _, ln := range lines {
				if ln.qty == 0 {
					continue
				}
				req := base
				req.Qty = -ln.qty
				req.Reason = ln.reason
				if _, err := poster.Post(req); err != nil {
					return err
				}
			}
			if err := s.Store.SetMarketItemCost(ctx, tx, it.ID, cost); err != nil {
				return err
			}
		}

		totals := ComputeMarketTotals(items, m.Expenses)
		if err := s.Store.MarkMarketSettled(ctx, tx, marketID, store.Now(), userID, totals); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "市集结算", "market", &marketID,
			m.Name+" 净利润 "+totals.NetProfit.String())
	})
}

// UnsettleMarket 撤销结算：回滚库存并恢复为可编辑状态。
func (s *Service) UnsettleMarket(ctx context.Context, marketID int64, user *model.User) error {
	m, err := s.Store.MarketByID(ctx, marketID)
	if err != nil {
		return err
	}
	if m == nil {
		return UserErrf("市集不存在")
	}
	if !m.IsSettled() {
		return UserErrf("该市集尚未结算，无需撤销")
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st != model.MarketSettled {
			return UserErrf("该市集状态已变化，请刷新页面后重试")
		}
		moves, err := s.Store.MovementsByRef(ctx, tx, "market", marketID)
		if err != nil {
			return err
		}
		affected := make(map[int64]bool, len(moves))
		for _, mv := range moves {
			affected[mv.ProductID] = true
		}
		if err := s.Store.DeleteMovementsByRef(ctx, tx, "market", marketID); err != nil {
			return err
		}
		if err := s.Store.ClearMarketItemCosts(ctx, tx, marketID); err != nil {
			return err
		}
		if err := s.RebuildAffected(ctx, tx, affected); err != nil {
			return err
		}
		totals := ComputeMarketTotals(liveItems(m.Items), m.Expenses)
		if err := s.Store.MarkMarketUnsettled(ctx, tx, marketID, totals); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "撤销市集结算", "market", &marketID, m.Name)
	})
}

// DeleteMarket 删除市集（已结算需先撤销）。
func (s *Service) DeleteMarket(ctx context.Context, marketID int64, user *model.User) error {
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		st, err := s.Store.MarketStatusTx(ctx, tx, marketID)
		if errors.Is(err, store.ErrNotFound) {
			return UserErrf("市集不存在")
		}
		if err != nil {
			return err
		}
		if st == model.MarketSettled {
			return UserErrf("已结算的市集不能删除，请先「撤销结算」")
		}
		if err := s.Store.DeleteMarket(ctx, tx, marketID); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "删除市集", "market", &marketID, "")
	})
}

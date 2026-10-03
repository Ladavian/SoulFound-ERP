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

// ComputeMarketTotals 依据明细、费用与逐笔记录计算损益。
//
// 计算口径（与界面展示的公式一致）：
//
//	销售额   = Σ(每笔销售的数量 × 成交价 − 该笔优惠)
//	售出成本 = Σ(每笔销售的数量 × 单位成本)
//	试饮成本 = Σ(每笔试饮的数量 × 单位成本)
//	赠送损耗 = Σ(每笔赠送/损耗的数量 × 单位成本)
//	净利润   = 销售额 − 售出成本 − 试饮成本 − 赠送损耗 − 活动费用
//
// 已结算的市集使用结算时的成本快照，未结算的按产品当前平均成本估算；
// 没有任何逐笔记录时（历史数据）退回按汇总数量计算。
func ComputeMarketTotals(items []model.MarketItem, expenses []model.MarketExpense, records []model.MarketRecord) model.MarketTotals {
	return model.ComputeTotals(items, expenses, records)
}

// liveRecords 复制逐笔记录并清空成本快照（用于估算态计算）。
func liveRecords(records []model.MarketRecord) []model.MarketRecord {
	out := make([]model.MarketRecord, len(records))
	copy(out, records)
	for i := range out {
		out[i].UnitCost = nil
	}
	return out
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

// CopyLastMarketItems 把最近一场有明细的市集的产品清单复制到本场。
//
// 市集往往卖的还是那几款，逐场重新勾选很繁琐。
// 复制的是"带了哪些产品、计划带多少、卖多少钱"，不含任何销售记录。
func (s *Service) CopyLastMarketItems(ctx context.Context, marketID int64, user *model.User) (int, error) {
	m, err := s.Store.MarketByID(ctx, marketID)
	if err != nil {
		return 0, err
	}
	if m == nil {
		return 0, UserErrf("市集不存在")
	}
	if m.IsSettled() {
		return 0, UserErrf("已结算的市集不能修改明细")
	}

	markets, err := s.Store.ListMarkets(ctx, store.MarketFilter{Limit: 30})
	if err != nil {
		return 0, err
	}
	var src *model.Market
	for i := range markets {
		if markets[i].ID == marketID || len(markets[i].Items) == 0 {
			continue
		}
		src = &markets[i]
		break
	}
	if src == nil {
		return 0, UserErrf("没有找到可沿用的历史市集，先手动添加一次产品即可")
	}

	have := map[int64]bool{}
	for _, it := range m.Items {
		have[it.ProductID] = true
	}
	copied := 0
	for _, it := range src.Items {
		if have[it.ProductID] {
			continue
		}
		p, err := s.Store.ProductByID(ctx, it.ProductID)
		if err != nil {
			return copied, err
		}
		if p == nil || !p.IsActive {
			continue // 已停用或已删除的产品不再带过来
		}
		if _, err := s.AddMarketProduct(ctx, marketID, it.ProductID, user); err != nil {
			return copied, err
		}
		copied++
		have[it.ProductID] = true
	}
	if copied == 0 {
		return 0, UserErrf("「%s」里的产品都已经在本场了", src.Name)
	}
	return copied, nil
}

// AddAllActiveProducts 把全部在售产品加入本场，适合产品不多的情况。
func (s *Service) AddAllActiveProducts(ctx context.Context, marketID int64, user *model.User) (int, error) {
	m, err := s.Store.MarketByID(ctx, marketID)
	if err != nil {
		return 0, err
	}
	if m == nil {
		return 0, UserErrf("市集不存在")
	}
	if m.IsSettled() {
		return 0, UserErrf("已结算的市集不能修改明细")
	}
	all, err := s.Store.ListProducts(ctx, store.ProductFilter{})
	if err != nil {
		return 0, err
	}
	have := map[int64]bool{}
	for _, it := range m.Items {
		have[it.ProductID] = true
	}
	added := 0
	for _, p := range all {
		if !p.IsActive || have[p.ID] {
			continue
		}
		if _, err := s.AddMarketProduct(ctx, marketID, p.ID, user); err != nil {
			return added, err
		}
		added++
	}
	if added == 0 {
		return 0, UserErrf("全部在售产品都已经在本场了")
	}
	return added, nil
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
		if err := s.Store.UpdateMarketItem(ctx, tx, &model.MarketItem{
			ID:          itemID,
			CarriedQty:  up.CarriedQty,
			TastingQty:  up.TastingQty,
			SoldQty:     up.SoldQty,
			GiftQty:     up.GiftQty,
			LossQty:     up.LossQty,
			UnitPrice:   up.UnitPrice,
			DiscountAmt: up.DiscountAmt,
			Note:        strings.TrimSpace(up.Note),
		}); err != nil {
			return err
		}
		// 汇总数量由逐笔记录推导，直接改汇总会与记录打架。
		// 因此这里把"填写值 − 记录合计"的差额补成一条调整记录，
		// 既保留了快速补录整场数量的能力，又保证两边永远一致。
		return s.reconcileItemQuantities(ctx, tx, marketID, item.ProductID, up)
	})
	if err != nil {
		return err
	}
	return s.RefreshMarketTotals(ctx, marketID)
}

// reconcileItemQuantities 让明细行的汇总数量与逐笔记录保持一致。
//
// 设计：现场扫码/点按钮产生的记录是"事实"，明细行上填的数字是"目标"。
// 目标与事实的差额，每个类型（销售/试饮/赠送/损耗）各落成一条
// 来源为 summary 的调整记录，收银台流水里因此始终能看到完整账目，
// 后台也仍然可以一次性补录整场数量。
//
// 填写的目标小于现场已记录数量时直接报错，避免写出负数记录——
// 那通常意味着填错了。
func (s *Service) reconcileItemQuantities(ctx context.Context, tx *sql.Tx, marketID, productID int64, up MarketItemUpdate) error {
	records, err := s.Store.MarketRecordsTx(ctx, tx, marketID)
	if err != nil {
		return err
	}

	// 现场记录（非汇总调整）的合计，以及每个类型最新的那条汇总记录
	sumQty := map[string]model.Qty{}
	summaries := map[string][]model.MarketRecord{}
	sumDiscount := model.Money(0)
	for _, r := range records {
		if r.ProductID != productID {
			continue
		}
		if r.Channel == "summary" {
			summaries[r.Kind] = append(summaries[r.Kind], r)
			continue
		}
		sumQty[r.Kind] += r.Qty
		if r.Kind == model.RecordSale {
			sumDiscount += r.Discount
		}
	}

	targets := []struct {
		kind   string
		target model.Qty
		price  model.Money
		disc   model.Money
	}{
		{model.RecordSale, up.SoldQty, up.UnitPrice, up.DiscountAmt - sumDiscount},
		{model.RecordTasting, up.TastingQty, 0, 0},
		{model.RecordGift, up.GiftQty, 0, 0},
		{model.RecordLoss, up.LossQty, 0, 0},
	}

	for _, t := range targets {
		label := model.RecordKindLabel(t.kind)
		gap := t.target - sumQty[t.kind]
		if gap < 0 {
			return UserErrf("「%s」现场已经记录了 %s，这里填的数量不能小于它；要减少请到收银台撤销对应记录",
				label, sumQty[t.kind])
		}

		list := summaries[t.kind]
		var keep *model.MarketRecord
		for i := range list {
			if keep == nil || list[i].ID > keep.ID {
				keep = &list[i]
			}
		}

		// 销售还需要承载优惠差额，所以即使数量没变也要留一条
		needRecord := gap > 0 || (t.kind == model.RecordSale && t.disc != 0)
		if !needRecord {
			for i := range list {
				if err := s.Store.DeleteMarketRecord(ctx, tx, list[i].ID); err != nil {
					return err
				}
			}
			continue
		}

		adjust := &model.MarketRecord{
			MarketID:   marketID,
			ProductID:  productID,
			Kind:       t.kind,
			Qty:        gap,
			UnitPrice:  t.price,
			Discount:   t.disc,
			Channel:    "summary",
			Note:       "汇总补录调整",
			OccurredAt: store.Now(),
		}
		if keep != nil {
			adjust.ID = keep.ID
			if err := s.Store.UpdateMarketRecord(ctx, tx, adjust); err != nil {
				return err
			}
			for i := range list {
				if list[i].ID == keep.ID {
					continue
				}
				if err := s.Store.DeleteMarketRecord(ctx, tx, list[i].ID); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := s.Store.InsertMarketRecord(ctx, tx, adjust); err != nil {
			return err
		}
	}
	return s.Store.RecomputeMarketItemAggregates(ctx, tx, marketID)
}

// DeleteMarketItemRow 删除一行市集明细，连同该产品在本场的现场记录。
func (s *Service) DeleteMarketItemRow(ctx context.Context, marketID, itemID int64, user *model.User) error {
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
		if err := s.Store.DeleteMarketRecordsOfProduct(ctx, tx, marketID, item.ProductID); err != nil {
			return err
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
		// 计算方式要先规整：扣点类费用的 Amount 本来就是 0，
		// 不能因为"金额为 0 且没备注"被当成空行丢掉。
		calc := model.ExpenseCalcFixed
		if e.Calc == model.ExpenseCalcPercent {
			calc = model.ExpenseCalcPercent
		}
		if calc == model.ExpenseCalcFixed && e.Amount == 0 && strings.TrimSpace(e.Note) == "" {
			continue
		}
		if e.Amount < 0 {
			return UserErrf("费用金额不能为负数")
		}
		if calc == model.ExpenseCalcPercent {
			if e.Rate <= 0 || e.Rate > 10000 {
				return UserErrf("按销售额扣点的比例应在 0-100 之间")
			}
		} else if e.Rate < 0 {
			return UserErrf("费用比例不能为负数")
		}
		if _, ok := model.ExpenseCategoryLabels[e.Category]; !ok {
			e.Category = model.ExpenseOther
		}
		amount := e.Amount
		if calc == model.ExpenseCalcPercent {
			amount = 0 // 扣点金额由销售额算出，不存固定值
		}
		clean = append(clean, model.MarketExpense{
			Category: e.Category,
			Amount:   amount,
			Calc:     calc,
			Rate:     e.Rate,
			Note:     strings.TrimSpace(e.Note),
		})
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
	totals := m.Totals()
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
			// 逐笔记录也锁定同一成本，保证撤销结算前后损益口径一致
			if err := s.Store.SetMarketRecordCost(ctx, tx, marketID, it.ProductID, cost); err != nil {
				return err
			}
		}

		totals := model.ComputeTotals(items, m.Expenses, m.Records)
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
		if err := s.Store.ClearMarketRecordCosts(ctx, tx, marketID); err != nil {
			return err
		}
		if err := s.RebuildAffected(ctx, tx, affected); err != nil {
			return err
		}
		totals := model.ComputeTotals(liveItems(m.Items), m.Expenses, liveRecords(m.Records))
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

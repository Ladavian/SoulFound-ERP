package web

import (
	"fmt"
	"net/http"
	"strconv"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// marketRow 市集列表行（含实时损益）。
type marketRow struct {
	Market model.Market
	Totals model.MarketTotals
}

// ---------------------------------------------------------------- 市集列表

func (s *Server) handleMarketList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	filter := store.MarketFilter{
		Keyword: f.Str("q"),
		Status:  f.Str("status"),
		From:    f.Str("from"),
		To:      f.Str("to"),
		Sort:    f.Str("sort"),
	}
	markets, err := s.svc.Store.ListMarkets(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows := make([]marketRow, 0, len(markets))
	var summary model.MarketTotals
	var totalSold, totalTasting model.Qty
	for _, m := range markets {
		t := m.Totals()
		rows = append(rows, marketRow{Market: m, Totals: t})
		summary.Revenue += t.Revenue
		summary.CogsSold += t.CogsSold
		summary.TastingCost += t.TastingCost
		summary.LossCost += t.LossCost
		summary.ExpenseCost += t.ExpenseCost
		summary.NetProfit += t.NetProfit
		totalSold += m.SoldQty()
		totalTasting += m.TastingQty()
	}
	conversion := 0.0
	if totalTasting != 0 {
		conversion = float64(totalSold) / float64(totalTasting) * 100
	}

	// 正在进行的那场放到最上面，方便现场一键进收银台
	var (
		ongoing        *model.Market
		ongoingRevenue model.Money
		ongoingOrders  int
	)
	for i := range rows {
		if rows[i].Market.Status == model.MarketOngoing {
			ongoing = &rows[i].Market
			ongoingRevenue = rows[i].Totals.Revenue
			ongoingOrders = rows[i].Market.SaleCount()
			break
		}
	}

	noCache(w)
	page := s.newPage(r, "市集活动", "markets")
	page["Rows"] = rows
	page["Ongoing"] = ongoing
	page["OngoingRevenue"] = ongoingRevenue
	page["OngoingSaleCount"] = ongoingOrders
	page["Summary"] = summary
	page["Count"] = len(rows)
	page["TotalSold"] = totalSold
	page["TotalTasting"] = totalTasting
	page["Conversion"] = conversion
	page["StatusOptions"] = model.MarketStatusFilterOptions
	page["Keyword"] = filter.Keyword
	page["Status"] = filter.Status
	page["From"] = filter.From
	page["To"] = filter.To
	page["Sort"] = filter.Sort
	page["ShowCost"] = canEdit(r, PermReportView)
	page["CanManage"] = canEdit(r, PermMarketManage)
	page["CanSettle"] = canEdit(r, PermMarketSettle)
	if err := s.rnd.Render(w, "markets/list", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 市集表单

func (s *Server) handleMarketForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	isNew := id == 0

	var market *model.Market
	if !isNew {
		var err error
		market, err = s.svc.Store.MarketByID(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if market == nil {
			s.notFound(w, r, "市集不存在")
			return
		}
	} else {
		market = &model.Market{StartDate: store.Today(), EndDate: store.Today()}
	}

	noCache(w)
	page := s.newPage(r, "市集活动", "markets")
	page["Market"] = market
	page["IsNew"] = isNew
	page["Today"] = store.Today()
	page["FormAction"] = "/markets/new"
	if !isNew {
		page["FormAction"] = "/markets/" + strconv.FormatInt(id, 10) + "/edit"
		page["Totals"] = market.Totals()
	}
	if err := s.rnd.Render(w, "markets/form", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleMarketSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	f := newFormReader(r)
	in := service.MarketInput{
		ID:        id,
		Name:      f.Required("name", "市集名称"),
		Venue:     f.Str("venue"),
		City:      f.Str("city"),
		Organizer: f.Str("organizer"),
		StartDate: f.Str("start_date"),
		EndDate:   f.Str("end_date"),
		Notes:     f.Str("notes"),
	}
	fallback := "/markets"
	if id > 0 {
		fallback = "/markets/" + strconv.FormatInt(id, 10) + "/edit"
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	newID, err := s.svc.SaveMarket(ctx, in, userFrom(r))
	if err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	target := "/markets/" + strconv.FormatInt(newID, 10)
	if id > 0 {
		s.ok(w, r, target, "市集信息已更新")
		return
	}
	s.ok(w, r, target, "市集已创建，接下来添加产品并填写试饮与销售数量")
}

// ---------------------------------------------------------------- 市集详情

// marketPanelData 组装市集面板数据（明细 + 费用 + 损益 + 可选产品）。
func (s *Server) marketPanelData(r *http.Request, marketID int64) (map[string]any, error) {
	m, err := s.svc.Store.MarketByID(r.Context(), marketID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, service.UserErrf("市集不存在")
	}

	all, err := s.productOptions(r)
	if err != nil {
		return nil, err
	}
	added := make(map[int64]bool, len(m.Items))
	for _, it := range m.Items {
		added[it.ProductID] = true
	}
	available := make([]ProductOption, 0, len(all))
	for _, o := range all {
		if !added[o.ID] {
			available = append(available, o)
		}
	}

	// 未结算时才检查库存是否够
	var shortages []string
	if !m.IsSettled() {
		for _, it := range m.Items {
			if it.OutQty() > it.StockQty {
				shortages = append(shortages, fmt.Sprintf("%s：需要 %s %s，当前库存仅 %s",
					it.ProductName, it.OutQty(), it.UnitOrDefault(), it.StockQty))
			}
		}
	}

	return map[string]any{
		"Market":            m,
		"Totals":            m.Totals(),
		"AvailableProducts": available,
		"Shortages":         shortages,
		"ExpenseCategories": model.ExpenseCategoryOptions,
		"ShowCost":          canEdit(r, PermReportView),
		"CanManage":         canEdit(r, PermMarketManage),
		"CanSettle":         canEdit(r, PermMarketSettle),
		"RowError":          "",
		"RowErrorID":        int64(0),
	}, nil
}

func (s *Server) handleMarketDetail(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	panel, err := s.marketPanelData(r, id)
	if err != nil {
		if _, ok := service.AsUserError(err); ok {
			s.notFound(w, r, "市集不存在或已被删除")
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	noCache(w)
	page := s.newPage(r, "市集活动", "markets")
	for k, v := range panel {
		page[k] = v
	}
	if err := s.rnd.Render(w, "markets/detail", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderMarketPanel 重新渲染面板片段（HTMX 局部刷新）。
func (s *Server) renderMarketPanel(w http.ResponseWriter, r *http.Request, marketID int64) {
	panel, err := s.marketPanelData(r, marketID)
	if err != nil {
		s.failPartial(w, r, "markets/detail", "market_panel", nil, err)
		return
	}
	if err := s.rnd.RenderPartial(w, "markets/detail", "market_panel", panel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 明细维护

func (s *Server) handleMarketStatus(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	target := "/markets/" + strconv.FormatInt(id, 10)
	f := newFormReader(r)
	status := f.Str("status")
	if err := s.svc.SetMarketStatus(r.Context(), id, status, userFrom(r)); err != nil {
		s.fail(w, r, target, err)
		return
	}
	s.ok(w, r, target, "市集状态已更新为「"+model.MarketStatusLabels[status]+"」")
}

func (s *Server) handleMarketItemAdd(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	f := newFormReader(r)
	productID := f.ID("product_id", "产品")
	if productID <= 0 {
		s.failPartial(w, r, "markets/detail", "market_panel", nil, service.UserErrf("请选择要添加的产品"))
		return
	}
	if _, err := s.svc.AddMarketProduct(r.Context(), id, productID, userFrom(r)); err != nil {
		s.failPartial(w, r, "markets/detail", "market_panel", nil, err)
		return
	}
	s.renderMarketPanel(w, r, id)
}

func (s *Server) handleMarketItemUpdate(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	itemID := pathID(r, "itemID")
	f := newFormReader(r)
	up := service.MarketItemUpdate{
		CarriedQty:  f.Qty("carried_qty", "带去数量"),
		TastingQty:  f.Qty("tasting_qty", "试饮数量"),
		SoldQty:     f.Qty("sold_qty", "销售数量"),
		GiftQty:     f.Qty("gift_qty", "赠送数量"),
		LossQty:     f.Qty("loss_qty", "损耗数量"),
		UnitPrice:   f.Money("unit_price", "售价"),
		DiscountAmt: f.Money("discount_amt", "优惠金额"),
		Note:        f.Str("note"),
	}

	err := f.Err()
	if err == nil {
		err = s.svc.UpdateMarketItemRow(r.Context(), id, itemID, up, userFrom(r))
	}
	if err == nil {
		s.renderMarketPanel(w, r, id)
		return
	}

	// 保存失败：保留用户刚输入的内容并提示原因
	msg, ok := service.AsUserError(err)
	if !ok {
		msg = "保存失败，请重试"
	}
	panel, perr := s.marketPanelData(r, id)
	if perr != nil {
		s.failPartial(w, r, "markets/detail", "market_panel", nil, perr)
		return
	}
	if m, ok := panel["Market"].(*model.Market); ok {
		for i := range m.Items {
			if m.Items[i].ID == itemID {
				m.Items[i].CarriedQty = up.CarriedQty
				m.Items[i].TastingQty = up.TastingQty
				m.Items[i].SoldQty = up.SoldQty
				m.Items[i].GiftQty = up.GiftQty
				m.Items[i].LossQty = up.LossQty
				m.Items[i].UnitPrice = up.UnitPrice
				m.Items[i].DiscountAmt = up.DiscountAmt
				m.Items[i].Note = up.Note
			}
		}
		panel["Totals"] = m.Totals()
	}
	panel["RowError"] = msg
	panel["RowErrorID"] = itemID
	if err := s.rnd.RenderPartial(w, "markets/detail", "market_panel", panel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleMarketItemDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	itemID := pathID(r, "itemID")
	if err := s.svc.DeleteMarketItemRow(r.Context(), id, itemID, userFrom(r)); err != nil {
		s.failPartial(w, r, "markets/detail", "market_panel", nil, err)
		return
	}
	s.renderMarketPanel(w, r, id)
}

// ---------------------------------------------------------------- 费用

func (s *Server) handleMarketExpenses(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	f := newFormReader(r)
	categories := f.List("category")
	amounts := f.List("amount")
	notes := f.List("note")

	expenses := make([]model.MarketExpense, 0, len(categories))
	for i := range categories {
		amount, err := model.ParseMoney(at(amounts, i))
		if err != nil {
			f.AddError("第 %d 行费用金额格式不正确", i+1)
			continue
		}
		expenses = append(expenses, model.MarketExpense{
			Category: at(categories, i),
			Amount:   amount,
			Note:     at(notes, i),
		})
	}
	if err := f.Err(); err != nil {
		s.failPartial(w, r, "markets/detail", "market_panel", nil, err)
		return
	}
	if err := s.svc.SaveMarketExpenses(r.Context(), id, expenses, userFrom(r)); err != nil {
		s.failPartial(w, r, "markets/detail", "market_panel", nil, err)
		return
	}
	s.renderMarketPanel(w, r, id)
}

// ---------------------------------------------------------------- 结算

func (s *Server) handleMarketSettle(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	target := "/markets/" + strconv.FormatInt(id, 10)
	if err := s.svc.SettleMarket(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, target, err)
		return
	}
	s.ok(w, r, target, "已结算：库存已扣减，成本与利润已锁定")
}

func (s *Server) handleMarketUnsettle(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	target := "/markets/" + strconv.FormatInt(id, 10)
	if err := s.svc.UnsettleMarket(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, target, err)
		return
	}
	s.ok(w, r, target, "已撤销结算，库存已回滚，可继续修改明细")
}

func (s *Server) handleMarketDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if err := s.svc.DeleteMarket(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, "/markets/"+strconv.FormatInt(id, 10), err)
		return
	}
	s.ok(w, r, "/markets", "市集已删除")
}

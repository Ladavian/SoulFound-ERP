package service

import (
	"context"
	"sort"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// DashboardData 首页看板数据。
type DashboardData struct {
	Stats           model.DashboardStats
	Monthly         []model.MonthlyPoint
	TopProducts     []model.ProductSalesRow
	RecentMarkets   []model.Market
	RecentMovements []model.StockMovement
	LowStock        []model.Product
	CategoryStock   []store.CategoryStock
	DraftPurchases  int
}

// Dashboard 汇总首页所需数据。
func (s *Service) Dashboard(ctx context.Context, months int) (*DashboardData, error) {
	if months <= 0 {
		months = 12
	}
	out := &DashboardData{}

	qty, value, err := s.Store.SumStock(ctx)
	if err != nil {
		return nil, err
	}
	skuCount, lowCount, err := s.Store.CountProducts(ctx)
	if err != nil {
		return nil, err
	}
	out.Stats.StockQty = qty
	out.Stats.StockValue = value
	out.Stats.SKUCount = skuCount
	out.Stats.LowStockCount = lowCount

	if out.Stats.SupplierCount, err = s.Store.CountSuppliers(ctx); err != nil {
		return nil, err
	}
	if out.Stats.CustomerCount, err = s.Store.CountCustomers(ctx); err != nil {
		return nil, err
	}
	if out.Stats.ActiveMarkets, err = s.Store.CountActiveMarkets(ctx); err != nil {
		return nil, err
	}
	if out.DraftPurchases, err = s.Store.CountDraftPurchases(ctx); err != nil {
		return nil, err
	}

	if out.Monthly, err = s.MonthlySeries(ctx, months); err != nil {
		return nil, err
	}

	today := todayDate()
	monthFrom := fmtDate(monthStart(today))
	yearFrom := fmtDate(yearStart(today))
	todayStr := fmtDate(today)

	monthMarkets, err := s.Store.ListMarkets(ctx, store.MarketFilter{From: monthFrom, To: todayStr})
	if err != nil {
		return nil, err
	}
	for _, m := range monthMarkets {
		t := m.Totals()
		out.Stats.MonthRevenue += t.Revenue
		out.Stats.MonthProfit += t.NetProfit
		out.Stats.MonthSoldQty += m.SoldQty()
	}
	out.Stats.MonthMarkets = len(monthMarkets)

	yearMarkets, err := s.Store.ListMarkets(ctx, store.MarketFilter{From: yearFrom, To: todayStr})
	if err != nil {
		return nil, err
	}
	for _, m := range yearMarkets {
		t := m.Totals()
		out.Stats.YearRevenue += t.Revenue
		out.Stats.YearProfit += t.NetProfit
	}
	out.Stats.YearMarkets = len(yearMarkets)

	// 团单与直销也要算进来，否则"总利润"只反映市集
	if g, err := s.GroupOrderSummaryBetween(ctx, monthFrom, todayStr); err == nil {
		out.Stats.MonthGroupAmount = g.Amount
		out.Stats.MonthGroupProfit = g.Profit
	}
	if g, err := s.GroupOrderSummaryBetween(ctx, yearFrom, todayStr); err == nil {
		out.Stats.YearGroupAmount = g.Amount
		out.Stats.YearGroupProfit = g.Profit
	}
	if d, err := s.Store.DirectSaleSummaryBetween(ctx, monthFrom, todayStr); err == nil {
		out.Stats.MonthDirectAmount = d.Amount
		out.Stats.MonthDirectProfit = d.Profit()
	}
	if d, err := s.Store.DirectSaleSummaryBetween(ctx, yearFrom, todayStr); err == nil {
		out.Stats.YearDirectAmount = d.Amount
		out.Stats.YearDirectProfit = d.Profit()
	}

	if out.TopProducts, err = s.ProductSalesRanking(ctx, yearFrom, todayStr, 5); err != nil {
		return nil, err
	}
	if out.RecentMarkets, err = s.Store.ListMarkets(ctx, store.MarketFilter{Limit: 5}); err != nil {
		return nil, err
	}
	if out.RecentMovements, err = s.Store.ListMovements(ctx, store.MovementFilter{Limit: 8}); err != nil {
		return nil, err
	}
	if out.LowStock, err = s.Store.LowStockProducts(ctx, 6); err != nil {
		return nil, err
	}
	if out.CategoryStock, err = s.Store.StockByCategory(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// MonthlySeries 近 N 个月的销售与利润走势（按市集结束日期归属月份）。
func (s *Service) MonthlySeries(ctx context.Context, months int) ([]model.MonthlyPoint, error) {
	if months <= 0 {
		months = 12
	}
	today := todayDate()
	from := fmtDate(addMonths(today, -(months - 1)))
	to := fmtDate(today)

	markets, err := s.Store.ListMarkets(ctx, store.MarketFilter{From: from, To: to, Sort: "oldest"})
	if err != nil {
		return nil, err
	}

	index := map[string]*model.MonthlyPoint{}
	points := make([]model.MonthlyPoint, 0, months)
	for i := 0; i < months; i++ {
		key := addMonths(today, -(months-1)+i).Format("2006-01")
		points = append(points, model.MonthlyPoint{Label: monthLabel(key)})
		index[key] = &points[len(points)-1]
	}

	for _, m := range markets {
		key := monthKey(m.EndDate)
		if key == "" {
			key = monthKey(m.StartDate)
		}
		p, ok := index[key]
		if !ok {
			continue
		}
		t := m.Totals()
		p.Revenue += t.Revenue
		p.Cost += t.TotalCost()
		p.Profit += t.NetProfit
		p.SoldQty += m.SoldQty()
		p.TastingQty += m.TastingQty()
	}
	return points, nil
}

// ProductSalesRanking 产品维度销售排行（按销售额倒序）。
func (s *Service) ProductSalesRanking(ctx context.Context, from, to string, limit int) ([]model.ProductSalesRow, error) {
	markets, err := s.Store.ListMarkets(ctx, store.MarketFilter{From: from, To: to})
	if err != nil {
		return nil, err
	}
	agg := map[int64]*model.ProductSalesRow{}
	for _, m := range markets {
		for _, it := range m.Items {
			row, ok := agg[it.ProductID]
			if !ok {
				row = &model.ProductSalesRow{
					ProductID:   it.ProductID,
					ProductName: it.ProductName,
					ProductSKU:  it.ProductSKU,
				}
				agg[it.ProductID] = row
			}
			row.SoldQty += it.SoldQty
			row.TastingQty += it.TastingQty
			row.Revenue += it.Revenue()
			row.Cogs += it.Cogs()
		}
	}
	rows := make([]model.ProductSalesRow, 0, len(agg))
	for _, row := range agg {
		row.Profit = row.Revenue - row.Cogs
		row.Conversion = 0
		if row.TastingQty != 0 {
			row.Conversion = float64(row.SoldQty) / float64(row.TastingQty)
		}
		rows = append(rows, *row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Revenue == rows[j].Revenue {
			return rows[i].SoldQty > rows[j].SoldQty
		}
		return rows[i].Revenue > rows[j].Revenue
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// MarketSummary 市集汇总报表。
func (s *Service) MarketSummary(ctx context.Context, from, to, status, keyword string) ([]model.MarketSummaryRow, model.MarketSummaryTotals, error) {
	markets, err := s.Store.ListMarkets(ctx, store.MarketFilter{
		From: from, To: to, Status: status, Keyword: keyword,
	})
	if err != nil {
		return nil, model.MarketSummaryTotals{}, err
	}

	rows := make([]model.MarketSummaryRow, 0, len(markets))
	var totals model.MarketSummaryTotals
	for _, m := range markets {
		t := m.Totals()
		row := model.MarketSummaryRow{
			ID:           m.ID,
			Code:         m.Code,
			Name:         m.Name,
			DateRange:    m.DateRange(),
			Status:       m.Status,
			Days:         m.Days(),
			Revenue:      t.Revenue,
			CogsSold:     t.CogsSold,
			TastingCost:  t.TastingCost,
			LossCost:     t.LossCost,
			ExpenseTotal: t.ExpenseCost,
			GrossProfit:  t.GrossProfit(),
			NetProfit:    t.NetProfit,
			NetMargin:    t.NetMargin(),
			SoldQty:      m.SoldQty(),
			TastingQty:   m.TastingQty(),
		}
		if row.TastingQty != 0 {
			row.Conversion = float64(row.SoldQty) / float64(row.TastingQty)
		}
		rows = append(rows, row)

		totals.Count++
		totals.Revenue += row.Revenue
		totals.CogsSold += row.CogsSold
		totals.TastingCost += row.TastingCost
		totals.LossCost += row.LossCost
		totals.ExpenseTotal += row.ExpenseTotal
		totals.GrossProfit += row.GrossProfit
		totals.NetProfit += row.NetProfit
		totals.SoldQty += row.SoldQty
		totals.TastingQty += row.TastingQty
	}
	if totals.Revenue != 0 {
		totals.NetMargin = float64(totals.NetProfit) / float64(totals.Revenue) * 100
	}
	if totals.TastingQty != 0 {
		totals.Conversion = float64(totals.SoldQty) / float64(totals.TastingQty)
	}
	return rows, totals, nil
}

// ExpenseBreakdown 费用类别汇总。
func (s *Service) ExpenseBreakdown(ctx context.Context, from, to, status string) ([]model.Option, map[string]model.Money, error) {
	markets, err := s.Store.ListMarkets(ctx, store.MarketFilter{From: from, To: to, Status: status})
	if err != nil {
		return nil, nil, err
	}
	sums := map[string]model.Money{}
	for _, m := range markets {
		for _, e := range m.Expenses {
			sums[e.Category] += e.Amount
		}
	}
	labels := make([]model.Option, 0, len(model.ExpenseCategoryOptions))
	for _, opt := range model.ExpenseCategoryOptions {
		if _, ok := sums[opt.Value]; ok {
			labels = append(labels, opt)
		}
	}
	// 保证顺序稳定
	sort.SliceStable(labels, func(i, j int) bool {
		return sums[labels[i].Value] > sums[labels[j].Value]
	})
	return labels, sums, nil
}

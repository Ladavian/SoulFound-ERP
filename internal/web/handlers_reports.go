package web

import (
	"net/http"
	"sort"
	"time"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// datePreset 报表快捷时间范围。
type datePreset struct {
	Label  string
	From   string
	To     string
	Active bool
}

func datePresets(from, to string) []datePreset {
	now := time.Now()
	today := now.Format("2006-01-02")
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	lastMonthStart := monthStart.AddDate(0, -1, 0)
	lastMonthEnd := monthStart.AddDate(0, 0, -1)
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())

	presets := []datePreset{
		{Label: "本月", From: monthStart.Format("2006-01-02"), To: today},
		{Label: "上月", From: lastMonthStart.Format("2006-01-02"), To: lastMonthEnd.Format("2006-01-02")},
		{Label: "近 3 个月", From: monthStart.AddDate(0, -2, 0).Format("2006-01-02"), To: today},
		{Label: "今年", From: yearStart.Format("2006-01-02"), To: today},
		{Label: "全部", From: "", To: ""},
	}
	for i := range presets {
		presets[i].Active = presets[i].From == from && presets[i].To == to
	}
	return presets
}

// ---------------------------------------------------------------- 市集汇总报表

func (s *Server) handleReportMarkets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	from, to := reportRange(f)
	status := f.Str("status")
	keyword := f.Str("q")

	rows, totals, err := s.svc.MarketSummary(ctx, from, to, status, keyword)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	labels, sums, err := s.svc.ExpenseBreakdown(ctx, from, to, status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type expenseSlice struct {
		Label string
		Total model.Money
	}
	expenses := make([]expenseSlice, 0, len(labels))
	for _, l := range labels {
		expenses = append(expenses, expenseSlice{Label: l.Label, Total: sums[l.Value]})
	}
	sort.SliceStable(expenses, func(i, j int) bool { return expenses[i].Total > expenses[j].Total })

	noCache(w)
	page := s.newPage(r, "市集利润报表", "report-markets")
	page["Rows"] = rows
	page["Totals"] = totals
	page["Expenses"] = expenses
	page["From"] = from
	page["To"] = to
	page["Status"] = status
	page["Keyword"] = keyword
	page["StatusOptions"] = model.MarketStatusFilterOptions
	page["Presets"] = datePresets(from, to)
	page["ExportURL"] = "/export/markets.xlsx?" + r.URL.RawQuery
	page["ShowCost"] = true
	if err := s.rnd.Render(w, "reports/markets", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 产品销售报表

func (s *Server) handleReportProducts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	from, to := reportRange(f)

	rows, err := s.svc.ProductSalesRanking(ctx, from, to, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var (
		totalRevenue model.Money
		totalCogs    model.Money
		totalSold    model.Qty
		totalTasting model.Qty
	)
	for _, row := range rows {
		totalRevenue += row.Revenue
		totalCogs += row.Cogs
		totalSold += row.SoldQty
		totalTasting += row.TastingQty
	}
	totalProfit := totalRevenue - totalCogs
	margin := 0.0
	if totalRevenue != 0 {
		margin = float64(totalProfit) / float64(totalRevenue) * 100
	}
	conversion := 0.0
	if totalTasting != 0 {
		conversion = float64(totalSold) / float64(totalTasting) * 100
	}

	noCache(w)
	page := s.newPage(r, "产品销售报表", "report-products")
	page["Rows"] = rows
	page["From"] = from
	page["To"] = to
	page["Presets"] = datePresets(from, to)
	page["TotalRevenue"] = totalRevenue
	page["TotalCogs"] = totalCogs
	page["TotalProfit"] = totalProfit
	page["TotalMargin"] = margin
	page["TotalSold"] = totalSold
	page["TotalTasting"] = totalTasting
	page["Conversion"] = conversion
	page["ExportURL"] = "/export/products.xlsx?" + r.URL.RawQuery
	if err := s.rnd.Render(w, "reports/products", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 库存报表

func (s *Server) handleReportInventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	products, err := s.svc.Store.ListProducts(ctx, store.ProductFilter{
		IncludeInactive: true, Sort: "value",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	byCategory, _ := s.svc.Store.StockByCategory(ctx)

	var (
		totalQty   model.Qty
		totalValue model.Money
	)
	for _, p := range products {
		totalQty += p.StockQty
		totalValue += p.StockValue
	}

	noCache(w)
	page := s.newPage(r, "库存报表", "report-inventory")
	page["Products"] = products
	page["CategoryStock"] = byCategory
	page["TotalQty"] = totalQty
	page["TotalValue"] = totalValue
	page["Count"] = len(products)
	page["ExportURL"] = "/export/inventory.xlsx"
	if err := s.rnd.Render(w, "reports/inventory", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// reportRange 读取报表时间范围，默认今年至今天。
func reportRange(f *formReader) (string, string) {
	from := f.Str("from")
	to := f.Str("to")
	if from == "" && to == "" && !f.hasKey("from") && !f.hasKey("to") {
		now := time.Now()
		from = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
		to = now.Format("2006-01-02")
	}
	return from, to
}

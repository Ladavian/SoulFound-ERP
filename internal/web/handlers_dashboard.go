package web

import (
	"encoding/json"
	"html/template"
	"net/http"
)

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	data, err := s.svc.Dashboard(r.Context(), 12)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 成本与利润属于敏感信息：没有 report.view 权限的角色（例如店员）
	// 既不能看到利润数字，也不应该从图表数据里读到。
	showCost := canEdit(r, PermReportView)

	noCache(w)
	page := s.newPage(r, "经营看板", "dashboard")
	page["ShowCost"] = showCost
	page["Stats"] = data.Stats
	page["Monthly"] = data.Monthly
	page["TopProducts"] = data.TopProducts
	page["RecentMarkets"] = data.RecentMarkets
	page["RecentMovements"] = data.RecentMovements
	page["LowStock"] = data.LowStock
	page["CategoryStock"] = data.CategoryStock
	page["DraftPurchases"] = data.DraftPurchases

	// 折线图数据
	labels := make([]string, 0, len(data.Monthly))
	revenue := make([]float64, 0, len(data.Monthly))
	profit := make([]float64, 0, len(data.Monthly))
	for _, p := range data.Monthly {
		labels = append(labels, p.Label)
		revenue = append(revenue, p.Revenue.Float())
		if showCost {
			profit = append(profit, p.Profit.Float())
		}
	}
	// 各市集净利润对比（取最近 6 场已结算/进行中）
	marketLabels := make([]string, 0, 6)
	marketProfit := make([]float64, 0, 6)
	for i := len(data.RecentMarkets) - 1; showCost && i >= 0; i-- {
		m := data.RecentMarkets[i]
		name := m.Name
		if len([]rune(name)) > 12 {
			name = string([]rune(name)[:12]) + "…"
		}
		marketLabels = append(marketLabels, name)
		marketProfit = append(marketProfit, m.NetProfit.Float())
	}
	payload, _ := json.Marshal(map[string]any{
		"labels":       labels,
		"revenue":      revenue,
		"profit":       profit,
		"marketLabels": marketLabels,
		"marketProfit": marketProfit,
	})
	page["ChartJSON"] = template.JS(payload)
	page["ShowMarketChart"] = showCost && len(marketLabels) > 0

	if err := s.rnd.Render(w, "dashboard", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

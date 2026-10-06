package web

import (
	"net/http"
	"sort"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

const ecPageSize = 50

// handleEcommerce 电商平台账单首页：导入入口 + 待绑定商品 + 订单列表。
func (s *Server) handleEcommerce(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	platform := s.ecPlatform(r)

	fr := newFormReader(r)
	pageNo := fr.Int("page", "页码")
	if pageNo < 1 {
		pageNo = 1
	}
	base := store.EcOrderFilter{
		Platform:  platform,
		Status:    fr.Str("status"),
		Keyword:   fr.Str("q"),
		From:      fr.Str("from"),
		To:        fr.Str("to"),
		Unmatched: fr.Str("unmatched") == "1",
		Limit:     ecPageSize,
	}
	f := base
	f.Offset = (pageNo - 1) * ecPageSize
	orders, err := s.svc.Store.ListEcOrders(ctx, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, _ := s.svc.Store.CountEcOrders(ctx, base)
	unmatched, _ := s.svc.Store.EcUnmatched(ctx, platform, 100)
	unmatchedLines, _ := s.svc.Store.CountEcUnmatched(ctx, platform)
	statuses, _ := s.svc.Store.EcOrderStatusCounts(ctx, platform)

	// 绑定用的产品候选（顺带把已有电商商品ID 带上，方便显示"已绑定"）
	products, _ := s.svc.Store.ListProducts(ctx, store.ProductFilter{Sort: "name"})
	links, _ := s.svc.Store.AllProductLinks(ctx)
	opts := make([]map[string]any, 0, len(products))
	for _, p := range products {
		opts = append(opts, map[string]any{
			"id":    p.ID,
			"label": p.Name + " · " + p.SKU,
			"links": len(links[p.ID]),
		})
	}

	allLinks, _ := s.svc.Store.AllProductLinks(ctx)
	var flat []model.ProductEcLink
	for _, list := range allLinks {
		flat = append(flat, list...)
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i].ID < flat[j].ID })

	// 汇总：有效销售的实收 / 成本 / 毛利。
	//
	// 关键口径：毛利只统计"已绑定产品且填了电商成本"的行。
	// 没成本的行不能按 0 成本算成毛利，否则毛利会虚高成全额收入；
	// 这部分单独用 MissingCostAmount 提示，让人去补。
	var revenue, cost, knownRevenue, missing model.Money
	var lines int
	for _, o := range orders {
		for _, it := range o.Items {
			if !it.Counts() {
				continue
			}
			lines++
			revenue += it.NetPaid()
			if it.Matched() && it.EcCost > 0 {
				cost += it.ItemCost()
				knownRevenue += it.NetPaid()
			} else {
				missing += it.NetPaid()
			}
		}
	}

	noCache(w)
	page := s.newPage(r, "电商平台账单", "ecommerce")
	page["Platform"] = platform
	page["PlatformLabel"] = model.EcPlatformLabel(platform)
	page["Orders"] = orders
	page["Total"] = total
	page["Page"] = pageNo
	page["PageSize"] = ecPageSize
	page["HasPrev"] = pageNo > 1
	page["HasNext"] = pageNo*ecPageSize < total
	page["PrevURL"] = withPage(r, pageNo-1)
	page["NextURL"] = withPage(r, pageNo+1)
	page["TotalPages"] = (total + ecPageSize - 1) / ecPageSize
	page["Status"] = f.Status
	page["Keyword"] = f.Keyword
	page["From"] = f.From
	page["To"] = f.To
	page["UnmatchedOnly"] = f.Unmatched
	page["Unmatched"] = unmatched
	page["UnmatchedLines"] = unmatchedLines
	page["Statuses"] = statuses
	page["ProductOptions"] = opts
	page["Links"] = flat
	page["PlatformOptions"] = model.EcPlatformOptions()
	page["Revenue"] = revenue
	page["Cost"] = cost
	page["Profit"] = knownRevenue - cost
	page["KnownRevenue"] = knownRevenue
	page["MissingCostAmount"] = missing
	page["LineCount"] = lines
	if msg := fr.Str("msg"); msg != "" {
		page["Flash"] = []Flash{{Level: "info", Text: msg}}
	}
	if err := s.rnd.Render(w, "ecommerce", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleEcOrderDetail 单张平台订单。
func (s *Server) handleEcOrderDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	order, err := s.svc.Store.EcOrderByID(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if order == nil {
		s.notFound(w, r, "订单不存在")
		return
	}
	products, _ := s.svc.Store.ListProducts(ctx, store.ProductFilter{Sort: "name"})
	noCache(w)
	page := s.newPage(r, "订单详情", "ecommerce")
	page["Order"] = order
	page["PlatformLabel"] = model.EcPlatformLabel(order.Platform)
	page["Products"] = products
	if err := s.rnd.Render(w, "ecommerce_order", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ecPlatform 当前查看的平台，默认淘宝。
func (s *Server) ecPlatform(r *http.Request) string {
	p := strings.TrimSpace(r.URL.Query().Get("platform"))
	if p == "" {
		return model.EcTaobao
	}
	return p
}

package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// oneBottle 快捷按钮的默认数量：1 瓶。
func oneBottle() model.Qty { return model.MustQty("1") }

// posTile 收银台上的一个产品按钮块。
//
// Item 可能为空：市集现场不必提前"上架"，任何在售产品都能直接记账，
// 记完会自动补进本场明细。Item 非空表示这场提前计划过（填了带去数量或售价）。
type posTile struct {
	Product  *model.Product
	Item     *model.MarketItem
	Out      model.Qty // 已出库（销售+试饮+赠送+损耗）
	Sold     model.Qty // 已售出
	Left     model.Qty // 带去数量 - 已出库
	Warning  string
	OnMarket bool
	// Search 供前端即时过滤：名称 + 编码 + 条码
	Search string
}

// posPanelData 组装收银台数据。
//
// 收银台是市集现场的主界面：一排产品按钮点一下记一笔，
// 顶部实时显示单数/销售额/瓶数，下方是最近的流水（可撤销）。
func (s *Server) posPanelData(r *http.Request, marketID int64, view map[string]any) (map[string]any, error) {
	ctx := r.Context()
	m, err := s.svc.Store.MarketByID(ctx, marketID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, service.UserErrf("市集不存在")
	}

	summary := service.SummarizeMarketRecords(m.Records)

	byProduct := map[int64]service.MarketRecordSummaryLine{}
	for _, line := range summary.ByProduct {
		byProduct[line.ProductID] = line
	}
	// 本场明细：用于标记"本场"与计算超量提示
	itemOf := map[int64]*model.MarketItem{}
	for i := range m.Items {
		itemOf[m.Items[i].ProductID] = &m.Items[i]
	}

	// 列出全部在用产品：现场不必提前上架，扫到/点到谁就记谁。
	// 已在本场明细里的排前面，方便按计划卖。
	all, err := s.svc.Store.ListProducts(ctx, store.ProductFilter{Sort: "name"})
	if err != nil {
		return nil, err
	}
	tiles := make([]posTile, 0, len(all))
	for _, p := range all {
		if !p.IsActive {
			continue
		}
		product := p
		it := itemOf[product.ID]
		out := service.OutQtyOf(m.Records, product.ID)
		tile := posTile{
			Product:  &product,
			Item:     it,
			Out:      out,
			Sold:     byProduct[product.ID].SoldQty,
			OnMarket: it != nil,
			Search:   product.Name + " " + product.SKU + " " + product.NameEn + " " + product.Barcode,
		}
		if it != nil {
			tile.Left = it.CarriedQty - out
			tile.Warning = service.StockWarning(*it, m.Records)
		}
		tiles = append(tiles, tile)
	}
	// 本场的排前面
	sort.SliceStable(tiles, func(i, j int) bool {
		if tiles[i].OnMarket != tiles[j].OnMarket {
			return tiles[i].OnMarket
		}
		return false
	})

	// 最近流水（默认 30 条，够现场回看）
	recent := m.Records
	if len(recent) > 30 {
		recent = recent[:30]
	}

	totals := m.Totals()
	data := map[string]any{
		"Market":        m,
		"Totals":        totals,
		"GrossProfit":   totals.Revenue - totals.CogsSold,
		"Summary":       summary,
		"Tiles":         tiles,
		"Records":       recent,
		"RecordKinds":   model.RecordKindOptions,
		"CanManage":     canEdit(r, PermMarketManage),
		"CanSettle":     canEdit(r, PermMarketSettle),
		"ShowCost":      canEdit(r, PermReportView),
		"NeedsScanner":  true, // 收银台要扫码
		"OnMarketCount": onMarketCount(tiles),
		"TotalCount":    len(tiles),
		"ScanNotice":    "",
		"ScanError":     "",
		"UnknownCode":   "",
		"BindProducts":  nil,
		"QtyOne":        oneBottle(),
	}
	for k, v := range view {
		data[k] = v
	}
	return data, nil
}

// renderPOSPanel 局部刷新收银台面板。
func (s *Server) renderPOSPanel(w http.ResponseWriter, r *http.Request, marketID int64, view map[string]any) {
	data, err := s.posPanelData(r, marketID, view)
	if err != nil {
		s.failPartial(w, r, "markets/pos", "pos_panel", nil, err)
		return
	}
	if err := s.rnd.RenderPartial(w, "markets/pos", "pos_panel", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleMarketPOS 渲染收银台整页。
func (s *Server) handleMarketPOS(w http.ResponseWriter, r *http.Request) {
	marketID := pathID(r, "id")
	data, err := s.posPanelData(r, marketID, nil)
	if err != nil {
		if _, ok := service.AsUserError(err); ok {
			s.notFound(w, r, "市集不存在或已被删除")
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	noCache(w)
	page := s.newPage(r, "市集收银台", "markets")
	for k, v := range data {
		page[k] = v
	}
	if err := s.rnd.Render(w, "markets/pos", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleAddRecord 记一笔现场流水（四个快捷按钮都走这里）。
func (s *Server) handleAddRecord(w http.ResponseWriter, r *http.Request) {
	marketID := pathID(r, "id")
	f := newFormReader(r)
	kind := f.Str("kind")
	in := service.MarketRecordInput{
		ProductID: f.ID("product_id", "产品"),
		Kind:      kind,
		Qty:       f.Qty("qty", "数量"),
		UnitPrice: f.Money("unit_price", "单价"),
		Discount:  f.Money("discount", "优惠"),
		Channel:   "manual",
		Note:      f.Str("note"),
	}
	if in.Qty == 0 {
		in.Qty = oneBottle()
	}
	// 销售的成交价缺省用明细行/产品售价，服务层会自动补齐
	if _, err := s.svc.AddMarketRecord(r.Context(), marketID, in, currentUser(r)); err != nil {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
		return
	}
	label := model.RecordKindLabel(kind)
	s.renderPOSPanel(w, r, marketID, map[string]any{
		"ScanNotice": label + " 已记录",
	})
}

// handleDeleteRecord 撤销一笔（记错了）。
func (s *Server) handleDeleteRecord(w http.ResponseWriter, r *http.Request) {
	marketID := pathID(r, "id")
	recordID := pathID(r, "rid")
	if err := s.svc.DeleteMarketRecordRow(r.Context(), marketID, recordID, currentUser(r)); err != nil {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
		return
	}
	s.renderPOSPanel(w, r, marketID, map[string]any{"ScanNotice": "已撤销这一笔"})
}

// handleUpdateRecord 修改一笔的数量与成交价。
func (s *Server) handleUpdateRecord(w http.ResponseWriter, r *http.Request) {
	marketID := pathID(r, "id")
	recordID := pathID(r, "rid")
	f := newFormReader(r)
	up := service.MarketRecordInput{
		Kind:      f.Str("kind"),
		Qty:       f.Qty("qty", "数量"),
		UnitPrice: f.Money("unit_price", "单价"),
		Discount:  f.Money("discount", "优惠"),
		Note:      f.Str("note"),
	}
	if err := s.svc.UpdateMarketRecordRow(r.Context(), marketID, recordID, up, currentUser(r)); err != nil {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
		return
	}
	s.renderPOSPanel(w, r, marketID, map[string]any{"ScanNotice": "已保存修改"})
}

// handlePOSScan 扫码出库：识别条码 → 找到产品 → 记一笔销售。
//
// 条码没绑过产品时不直接报错，而是返回一份"这是哪个产品"的选择表单，
// 现场确认一次就完成绑定，下次再扫就能直接出库。
func (s *Server) handlePOSScan(w http.ResponseWriter, r *http.Request) {
	marketID := pathID(r, "id")
	f := newFormReader(r)
	code := strings.TrimSpace(f.Str("code"))
	if code == "" {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": "没有读到条码内容，请对准条形码再试一次"})
		return
	}

	res, err := s.svc.LookupBarcode(r.Context(), code)
	if err != nil {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
		return
	}
	if res.Product == nil {
		options, err := s.productOptions(r)
		if err != nil {
			s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
			return
		}
		s.renderPOSPanel(w, r, marketID, map[string]any{
			"UnknownCode":  code,
			"BindProducts": options,
			"ScanError":    "这个条码还没有绑过产品，请选择它属于哪个产品（确认后会自动记住）",
		})
		return
	}

	if _, err := s.svc.AddMarketRecord(r.Context(), marketID, service.MarketRecordInput{
		ProductID: res.Product.ID,
		Kind:      model.RecordSale,
		Qty:       oneBottle(),
		Channel:   "scan",
		Barcode:   code,
	}, currentUser(r)); err != nil {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
		return
	}
	s.renderPOSPanel(w, r, marketID, map[string]any{
		"ScanNotice": "已扫码出库：" + res.Product.Name,
	})
}

// handlePOSScanBind 扫码后绑定条码到产品，并顺手记一笔销售。
func (s *Server) handlePOSScanBind(w http.ResponseWriter, r *http.Request) {
	marketID := pathID(r, "id")
	f := newFormReader(r)
	code := strings.TrimSpace(f.Str("code"))
	productID := f.ID("product_id", "产品")
	if code == "" || productID == 0 {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": "请选择产品后再确认"})
		return
	}
	if err := s.svc.BindProductBarcode(r.Context(), productID, code, currentUser(r)); err != nil {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
		return
	}
	product, _ := s.svc.Store.ProductByID(r.Context(), productID)
	if _, err := s.svc.AddMarketRecord(r.Context(), marketID, service.MarketRecordInput{
		ProductID: productID,
		Kind:      model.RecordSale,
		Qty:       oneBottle(),
		Channel:   "scan",
		Barcode:   code,
	}, currentUser(r)); err != nil {
		s.renderPOSPanel(w, r, marketID, map[string]any{"ScanError": userMessage(err)})
		return
	}
	name := "产品"
	if product != nil {
		name = product.Name
	}
	s.renderPOSPanel(w, r, marketID, map[string]any{
		"ScanNotice": "已绑定条码并出库：" + name,
	})
}

// handleBarcodeLookup 供产品档案"扫码录入"使用：只返回条码对应的产品，不记账。
func (s *Server) handleBarcodeLookup(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	res, err := s.svc.LookupBarcode(r.Context(), code)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message": userMessage(err)})
		return
	}
	if res.Product == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "code": code,
			"message": "这个条码还没有绑定产品",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "code": code,
		"product": map[string]any{
			"id": res.Product.ID, "name": res.Product.Name, "sku": res.Product.SKU,
		},
	})
}

// handleBindBarcode 在产品档案里保存条码。
func (s *Server) handleBindBarcode(w http.ResponseWriter, r *http.Request) {
	productID := pathID(r, "id")
	f := newFormReader(r)
	code := strings.TrimSpace(f.Str("barcode"))

	back := "/products/" + itoa(productID)
	if f.Str("back") != "" {
		back = f.Str("back")
	}
	if err := s.svc.BindProductBarcode(r.Context(), productID, code, currentUser(r)); err != nil {
		s.setFlash(w, "error", userMessage(err))
		s.redirect(w, r, back)
		return
	}
	if code == "" {
		s.setFlash(w, "success", "已清空条码")
	} else {
		s.setFlash(w, "success", "条码已保存，市集上扫码即可出库")
	}
	s.redirect(w, r, back)
}

// handleProductLabels 条码标签打印页。
func (s *Server) handleProductLabels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	filter := store.ProductFilter{Sort: f.Str("sort")}
	if filter.Sort == "" {
		filter.Sort = "name"
	}
	products, err := s.svc.Store.ListProducts(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 只打印有货或在售的产品，避免整本标签都是停产款
	if f.Str("all") == "" {
		active := make([]model.Product, 0, len(products))
		for _, p := range products {
			if p.IsActive {
				active = append(active, p)
			}
		}
		products = active
	}

	noCache(w)
	page := s.newPage(r, "条码标签", "products")
	page["Products"] = products
	page["ShowAll"] = f.Str("all") != ""
	if err := s.rnd.Render(w, "products/labels", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// currentUser 取当前登录用户（可能为 nil，仅用于记录操作人）。
func currentUser(r *http.Request) *model.User { return userFrom(r) }

// userMessage 把错误转成给用户看的文案。
func userMessage(err error) string {
	if err == nil {
		return ""
	}
	if msg, ok := service.AsUserError(err); ok {
		return msg
	}
	return err.Error()
}

// itoa 整数转字符串。
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// writeJSON 输出 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// Price 本场售价：优先用本场明细里的价格，没上架就用产品建议售价。
func (t posTile) Price() model.Money {
	if t.Item != nil && t.Item.UnitPrice > 0 {
		return t.Item.UnitPrice
	}
	return t.Product.SalePrice
}

// CarriedQty 本场计划带去数量，没计划过返回 0。
func (t posTile) CarriedQty() model.Qty {
	if t.Item == nil {
		return 0
	}
	return t.Item.CarriedQty
}

// onMarketCount 已进入本场明细的产品数。
func onMarketCount(tiles []posTile) int {
	n := 0
	for _, t := range tiles {
		if t.OnMarket {
			n++
		}
	}
	return n
}

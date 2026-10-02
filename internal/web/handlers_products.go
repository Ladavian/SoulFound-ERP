package web

import (
	"net/http"
	"strconv"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// pathID 读取路径参数中的 ID。
func pathID(r *http.Request, name string) int64 {
	raw := r.PathValue(name)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// at 安全取列表元素。
func at(list []string, i int) string {
	if i < 0 || i >= len(list) {
		return ""
	}
	return strings.TrimSpace(list[i])
}

var productUnits = []string{"瓶", "箱", "套", "礼盒"}

// ---------------------------------------------------------------- 产品列表

func (s *Server) handleProductList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	filter := store.ProductFilter{
		Keyword:         f.Str("q"),
		Category:        f.Str("category"),
		SupplierID:      f.ID("supplier", "供应商"),
		LowStockOnly:    f.Str("low") == "1",
		IncludeInactive: f.Str("all") == "1",
		Sort:            f.Str("sort"),
	}
	products, err := s.svc.Store.ListProducts(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	suppliers, err := s.svc.Store.ListSuppliers(ctx, "", false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	categories, _ := s.svc.Store.CategoriesInUse(ctx)

	var (
		totalQty   model.Qty
		totalValue model.Money
		lowCount   int
	)
	for _, p := range products {
		totalQty += p.StockQty
		totalValue += p.StockValue
		if p.IsActive && p.IsLowStock() {
			lowCount++
		}
	}

	noCache(w)
	page := s.newPage(r, "产品档案", "products")
	page["Products"] = products
	page["Suppliers"] = suppliers
	page["Categories"] = categories
	page["Keyword"] = filter.Keyword
	page["Category"] = filter.Category
	page["SupplierID"] = filter.SupplierID
	page["LowStockOnly"] = filter.LowStockOnly
	page["IncludeInactive"] = filter.IncludeInactive
	page["Sort"] = filter.Sort
	page["Count"] = len(products)
	page["TotalQty"] = totalQty
	page["TotalValue"] = totalValue
	page["LowCount"] = lowCount
	page["CanManage"] = canEdit(r, PermProductManage)
	if err := s.rnd.Render(w, "products/list", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 产品表单

func (s *Server) handleProductForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	isNew := id == 0

	var product *model.Product
	if !isNew {
		var err error
		product, err = s.svc.Store.ProductByID(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if product == nil {
			s.notFound(w, r, "产品不存在或已被删除")
			return
		}
	} else {
		settings := settingsFrom(r)
		product = &model.Product{
			Category:       "冰酒",
			Unit:           "瓶",
			BottlesPerCase: 6,
			VolumeML:       375,
			LowStockQty:    settings.DefaultLowQty,
			IsActive:       true,
		}
	}

	suppliers, err := s.svc.Store.ListSuppliers(ctx, "", false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	categories, _ := s.svc.Store.CategoriesInUse(ctx)
	merged := model.Categories
	for _, c := range categories {
		found := false
		for _, m := range merged {
			if m == c {
				found = true
				break
			}
		}
		if !found {
			merged = append(merged, c)
		}
	}

	noCache(w)
	page := s.newPage(r, "产品档案", "products")
	page["Product"] = product
	page["IsNew"] = isNew
	page["Suppliers"] = suppliers
	page["Categories"] = merged
	page["Units"] = productUnits
	page["FormAction"] = "/products/new"
	if !isNew {
		page["FormAction"] = "/products/" + strconv.FormatInt(id, 10) + "/edit"
	}
	if err := s.rnd.Render(w, "products/form", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleProductSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	fallback := "/products"
	if id > 0 {
		fallback = "/products/" + strconv.FormatInt(id, 10)
	}

	f := newFormReader(r)
	product := &model.Product{
		ID:             id,
		SKU:            f.Required("sku", "产品编码"),
		Name:           f.Required("name", "产品名称"),
		NameEn:         f.Str("name_en"),
		Category:       f.Str("category"),
		Vintage:        f.Int("vintage", "年份"),
		VolumeML:       f.Int("volume_ml", "容量"),
		Unit:           f.Str("unit"),
		BottlesPerCase: f.Int("bottles_per_case", "每箱瓶数"),
		SalePrice:      f.Money("sale_price", "建议售价"),
		LowStockQty:    f.Qty("low_stock_qty", "库存预警线"),
		SupplierID:     f.OptionalID("supplier_id"),
		ImageURL:       f.Str("image_url"),
		Notes:          f.Str("notes"),
		IsActive:       f.Bool("is_active"),
	}
	if product.Category == "" {
		product.Category = "冰酒"
	}
	if product.Unit == "" {
		product.Unit = "瓶"
	}
	if product.BottlesPerCase <= 0 {
		product.BottlesPerCase = 1
	}
	if product.Vintage < 0 || product.Vintage > 2100 {
		f.AddError("年份不正确")
	}
	if product.VolumeML < 0 || product.VolumeML > 10000 {
		f.AddError("容量不正确")
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, fallback, err)
		return
	}

	// SKU 唯一性检查
	if existing, err := s.svc.Store.ProductBySKU(ctx, product.SKU); err != nil {
		s.fail(w, r, fallback, err)
		return
	} else if existing != nil && existing.ID != id {
		s.fail(w, r, fallback, service.UserErrf("产品编码「%s」已被「%s」占用，请换一个", product.SKU, existing.Name))
		return
	}

	if id > 0 {
		if _, err := s.svc.Store.ProductByID(ctx, id); err != nil {
			s.fail(w, r, fallback, err)
			return
		}
		if err := s.svc.Store.UpdateProduct(ctx, product); err != nil {
			s.fail(w, r, fallback, err)
			return
		}
		s.ok(w, r, "/products/"+strconv.FormatInt(id, 10), "产品已更新")
		return
	}

	newID, err := s.svc.Store.CreateProduct(ctx, product)
	if err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	s.ok(w, r, "/products/"+strconv.FormatInt(newID, 10), "产品已创建，可以开始录入采购入库了")
}

func (s *Server) handleProductToggle(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	product, err := s.svc.Store.ProductByID(ctx, id)
	if err != nil || product == nil {
		s.fail(w, r, "/products", service.UserErrf("产品不存在"))
		return
	}
	if err := s.svc.Store.SetProductActive(ctx, id, !product.IsActive); err != nil {
		s.fail(w, r, "/products", err)
		return
	}
	msg := "产品已停用"
	if !product.IsActive {
		msg = "产品已启用"
	}
	s.ok(w, r, "/products", msg)
}

func (s *Server) handleProductDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if err := s.svc.Store.DeleteProduct(r.Context(), id); err != nil {
		s.fail(w, r, "/products", err)
		return
	}
	s.ok(w, r, "/products", "产品已删除")
}

// ---------------------------------------------------------------- 产品详情

func (s *Server) handleProductDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	product, err := s.svc.Store.ProductByID(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if product == nil {
		s.notFound(w, r, "产品不存在或已被删除")
		return
	}

	movements, err := s.svc.Store.ListMovements(ctx, store.MovementFilter{ProductID: id, Limit: 30})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 统计该产品在各市集的试饮/销售表现
	markets, err := s.svc.Store.ListMarkets(ctx, store.MarketFilter{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type marketStat struct {
		MarketID   int64
		Code       string
		Name       string
		DateRange  string
		Status     string
		TastingQty model.Qty
		SoldQty    model.Qty
		Revenue    model.Money
		Profit     model.Money
	}
	var (
		stats        []marketStat
		totalTasting model.Qty
		totalSold    model.Qty
		totalRevenue model.Money
		totalProfit  model.Money
	)
	for _, m := range markets {
		for _, it := range m.Items {
			if it.ProductID != id {
				continue
			}
			stats = append(stats, marketStat{
				MarketID: m.ID, Code: m.Code, Name: m.Name, DateRange: m.DateRange(),
				Status: m.Status, TastingQty: it.TastingQty, SoldQty: it.SoldQty,
				Revenue: it.Revenue(), Profit: it.NetProfit(),
			})
			totalTasting += it.TastingQty
			totalSold += it.SoldQty
			totalRevenue += it.Revenue()
			totalProfit += it.NetProfit()
		}
	}

	noCache(w)
	page := s.newPage(r, product.Name, "products")
	page["Product"] = product
	page["Movements"] = movements
	page["MarketStats"] = stats
	page["TotalTasting"] = totalTasting
	page["TotalSold"] = totalSold
	page["TotalRevenue"] = totalRevenue
	page["TotalProfit"] = totalProfit
	// Conversion 是「试饮带动倍数」= 销售瓶数 / 试饮瓶数（不是百分比）
	page["Conversion"] = 0.0
	if totalTasting != 0 {
		page["Conversion"] = float64(totalSold) / float64(totalTasting)
	}
	page["CanManage"] = canEdit(r, PermProductManage)
	if err := s.rnd.Render(w, "products/detail", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// notFound 渲染 404 页面。
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, detail string) {
	data := s.newPage(r, "未找到", "")
	s.rnd.RenderError(w, http.StatusNotFound, "未找到", detail, data)
}

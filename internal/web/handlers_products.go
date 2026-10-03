package web

import (
	"errors"
	"fmt"
	"io"
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

var productUnits = model.DefaultUnits

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
	// 表单里可以扫码填条码：只有这一页需要 328KB 的扫码库
	page["NeedsScanner"] = true
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

	// 表单里可能带图片，先限制请求体大小，避免超大文件把内存吃满
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxImageBytes+(2<<20))
	if err := r.ParseMultipartForm(service.MaxImageBytes + (2 << 20)); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.fail(w, r, fallback, service.UserErrf("图片太大了（上限 %d MB），请先压缩再上传",
				service.MaxImageBytes>>20))
			return
		}
		// 普通表单（非 multipart）会返回 ErrNotMultipart，交给下面的表单读取处理
	}

	f := newFormReader(r)
	product := &model.Product{
		ID:             id,
		SKU:            f.Required("sku", "产品编码"),
		Name:           f.Required("name", "产品名称"),
		NameEn:         f.Str("name_en"),
		Category:       f.Str("category"),
		Brand:          f.Str("brand"),
		Origin:         f.Str("origin"),
		Vintage:        f.Int("vintage", "年份 / 批次"),
		VolumeML:       f.Int("volume_ml", "容量"),
		ABV:            parseABV(f.Str("abv")),
		Unit:           f.Str("unit"),
		BottlesPerCase: f.Int("bottles_per_case", "每箱数量"),
		SalePrice:      f.Money("sale_price", "建议售价"),
		CostPrice:      f.Money("cost_price", "参考成本价"),
		Specs:          normalizeSpecs(f.Raw("specs")),
		LowStockQty:    f.Qty("low_stock_qty", "库存预警线"),
		SupplierID:     f.OptionalID("supplier_id"),
		Barcode:        f.Str("barcode"),
		ImageURL:       f.Str("image_url"),
		Notes:          f.Str("notes"),
		IsActive:       f.Bool("is_active"),
	}
	if product.Category == "" {
		product.Category = model.DefaultCategory
	}
	if product.Unit == "" {
		product.Unit = "瓶"
	}
	if product.BottlesPerCase <= 0 {
		product.BottlesPerCase = 1
	}
	if product.Vintage < 0 || product.Vintage > 2100 {
		f.AddError("年份 / 批次不正确")
	}
	if product.VolumeML < 0 || product.VolumeML > 100000 {
		f.AddError("容量不正确")
	}
	if product.ABV < 0 || product.ABV > 10000 {
		f.AddError("酒精度应在 0-100 之间")
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
		// 图片必须在保存产品之前处理：替换图片时服务要读数据库里的旧地址，
		// 才能把上一张文件删掉，不然上传目录会越攒越多。
		note, ok := s.applyProductImage(r, id)
		if imageTouched(r) {
			// 图片刚改过，用数据库里的最新地址，别被表单里的旧值覆盖
			if fresh, err := s.svc.Store.ProductByID(ctx, id); err == nil && fresh != nil {
				product.ImageURL = fresh.ImageURL
			}
		}
		if err := s.svc.Store.UpdateProduct(ctx, product); err != nil {
			s.fail(w, r, fallback, err)
			return
		}
		s.logAction(r, "修改产品", "product", &id, product.SKU+" "+product.Name)
		if !ok {
			s.fail(w, r, fallback, service.UserErrf("%s（产品其它信息已保存）", note))
			return
		}
		msg := "产品已更新"
		if note != "" {
			msg += "，" + note
		}
		s.ok(w, r, "/products/"+strconv.FormatInt(id, 10), msg)
		return
	}

	newID, err := s.svc.Store.CreateProduct(ctx, product)
	if err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	s.logAction(r, "新建产品", "product", &newID, product.SKU+" "+product.Name)
	// 新建时是先有产品 ID 才能存图片，没有旧文件需要清理
	note, ok := s.applyProductImage(r, newID)
	if !ok {
		s.fail(w, r, fallback, service.UserErrf("%s（产品已创建）", note))
		return
	}
	msg := "产品已创建，可以开始录入采购入库了"
	if note != "" {
		msg += "，" + note
	}
	s.ok(w, r, "/products/"+strconv.FormatInt(newID, 10), msg)
}

// imageTouched 本次提交是否动过图片（选了新文件或勾了删除）。
func imageTouched(r *http.Request) bool {
	if strings.TrimSpace(r.FormValue("remove_image")) != "" {
		return true
	}
	_, _, err := r.FormFile("image")
	return err == nil
}

// applyProductImage 处理产品表单里的图片：上传新图或删除旧图。
//
// 图片是跟产品一起提交的，所以这里在产品保存成功后调用。
// 返回一句可以拼进提示语的说明；本次没有图片操作时返回空串。
func (s *Server) applyProductImage(r *http.Request, productID int64) (string, bool) {
	if productID <= 0 {
		return "", true
	}
	ctx := r.Context()

	// 勾了「删除当前图片」就清掉。删除优先于上传，避免误覆盖。
	if strings.TrimSpace(r.FormValue("remove_image")) != "" {
		if err := s.svc.ClearProductImage(ctx, productID, userFrom(r)); err != nil {
			return "删除图片失败：" + userMessage(err), false
		}
		return "图片已删除", true
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		return "", true // 没选文件是正常情况
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, service.MaxImageBytes+1))
	if err != nil {
		return "读取图片失败，请重试", false
	}
	if len(data) == 0 {
		return "", true
	}
	if len(data) > service.MaxImageBytes {
		return fmt.Sprintf("图片太大了（上限 %d MB），请先压缩再上传", service.MaxImageBytes>>20), false
	}
	if _, err := s.svc.SaveProductImage(ctx, productID, header.Filename, data, userFrom(r)); err != nil {
		return "图片保存失败：" + userMessage(err), false
	}
	return "图片已保存（压缩后 " + service.ImageSizeText(len(data)) + "）", true
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

// ---------------------------------------------------------------- 产品图片

// handleProductImageUpload 接收产品图片上传。
//
// 手机拍的原图直接传上来即可，服务端会等比压缩后再存盘，
// 市集现场加载产品图才不至于卡。
func (s *Server) handleProductImageUpload(w http.ResponseWriter, r *http.Request) {
	productID := pathID(r, "id")
	back := "/products/" + itoa(productID) + "/edit"

	// 限制请求体，避免超大文件把内存吃满
	r.Body = http.MaxBytesReader(w, r.Body, service.MaxImageBytes+(1<<20))
	if err := r.ParseMultipartForm(service.MaxImageBytes + (1 << 20)); err != nil {
		s.setFlash(w, "error", "图片上传失败：文件过大或格式不正确")
		s.redirect(w, r, back)
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		s.setFlash(w, "error", "请选择要上传的图片")
		s.redirect(w, r, back)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, service.MaxImageBytes+1))
	if err != nil {
		s.setFlash(w, "error", "读取图片失败，请重试")
		s.redirect(w, r, back)
		return
	}

	if _, err := s.svc.SaveProductImage(r.Context(), productID, header.Filename, data, userFrom(r)); err != nil {
		s.setFlash(w, "error", userMessage(err))
		s.redirect(w, r, back)
		return
	}
	s.logAction(r, "上传产品图片", "product", &productID, header.Filename)
	s.setFlash(w, "success", "图片已保存（已压缩到长边 1280，原图 "+service.ImageSizeText(len(data))+"）")
	s.redirect(w, r, back)
}

// handleProductImageDelete 删除产品图片。
func (s *Server) handleProductImageDelete(w http.ResponseWriter, r *http.Request) {
	productID := pathID(r, "id")
	if err := s.svc.ClearProductImage(r.Context(), productID, userFrom(r)); err != nil {
		s.setFlash(w, "error", userMessage(err))
		s.redirect(w, r, "/products/"+itoa(productID)+"/edit")
		return
	}
	s.setFlash(w, "success", "图片已删除")
	s.redirect(w, r, "/products/"+itoa(productID)+"/edit")
}

// parseABV 把「11.5」「11.5%」这类输入转成百分之一为单位的整数。
func parseABV(raw string) int {
	v := strings.TrimSpace(raw)
	v = strings.TrimSuffix(v, "%")
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return -1 // 交给上层报错
	}
	return int(f*100 + 0.5)
}

// normalizeSpecs 规整规格参数文本：去掉空行、统一换行。
func normalizeSpecs(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

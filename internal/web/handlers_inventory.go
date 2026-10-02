package web

import (
	"net/http"
	"net/url"
	"strconv"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// ---------------------------------------------------------------- 库存总览

func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	filter := store.ProductFilter{
		Keyword:         f.Str("q"),
		Category:        f.Str("category"),
		LowStockOnly:    f.Str("low") == "1",
		IncludeInactive: f.Str("all") == "1",
		Sort:            f.Str("sort"),
	}
	products, err := s.svc.Store.ListProducts(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	categories, _ := s.svc.Store.CategoriesInUse(ctx)
	byCategory, _ := s.svc.Store.StockByCategory(ctx)

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
	page := s.newPage(r, "库存查询", "inventory")
	page["Products"] = products
	page["Categories"] = categories
	page["CategoryStock"] = byCategory
	page["Keyword"] = filter.Keyword
	page["Category"] = filter.Category
	page["LowStockOnly"] = filter.LowStockOnly
	page["IncludeInactive"] = filter.IncludeInactive
	page["Sort"] = filter.Sort
	page["TotalQty"] = totalQty
	page["TotalValue"] = totalValue
	page["LowCount"] = lowCount
	page["Count"] = len(products)
	page["CanAdjust"] = canEdit(r, PermInventoryAdjust)
	if err := s.rnd.Render(w, "inventory/index", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 库存流水

const movementPageSize = 50

func (s *Server) handleMovements(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)

	pageNo := f.Int("page", "页码")
	if pageNo < 1 {
		pageNo = 1
	}
	filter := store.MovementFilter{
		ProductID: f.ID("product", "产品"),
		Reason:    f.Str("reason"),
		From:      f.Str("from"),
		To:        f.Str("to"),
		Keyword:   f.Str("q"),
		Direction: f.Str("direction"),
		Limit:     movementPageSize,
		Offset:    (pageNo - 1) * movementPageSize,
	}

	movements, err := s.svc.Store.ListMovements(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, err := s.svc.Store.CountMovements(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	options, err := s.productOptions(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	totalPages := (total + movementPageSize - 1) / movementPageSize
	if totalPages < 1 {
		totalPages = 1
	}

	noCache(w)
	page := s.newPage(r, "库存流水", "movements")
	page["Movements"] = movements
	page["Products"] = options
	page["ProductID"] = filter.ProductID
	page["Reason"] = filter.Reason
	page["Direction"] = filter.Direction
	page["From"] = filter.From
	page["To"] = filter.To
	page["Keyword"] = filter.Keyword
	page["ReasonOptions"] = model.MovementFilterOptions
	page["Total"] = total
	page["Page"] = pageNo
	page["PageSize"] = movementPageSize
	page["TotalPages"] = totalPages
	page["HasPrev"] = pageNo > 1
	page["HasNext"] = pageNo < totalPages
	page["PrevURL"] = withPage(r, pageNo-1)
	page["NextURL"] = withPage(r, pageNo+1)
	page["CSVURL"] = "/export/movements.csv?" + r.URL.RawQuery
	page["CanAdjust"] = canEdit(r, PermInventoryAdjust)
	if err := s.rnd.Render(w, "inventory/movements", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// withPage 在保留筛选条件的前提下生成分页链接。
func withPage(r *http.Request, page int) string {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		values = url.Values{}
	}
	if page < 1 {
		page = 1
	}
	values.Set("page", strconv.Itoa(page))
	return r.URL.Path + "?" + values.Encode()
}

// ---------------------------------------------------------------- 出入库登记

func (s *Server) handleAdjustForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	options, err := s.productOptions(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	movements, err := s.svc.Store.ListMovements(ctx, store.MovementFilter{
		RefType: "manual",
		Limit:   15,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	preselect, _ := strconv.ParseInt(r.URL.Query().Get("product"), 10, 64)

	noCache(w)
	page := s.newPage(r, "出入库登记", "adjust")
	page["Products"] = options
	page["Reasons"] = manualReasonOptions()
	page["RecentMovements"] = movements
	page["Today"] = store.Today()
	page["Preselect"] = preselect
	if err := s.rnd.Render(w, "inventory/adjust", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// manualReasonOptions 手工登记可选的原因。
func manualReasonOptions() []model.Option {
	return []model.Option{
		{Value: model.ReasonOpening, Label: "期初建账（开始用系统时的现有库存）"},
		{Value: model.ReasonAdjustIn, Label: "盘点调增（实物比账面多，填正数）"},
		{Value: model.ReasonAdjustOut, Label: "盘点调减（实物比账面少，填负数）"},
		{Value: model.ReasonReturnIn, Label: "退货入库（客户退回）"},
		{Value: model.ReasonMarketLoss, Label: "破损 / 损耗"},
		{Value: model.ReasonMarketGift, Label: "赠送 / 公关用酒"},
	}
}

func (s *Server) handleAdjustSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)

	qty := f.Qty("qty", "数量")
	in := service.AdjustInput{
		ProductID:  f.ID("product_id", "产品"),
		Qty:        qty,
		UnitCost:   f.Money("unit_cost", "单位成本"),
		OccurredOn: f.Str("occurred_on"),
		Reason:     f.Str("reason"),
		Note:       f.Str("note"),
	}
	if in.ProductID <= 0 {
		f.AddError("请选择产品")
	}
	if qty == 0 {
		f.AddError("请填写数量（入库为正数、出库为负数）")
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, "/inventory/adjust", err)
		return
	}
	if err := s.svc.AdjustStock(ctx, in, userFrom(r)); err != nil {
		s.fail(w, r, "/inventory/adjust", err)
		return
	}

	product, _ := s.svc.Store.ProductByID(ctx, in.ProductID)
	name := "产品"
	unit := ""
	if product != nil {
		name = product.Name
		unit = product.Unit
	}
	s.ok(w, r, "/inventory/adjust",
		model.ReasonLabel(in.Reason)+"："+name+" "+qty.String()+unit+"，当前库存已更新")
}

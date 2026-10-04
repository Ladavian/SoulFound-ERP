package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

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
	// 本月"销售出库"（市集之外的直销）汇总
	firstOfMonth := time.Now().Format("2006-01") + "-01"
	if summary, err := s.svc.Store.DirectSaleSummaryBetween(ctx, firstOfMonth, store.Today()); err == nil {
		page["DirectSales"] = summary
	}
	// 出库构成：市集之外的出库也要看得见
	if outbound, err := s.svc.Store.OutboundByReasonBetween(ctx, firstOfMonth, store.Today()); err == nil {
		page["Outbound"] = outbound
	}
	page["MonthStart"] = firstOfMonth
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
	page["NeedsScanner"] = true // 可以扫码选产品
	page["Products"] = options
	page["ReasonGroups"] = []ReasonGroup{
		{Label: "入库", Options: model.DirectionInReasons},
		{Label: "出库", Options: model.DirectionOutReasons},
	}
	page["RecentMovements"] = movements
	customers, _ := s.svc.Store.ListCustomers(r.Context(), "", false)
	custOpts := make([]CustomerOption, 0, len(customers))
	for _, c := range customers {
		custOpts = append(custOpts, CustomerOption{ID: c.ID, Name: c.Name})
	}
	page["Customers"] = custOpts
	page["Today"] = store.Today()
	page["Preselect"] = preselect
	// 选择产品时左侧弹出图片，方便核对拿到的是不是同一款酒
	imgs := map[string]string{}
	for _, o := range options {
		if o.Image != "" {
			imgs[itoa(o.ID)] = o.Image
		}
	}
	page["ImageMap"] = imgs
	if err := s.rnd.Render(w, "inventory/adjust", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// manualReasonOptions 手工登记可选的原因（按入库/出库分组）。
func manualReasonOptions() []model.Option {
	out := make([]model.Option, 0, len(model.DirectionInReasons)+len(model.DirectionOutReasons))
	out = append(out, model.DirectionInReasons...)
	out = append(out, model.DirectionOutReasons...)
	return out
}

func (s *Server) handleAdjustSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)

	// 界面只让用户选「入库 / 出库」并填正数，方向在这里转成正负号。
	direction := f.Str("direction")
	qty := f.Qty("qty", "数量")

	// signedQty 最终写库的数量（出库为负）。
	// 兼容老链接：没有 direction 时按数量自带的符号判断，
	// 此时负数直接沿用，不再二次取反。
	explicit := direction == model.DirectionIn || direction == model.DirectionOut
	signedQty := qty
	if !explicit {
		if qty < 0 {
			direction = model.DirectionOut
			signedQty = qty
		} else {
			direction = model.DirectionIn
		}
	} else if direction == model.DirectionOut {
		signedQty = -qty
	}

	in := service.AdjustInput{
		ProductID:  f.ID("product_id", "产品"),
		UnitCost:   f.Money("unit_cost", "单位成本"),
		OccurredOn: f.Str("occurred_on"),
		Reason:     f.Str("reason"),
		Note:       f.Str("note"),
		SalePrice:  f.Money("sale_price", "售价"),
		CustomerID: f.OptionalID("customer_id"),
	}

	if in.ProductID <= 0 {
		f.AddError("请选择产品")
	}
	if qty == 0 {
		f.AddError("请填写数量")
	} else if explicit && qty < 0 {
		f.AddError("数量请填正数，用上面的「入库 / 出库」选择方向")
	}
	if direction != model.DirectionIn && direction != model.DirectionOut {
		f.AddError("请选择入库还是出库")
	}

	// 原因要与方向匹配，避免「期初建账」却做成出库
	if want := model.ReasonDirection(in.Reason); want != "" && want != direction {
		f.AddError("所选类型与方向不一致，请重新选择")
	}
	in.Qty = signedQty

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
	verb := "入库"
	if direction == model.DirectionOut {
		verb = "出库"
	}
	// 出库时数量已转成负数，提示里还原成正数更好读
	shown := signedQty
	if shown < 0 {
		shown = -shown
	}
	s.ok(w, r, "/inventory/adjust",
		verb+"（"+model.ReasonLabel(in.Reason)+"）："+name+" "+shown.String()+unit+"，当前库存已更新")
}

// ReasonGroup 出入库类型的一组选项。
type ReasonGroup struct {
	Label   string
	Options []model.Option
}

// CustomerOption 下拉用的客户精简数据。
type CustomerOption struct {
	ID   int64
	Name string
}

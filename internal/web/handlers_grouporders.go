package web

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// groupOrderPageSize 列表每页条数。
const groupOrderPageSize = 30

// GroupItemOption 团单明细里可选的产品。
type GroupItemOption struct {
	ID    int64   `json:"id"`
	Label string  `json:"label"`
	Name  string  `json:"name"`
	SKU   string  `json:"sku"`
	Unit  string  `json:"unit"`
	Price float64 `json:"price"`
}

func (s *Server) groupItemOptions(r *http.Request) ([]GroupItemOption, error) {
	products, err := s.svc.Store.ListProducts(r.Context(), store.ProductFilter{Sort: "name"})
	if err != nil {
		return nil, err
	}
	out := make([]GroupItemOption, 0, len(products))
	for _, p := range products {
		spec := ""
		if p.VolumeML > 0 {
			spec = " " + strconv.Itoa(p.VolumeML) + "ml"
		}
		out = append(out, GroupItemOption{
			ID:    p.ID,
			Label: p.SKU + " · " + p.Name + spec,
			Name:  p.Name,
			SKU:   p.SKU,
			Unit:  p.Unit,
			Price: p.SalePrice.Float(),
		})
	}
	return out, nil
}

// ---------------------------------------------------------------- 列表

func (s *Server) handleGroupOrderList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	pageNo := atoiDefault(f.Str("page"), 1)
	if pageNo < 1 {
		pageNo = 1
	}
	filter := store.GroupOrderFilter{
		Keyword: f.Str("q"),
		Status:  f.Str("status"),
		From:    f.Str("from"),
		To:      f.Str("to"),
		Sort:    f.Str("sort"),
		Limit:   groupOrderPageSize,
		Offset:  (pageNo - 1) * groupOrderPageSize,
	}

	orders, err := s.svc.Store.ListGroupOrders(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, err := s.svc.Store.CountGroupOrders(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 列表顶部汇总：跟着当前筛选走，取消的不计入金额
	var summary model.GroupOrderSummary
	for _, o := range orders {
		if o.Status == model.GroupCancelled {
			continue
		}
		summary.Count++
		summary.Qty += o.TotalQty()
		summary.Amount += o.Total()
	}

	totalPages := (total + groupOrderPageSize - 1) / groupOrderPageSize
	if totalPages < 1 {
		totalPages = 1
	}

	noCache(w)
	page := s.newPage(r, "线下团单", "group-orders")
	page["Orders"] = orders
	page["Summary"] = summary
	page["Keyword"] = filter.Keyword
	page["Status"] = filter.Status
	page["From"] = filter.From
	page["To"] = filter.To
	page["Sort"] = filter.Sort
	page["StatusOptions"] = model.GroupStatusOptions
	page["Total"] = total
	page["Page"] = pageNo
	page["TotalPages"] = totalPages
	page["HasPrev"] = pageNo > 1
	page["HasNext"] = pageNo < totalPages
	page["PrevURL"] = groupWithPage(r, pageNo-1)
	page["NextURL"] = groupWithPage(r, pageNo+1)
	page["CanManage"] = canEdit(r, PermGroupManage)
	if err := s.rnd.Render(w, "grouporders/list", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func groupWithPage(r *http.Request, pageNo int) string {
	values := r.URL.Query()
	values.Set("page", strconv.Itoa(pageNo))
	return r.URL.Path + "?" + values.Encode()
}

// ---------------------------------------------------------------- 表单

func (s *Server) handleGroupOrderForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	isNew := id == 0

	order := &model.GroupOrder{
		Status:    model.GroupDraft,
		OrderDate: store.Today(),
		Warehouse: "大仓",
		Items:     []model.GroupOrderItem{{Qty: model.QtyFromInt(1)}},
	}
	if !isNew {
		var err error
		order, err = s.svc.Store.GroupOrderByID(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if order == nil {
			s.notFound(w, r, "团单不存在或已被删除")
			return
		}
	}

	items, err := s.groupItemOptions(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	customers, _ := s.svc.Store.ListCustomers(ctx, "", false)

	action := "/group-orders/new"
	if !isNew {
		action = "/group-orders/" + strconv.FormatInt(id, 10) + "/edit"
	}

	noCache(w)
	page := s.newPage(r, "线下团单", "group-orders")
	page["Order"] = order
	page["IsNew"] = isNew
	page["FormAction"] = action
	page["Products"] = items
	page["ProductsJSON"] = template.JS(jsonEncode(items))
	page["Customers"] = customers
	page["StatusOptions"] = model.GroupStatusOptions
	if err := s.rnd.Render(w, "grouporders/form", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleGroupOrderSave(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	f := newFormReader(r)

	// 明细按列读取，行与行之间靠顺序对齐
	productIDs := f.List("product_id")
	names := f.List("product_name")
	skus := f.List("item_sku")
	qtys := f.List("qty")
	units := f.List("unit")
	prices := f.List("unit_price")
	notes := f.List("item_note")

	var items []service.GroupItemInput
	for i := range names {
		name := strings.TrimSpace(at(names, i))
		pid := parseOptionalID(at(productIDs, i))
		qty, err := model.ParseQty(at(qtys, i))
		if err != nil {
			f.AddError("第 %d 行数量格式不正确", i+1)
			continue
		}
		price, err := model.ParseMoney(at(prices, i))
		if err != nil {
			f.AddError("第 %d 行单价格式不正确", i+1)
			continue
		}
		// 整行都空的行直接忽略（表单里默认会有一行空行）
		if name == "" && pid == nil && qty == 0 {
			continue
		}
		// 选了产品但名称为空时，用产品名兜底
		if name == "" && pid != nil {
			if p, err := s.svc.Store.ProductByID(r.Context(), *pid); err == nil && p != nil {
				name = p.Name
				if at(skus, i) == "" {
					skus[i] = p.SKU
				}
			}
		}
		items = append(items, service.GroupItemInput{
			ProductID:   pid,
			ProductName: name,
			SKU:         strings.TrimSpace(at(skus, i)),
			Qty:         qty,
			Unit:        strings.TrimSpace(at(units, i)),
			UnitPrice:   price,
			Note:        strings.TrimSpace(at(notes, i)),
		})
	}

	in := service.GroupOrderInput{
		CustomerID:   f.OptionalID("customer_id"),
		CustomerName: f.Str("customer_name"),
		Contact:      f.Str("contact"),
		Phone:        f.Str("phone"),
		OrderDate:    f.Str("order_date"),
		ShipDate:     f.Str("ship_date"),
		Warehouse:    f.Str("warehouse"),
		Discount:     f.Money("discount", "优惠"),
		ExtraFee:     f.Money("extra_fee", "其它费用"),
		Note:         f.Str("note"),
		Items:        items,
	}

	fallback := "/group-orders"
	if id > 0 {
		fallback = "/group-orders/" + strconv.FormatInt(id, 10) + "/edit"
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, fallback, err)
		return
	}

	savedID, err := s.svc.SaveGroupOrder(r.Context(), id, in, userFrom(r))
	if err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	if id == 0 {
		s.ok(w, r, "/group-orders/"+strconv.FormatInt(savedID, 10), "团单已创建")
		return
	}
	s.ok(w, r, "/group-orders/"+strconv.FormatInt(savedID, 10), "团单已保存")
}

// ---------------------------------------------------------------- 详情与操作

func (s *Server) handleGroupOrderDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	order, err := s.svc.Store.GroupOrderByID(ctx, pathID(r, "id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if order == nil {
		s.notFound(w, r, "团单不存在或已被删除")
		return
	}

	noCache(w)
	page := s.newPage(r, "团单详情", "group-orders")
	page["Order"] = order
	page["StatusOptions"] = model.GroupStatusOptions
	page["CanManage"] = canEdit(r, PermGroupManage)
	if err := s.rnd.Render(w, "grouporders/detail", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleGroupOrderStatus(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	f := newFormReader(r)
	status := f.Str("status")
	if err := s.svc.SetGroupOrderStatus(r.Context(), id, status, userFrom(r)); err != nil {
		s.fail(w, r, "/group-orders/"+strconv.FormatInt(id, 10), err)
		return
	}
	s.ok(w, r, "/group-orders/"+strconv.FormatInt(id, 10),
		"状态已改为「"+model.GroupStatusLabel(status)+"」")
}

func (s *Server) handleGroupOrderDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if err := s.svc.DeleteGroupOrder(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, "/group-orders/"+strconv.FormatInt(id, 10), err)
		return
	}
	s.ok(w, r, "/group-orders", "团单已删除")
}

// atoiDefault 解析整数，失败时用默认值。
func atoiDefault(raw string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return n
}

// parseOptionalID 解析可空 ID。
func parseOptionalID(raw string) *int64 {
	v := strings.TrimSpace(raw)
	if v == "" || v == "0" {
		return nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return &id
}

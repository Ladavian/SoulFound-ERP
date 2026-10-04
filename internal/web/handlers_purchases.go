package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// ProductOption 供前端选择产品用的精简数据。
type ProductOption struct {
	ID    int64   `json:"id"`
	Label string  `json:"label"`
	Name  string  `json:"name"`
	SKU   string  `json:"sku"`
	Image string  `json:"image"`
	Price float64 `json:"price"`
	Cost  float64 `json:"cost"`
	Stock float64 `json:"stock"`
}

func (s *Server) productOptions(r *http.Request) ([]ProductOption, error) {
	products, err := s.svc.Store.ListProducts(r.Context(), store.ProductFilter{Sort: "name"})
	if err != nil {
		return nil, err
	}
	out := make([]ProductOption, 0, len(products))
	for _, p := range products {
		// 标签做成"名称 · 编码"：名称最好认，编码用来区分同名产品。
		// 容量只在名称里没写的时候才补——产品名通常已经带 375ml，
		// 之前无条件拼一次会变成"…375ml 375ml"这种啰嗦的重复。
		label := p.Name
		if p.VolumeML > 0 {
			volume := strconv.Itoa(p.VolumeML) + "ml"
			if !strings.Contains(label, volume) {
				label += " " + volume
			}
		}
		if p.SKU != "" {
			label += " · " + p.SKU
		}
		out = append(out, ProductOption{
			ID:    p.ID,
			Label: label,
			Name:  p.Name,
			SKU:   p.SKU,
			Image: p.ImageURL,
			Price: p.SalePrice.Float(),
			Cost:  p.CostPriceOrAvg().Float(),
			Stock: p.StockQty.Float(),
		})
	}
	return out, nil
}

// ---------------------------------------------------------------- 采购列表

func (s *Server) handlePurchaseList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	filter := store.PurchaseFilter{
		Keyword:    f.Str("q"),
		Status:     f.Str("status"),
		SupplierID: f.ID("supplier", "供应商"),
		From:       f.Str("from"),
		To:         f.Str("to"),
	}
	purchases, err := s.svc.Store.ListPurchases(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	suppliers, err := s.svc.Store.ListSuppliers(ctx, "", false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var (
		totalAmount model.Money
		totalQty    model.Qty
	)
	for _, p := range purchases {
		if p.Status == model.PurchaseConfirmed {
			totalAmount += p.TotalCost
			totalQty += p.TotalQty()
		}
	}

	noCache(w)
	page := s.newPage(r, "采购入库", "purchases")
	page["Purchases"] = purchases
	page["Suppliers"] = suppliers
	page["Keyword"] = filter.Keyword
	page["Status"] = filter.Status
	page["SupplierID"] = filter.SupplierID
	page["From"] = filter.From
	page["To"] = filter.To
	page["StatusOptions"] = model.PurchaseStatusOptions
	page["TotalAmount"] = totalAmount
	page["TotalQty"] = totalQty
	page["Count"] = len(purchases)
	page["CanManage"] = canEdit(r, PermPurchaseManage)
	page["CanConfirm"] = canEdit(r, PermPurchaseConfirm)
	if err := s.rnd.Render(w, "purchases/list", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 采购表单

func (s *Server) handlePurchaseForm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	isNew := id == 0

	var (
		purchase *model.Purchase
		items    []model.PurchaseItem
	)

	if !isNew {
		var err error
		purchase, err = s.svc.Store.PurchaseByID(ctx, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if purchase == nil {
			s.notFound(w, r, "采购单不存在")
			return
		}
		if !purchase.IsDraft() {
			s.ok(w, r, "/purchases/"+strconv.FormatInt(id, 10), "已入库的采购单不能编辑，请先「撤销入库」")
			return
		}
		items = purchase.Items
	} else {
		purchase = &model.Purchase{
			PurchaseDate: store.Today(),
			Currency:     settingsFrom(r).Currency,
			AllocMethod:  model.AllocByAmount,
			Status:       model.PurchaseDraft,
		}
	}

	suppliers, err := s.svc.Store.ListSuppliers(ctx, "", false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	options, err := s.productOptions(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rowList := make([]map[string]any, 0, len(items))
	for _, it := range items {
		rowList = append(rowList, map[string]any{
			"productId": it.ProductID,
			"qty":       it.Qty.Float(),
			"unitPrice": it.UnitPrice.Float(),
			"note":      it.Note,
		})
	}
	if len(rowList) == 0 {
		rowList = append(rowList, map[string]any{"productId": 0, "qty": 0, "unitPrice": 0, "note": ""})
	}
	rowsJSON, _ := json.Marshal(rowList)
	optionsJSON, _ := json.Marshal(options)

	nextCode := "自动生成"
	if isNew {
		if code, err := s.svc.Store.PeekCode(ctx, "PO", "PO", time.Now()); err == nil {
			nextCode = code
		}
	}

	noCache(w)
	page := s.newPage(r, "采购入库", "purchases")
	page["Purchase"] = purchase
	page["Items"] = items
	page["IsNew"] = isNew
	page["Suppliers"] = suppliers
	page["AllocOptions"] = model.AllocMethodOptions
	page["NextCode"] = nextCode
	page["Today"] = store.Today()
	page["RowsJSON"] = template.JS(rowsJSON)
	page["ProductsJSON"] = template.JS(optionsJSON)
	page["FormAction"] = "/purchases/new"
	if !isNew {
		page["FormAction"] = "/purchases/" + strconv.FormatInt(id, 10) + "/edit"
	}
	if err := s.rnd.Render(w, "purchases/form", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePurchaseSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	f := newFormReader(r)

	in := service.PurchaseInput{
		ID:           id,
		SupplierID:   f.OptionalID("supplier_id"),
		PurchaseDate: f.Str("purchase_date"),
		AllocMethod:  f.Str("alloc_method"),
		Notes:        f.Str("notes"),
		ShippingCost: f.Money("shipping_cost", "运费"),
		TariffCost:   f.Money("tariff_cost", "关税"),
		OtherCost:    f.Money("other_cost", "其他费用"),
	}

	ids := f.List("product_id")
	qtys := f.List("qty")
	prices := f.List("unit_price")
	notes := f.List("note")
	for i, raw := range ids {
		pid, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || pid <= 0 {
			continue
		}
		qty, errQty := model.ParseQty(at(qtys, i))
		price, errPrice := model.ParseMoney(at(prices, i))
		if errQty != nil || errPrice != nil {
			f.AddError("第 %d 行的数量或单价格式不正确", i+1)
			continue
		}
		in.Items = append(in.Items, service.PurchaseItemInput{
			ProductID: pid,
			Qty:       qty,
			UnitPrice: price,
			Note:      at(notes, i),
		})
	}

	fallback := "/purchases"
	if id > 0 {
		fallback = "/purchases/" + strconv.FormatInt(id, 10)
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, fallback, err)
		return
	}

	newID, err := s.svc.SavePurchase(ctx, in, userFrom(r))
	if err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	target := "/purchases/" + strconv.FormatInt(newID, 10)
	msg := "采购单已保存，确认无误后点「确认入库」才会写入库存"
	if id > 0 {
		msg = "采购单已更新"
	}
	s.ok(w, r, target, msg)
}

// ---------------------------------------------------------------- 采购详情

func (s *Server) handlePurchaseDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := pathID(r, "id")
	purchase, err := s.svc.Store.PurchaseByID(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if purchase == nil {
		s.notFound(w, r, "采购单不存在")
		return
	}

	noCache(w)
	page := s.newPage(r, "采购单 "+purchase.Code, "purchases")
	page["Purchase"] = purchase
	page["CanManage"] = canEdit(r, PermPurchaseManage)
	page["CanConfirm"] = canEdit(r, PermPurchaseConfirm)
	page["ExtraTotal"] = purchase.ExtraCost()
	page["IsConfirmed"] = purchase.IsConfirmed()
	if err := s.rnd.Render(w, "purchases/detail", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handlePurchaseConfirm(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	target := "/purchases/" + strconv.FormatInt(id, 10)
	if err := s.svc.ConfirmPurchase(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, target, err)
		return
	}
	s.ok(w, r, target, "已确认入库，库存与移动加权平均成本已更新")
}

func (s *Server) handlePurchaseUnconfirm(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	target := "/purchases/" + strconv.FormatInt(id, 10)
	if err := s.svc.UnconfirmPurchase(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, target, err)
		return
	}
	s.ok(w, r, target, "已撤销入库，相关库存与成本已回滚")
}

func (s *Server) handlePurchaseVoid(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	target := "/purchases/" + strconv.FormatInt(id, 10)
	if err := s.svc.VoidPurchase(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, target, err)
		return
	}
	s.ok(w, r, target, "采购单已作废")
}

func (s *Server) handlePurchaseDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if err := s.svc.DeletePurchase(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, "/purchases/"+strconv.FormatInt(id, 10), err)
		return
	}
	s.ok(w, r, "/purchases", "采购单已删除")
}

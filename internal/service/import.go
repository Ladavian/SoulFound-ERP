package service

import (
	"context"
	"fmt"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// ImportResult 导入结果汇总。
type ImportResult struct {
	Total   int      // 读取到的数据行
	Created int      // 新建
	Updated int      // 更新（按编码/名称匹配到已有记录）
	Skipped int      // 跳过
	Errors  []string // 逐行问题，最多保留 50 条
}

func (r *ImportResult) fail(format string, args ...any) {
	r.Skipped++
	if len(r.Errors) < 50 {
		r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
	}
}

// ProductImportRow 产品导入的一行。
type ProductImportRow struct {
	SKU        string
	Barcode    string
	Name       string
	ShortName  string
	Category   string
	Brand      string
	Origin     string
	Unit       string
	CostPrice  string
	SalePrice  string
	LowStock   string
	Supplier   string
	OpeningQty string
	OpeningOn  string
	Notes      string
}

// ImportProducts 导入产品档案，并按需写入期初库存。
//
// 匹配规则：先按产品编码，再按条形码；都匹配不到才新建。
// 因此同一份文件重复导入是安全的——只会更新，不会重复建。
func (s *Service) ImportProducts(ctx context.Context, rows []ProductImportRow, user *model.User) (ImportResult, error) {
	var res ImportResult
	supplierCache := map[string]*int64{}

	for i, row := range rows {
		line := i + 2 // 表格里的实际行号（含表头）
		res.Total++

		name := strings.TrimSpace(row.Name)
		sku := strings.TrimSpace(row.SKU)
		if name == "" && sku == "" {
			res.Skipped++
			continue
		}
		if name == "" {
			res.fail("第 %d 行：有产品编码但没有产品名称", line)
			continue
		}
		if sku == "" {
			// 没给编码就用条形码或名称兜底，保证每条都有唯一标识
			sku = strings.TrimSpace(row.Barcode)
			if sku == "" {
				sku = strings.TrimSpace(row.ShortName)
			}
			if sku == "" {
				res.fail("第 %d 行：缺少产品编码与条形码，无法唯一标识「%s」", line, name)
				continue
			}
		}

		// 匹配规则：填了产品编码就只认编码。
		// 只有编码为空时才退回条形码——因为不同产品共用条形码是常见的录入情况，
		// 用条形码兜底会把它们错误地合并成一个产品。
		var (
			existing *model.Product
			err      error
		)
		if p, e := s.Store.ProductBySKU(ctx, sku); e == nil && p != nil {
			existing, err = p, nil
		} else if e != nil {
			err = e
		}
		if existing == nil && strings.TrimSpace(row.SKU) == "" && strings.TrimSpace(row.Barcode) != "" {
			existing, err = s.Store.ProductByBarcode(ctx, strings.TrimSpace(row.Barcode))
		}
		if err != nil {
			res.fail("第 %d 行：查询产品失败 %v", line, err)
			continue
		}

		product := &model.Product{}
		if existing != nil {
			product = existing
		}
		product.SKU = sku
		product.Name = name
		product.NameEn = strings.TrimSpace(row.ShortName) // 简称先放在别名里，便于搜索
		if cat := strings.TrimSpace(row.Category); cat != "" {
			product.Category = cat
		} else if product.Category == "" {
			product.Category = model.DefaultCategory
		}
		product.Brand = strings.TrimSpace(row.Brand)
		product.Origin = strings.TrimSpace(row.Origin)
		product.Unit = strings.TrimSpace(row.Unit)
		if product.Unit == "" {
			product.Unit = "瓶"
		}
		if product.BottlesPerCase <= 0 {
			product.BottlesPerCase = 1
		}
		if v, err := model.ParseMoney(row.CostPrice); err == nil {
			product.CostPrice = v
		}
		if v, err := model.ParseMoney(row.SalePrice); err == nil {
			product.SalePrice = v
		}
		if v, err := model.ParseQty(row.LowStock); err == nil {
			product.LowStockQty = v
		}
		if code := strings.TrimSpace(row.Barcode); code != "" {
			// 条形码有唯一约束，被别的产品占用时保留本行产品但清掉条码，
			// 并在结果里说明，避免整行导入失败。
			owner, err := s.Store.ProductByBarcode(ctx, code)
			switch {
			case err != nil:
				res.fail("第 %d 行：检查条形码失败 %v", line, err)
			case owner != nil && (existing == nil || owner.ID != existing.ID):
				res.fail("第 %d 行：「%s」的条形码 %s 已被产品「%s」占用，本行未写入条形码",
					line, name, code, owner.Name)
			default:
				product.Barcode = code
			}
		}
		product.Notes = strings.TrimSpace(row.Notes)
		product.IsActive = true

		if sup := strings.TrimSpace(row.Supplier); sup != "" {
			id, ok := supplierCache[sup]
			if !ok {
				found, err := s.supplierByName(ctx, sup)
				if err != nil {
					res.fail("第 %d 行：查询供应商失败 %v", line, err)
					continue
				}
				if found != nil {
					v := found.ID
					id = &v
				} else {
					nid, err := s.Store.CreateSupplier(ctx, &model.Supplier{Name: sup, IsActive: true})
					if err != nil {
						res.fail("第 %d 行：创建供应商「%s」失败 %v", line, sup, err)
						continue
					}
					id = &nid
				}
				supplierCache[sup] = id
			}
			product.SupplierID = id
		}

		var productID int64
		if existing != nil {
			if err := s.Store.UpdateProduct(ctx, product); err != nil {
				res.fail("第 %d 行：更新产品失败 %v", line, err)
				continue
			}
			productID = existing.ID
			res.Updated++
		} else {
			id, err := s.Store.CreateProduct(ctx, product)
			if err != nil {
				res.fail("第 %d 行：创建产品失败 %v", line, err)
				continue
			}
			productID = id
			res.Created++
		}

		// 期初库存：只在填了数字且当前没有库存记录时写入，避免重复导入叠加
		opening, err := model.ParseQty(row.OpeningQty)
		if err != nil {
			res.fail("第 %d 行：期初库存格式不正确", line)
			continue
		}
		if opening <= 0 {
			continue
		}
		counted, err := s.Store.CountMovements(ctx, store.MovementFilter{ProductID: productID})
		if err != nil {
			res.fail("第 %d 行：检查库存流水失败 %v", line, err)
			continue
		}
		if counted > 0 {
			// 已经有流水（例如先导了进货明细），就不再补期初，防止数量翻倍
			continue
		}
		cost, _ := model.ParseMoney(row.CostPrice)
		on := strings.TrimSpace(row.OpeningOn)
		if on == "" {
			on = store.Today()
		}
		if err := s.AdjustStock(ctx, AdjustInput{
			ProductID:  productID,
			Qty:        opening,
			UnitCost:   cost,
			OccurredOn: on,
			Reason:     model.ReasonOpening,
			Note:       "导入期初库存",
		}, user); err != nil {
			res.fail("第 %d 行：写入期初库存失败 %v", line, err)
		}
	}
	return res, nil
}

// PurchaseImportRow 进货导入的一行。
type PurchaseImportRow struct {
	Date        string
	Supplier    string
	SKU         string
	Product     string
	Qty         string
	UnitPrice   string
	Note        string
	ShippingFee string
	TariffFee   string
}

// ImportPurchases 导入进货明细：同一天同一供应商合并成一张采购单，直接确认入库。
func (s *Service) ImportPurchases(ctx context.Context, rows []PurchaseImportRow, user *model.User) (ImportResult, error) {
	var res ImportResult
	type key struct{ date, supplier string }
	groups := map[key][]PurchaseItemInput{}
	notes := map[key]string{}
	var order []key

	for i, row := range rows {
		line := i + 2
		res.Total++
		if strings.TrimSpace(row.Product) == "" && strings.TrimSpace(row.SKU) == "" {
			res.Skipped++
			continue
		}
		product, err := s.findProductForImport(ctx, row.SKU, row.Product)
		if err != nil {
			res.fail("第 %d 行：%v", line, err)
			continue
		}
		qty, err := model.ParseQty(row.Qty)
		if err != nil || qty <= 0 {
			res.fail("第 %d 行：数量不正确（%s）", line, row.Qty)
			continue
		}
		price, err := model.ParseMoney(row.UnitPrice)
		if err != nil {
			res.fail("第 %d 行：单价格式不正确（%s）", line, row.UnitPrice)
			continue
		}
		date := normalizeImportDate(row.Date)
		if date == "" {
			res.fail("第 %d 行：日期不正确（%s）", line, row.Date)
			continue
		}
		supplier := strings.TrimSpace(row.Supplier)
		if supplier == "" {
			supplier = "未指定供应商"
		}
		k := key{date, supplier}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], PurchaseItemInput{
			ProductID: product.ID, Qty: qty, UnitPrice: price, Note: strings.TrimSpace(row.Note),
		})
		if notes[k] == "" && (row.ShippingFee != "" || row.TariffFee != "") {
			notes[k] = "导入：" + row.ShippingFee + "/" + row.TariffFee
		}
	}

	for _, k := range order {
		items := groups[k]
		supplierID, err := s.ensureSupplier(ctx, k.supplier)
		if err != nil {
			res.fail("%s %s：%v", k.date, k.supplier, err)
			continue
		}
		shipping, _ := model.ParseMoney(notes[k])
		id, err := s.SavePurchase(ctx, PurchaseInput{
			SupplierID:   supplierID,
			PurchaseDate: k.date,
			Notes:        "由表格导入",
			Items:        items,
		}, user)
		if err != nil {
			res.fail("%s %s：保存采购单失败 %v", k.date, k.supplier, err)
			continue
		}
		// 分摊费用留空时按 0 处理；确认后才真正入库
		_ = shipping
		if err := s.ConfirmPurchase(ctx, id, user); err != nil {
			res.fail("%s %s：确认入库失败 %v", k.date, k.supplier, err)
			continue
		}
		res.Created++
	}
	return res, nil
}

// OutboundImportRow 出库导入的一行（市集流水与手工出库共用）。
type OutboundImportRow struct {
	Market  string // 市集名称；为空表示非市集的出库，走手工登记
	Date    string
	SKU     string
	Product string
	Kind    string // 销售/试饮/赠送/损耗，或手工出库原因
	Qty     string
	Price   string
	Note    string
}

// ImportOutbound 导入出库明细。
//
// 有市集名称的行按「市集 + 日期」归集，写成市集现场记录；
// 没有市集名称的行（调拨、货损等）走手工出库登记。
func (s *Service) ImportOutbound(ctx context.Context, rows []OutboundImportRow, user *model.User) (ImportResult, error) {
	var res ImportResult
	markets := map[string]*model.Market{}
	manual := map[string][]struct {
		product *model.Product
		qty     model.Qty
		reason  string
		date    string
		note    string
	}{}

	for i, row := range rows {
		line := i + 2
		res.Total++
		if strings.TrimSpace(row.Product) == "" && strings.TrimSpace(row.SKU) == "" {
			res.Skipped++
			continue
		}
		product, err := s.findProductForImport(ctx, row.SKU, row.Product)
		if err != nil {
			res.fail("第 %d 行：%v", line, err)
			continue
		}
		qty, err := model.ParseQty(row.Qty)
		if err != nil || qty <= 0 {
			res.fail("第 %d 行：数量不正确（%s）", line, row.Qty)
			continue
		}
		date := normalizeImportDate(row.Date)
		if date == "" {
			res.fail("第 %d 行：日期不正确（%s）", line, row.Date)
			continue
		}
		kind := normalizeOutboundKind(row.Kind)
		mktName := strings.TrimSpace(row.Market)

		// 没有市集名称的行（调拨、仓库货损等）走手工出库登记；
		// 有市集名称的才写成市集现场记录。
		if mktName == "" {
			key := date + "|" + kind
			manual[key] = append(manual[key], struct {
				product *model.Product
				qty     model.Qty
				reason  string
				date    string
				note    string
			}{product, qty, manualReasonForKind(kind), date, strings.TrimSpace(row.Note)})
			continue
		}
		mkt, err := s.ensureMarket(ctx, mktName, date, markets)
		if err != nil {
			res.fail("第 %d 行：%v", line, err)
			continue
		}
		price, _ := model.ParseMoney(row.Price)
		if _, err := s.AddMarketRecord(ctx, mkt.ID, MarketRecordInput{
			ProductID: product.ID,
			Kind:      kind,
			Qty:       qty,
			UnitPrice: price,
			Channel:   "manual",
			Note:      strings.TrimSpace(row.Note),
		}, user); err != nil {
			res.fail("第 %d 行：写入市集记录失败 %v", line, err)
			continue
		}
		res.Created++
	}

	// 手工出库
	for key, list := range manual {
		for _, it := range list {
			if err := s.AdjustStock(ctx, AdjustInput{
				ProductID:  it.product.ID,
				Qty:        -it.qty,
				OccurredOn: it.date,
				Reason:     it.reason,
				Note:       firstNonEmpty(it.note, "由表格导入 "+key),
			}, user); err != nil {
				res.fail("%s 出库失败：%v", it.product.Name, err)
				continue
			}
			res.Created++
		}
	}
	return res, nil
}

// findProductForImport 按编码或名称找产品（导入时两者都可能给）。
func (s *Service) findProductForImport(ctx context.Context, sku, name string) (*model.Product, error) {
	if code := strings.TrimSpace(sku); code != "" {
		if p, err := s.Store.ProductBySKU(ctx, code); err != nil {
			return nil, err
		} else if p != nil {
			return p, nil
		}
		if p, err := s.Store.ProductByBarcode(ctx, code); err != nil {
			return nil, err
		} else if p != nil {
			return p, nil
		}
	}
	if n := strings.TrimSpace(name); n != "" {
		list, err := s.Store.ListProducts(ctx, store.ProductFilter{Keyword: n, IncludeInactive: true})
		if err != nil {
			return nil, err
		}
		for i := range list {
			if strings.TrimSpace(list[i].Name) == n || strings.TrimSpace(list[i].NameEn) == n {
				return &list[i], nil
			}
		}
		if len(list) == 1 {
			return &list[0], nil
		}
		return nil, UserErrf("找不到产品「%s」，请先导入产品档案或核对名称", n)
	}
	return nil, UserErrf("缺少产品编码与名称")
}

func (s *Service) ensureSupplier(ctx context.Context, name string) (*int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	found, err := s.supplierByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if found != nil {
		id := found.ID
		return &id, nil
	}
	id, err := s.Store.CreateSupplier(ctx, &model.Supplier{Name: name, IsActive: true})
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// ensureMarket 按名称找市集，没有就建一个当天的。
func (s *Service) ensureMarket(ctx context.Context, name, date string, cache map[string]*model.Market) (*model.Market, error) {
	if m, ok := cache[name]; ok {
		return m, nil
	}
	list, err := s.Store.ListMarkets(ctx, store.MarketFilter{Keyword: name, Limit: 50})
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.TrimSpace(list[i].Name) == name {
			cache[name] = &list[i]
			return &list[i], nil
		}
	}
	id, err := s.SaveMarket(ctx, MarketInput{
		Name: name, StartDate: date, EndDate: date,
		Venue: name, Notes: "由表格导入",
	}, nil)
	if err != nil {
		return nil, err
	}
	m, err := s.Store.MarketByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, UserErrf("创建市集「%s」失败", name)
	}
	_ = s.SetMarketStatus(ctx, id, model.MarketOngoing, nil)
	cache[name] = m
	return m, nil
}

// normalizeOutboundKind 把表格里的中文类型映射成系统类型。
func normalizeOutboundKind(kind string) string {
	k := strings.TrimSpace(kind)
	switch {
	case strings.Contains(k, "销售"):
		return model.RecordSale
	case strings.Contains(k, "试饮"):
		return model.RecordTasting
	case strings.Contains(k, "赠送"), strings.Contains(k, "公关"):
		return model.RecordGift
	case strings.Contains(k, "损"), strings.Contains(k, "货损"):
		return model.RecordLoss
	case strings.Contains(k, "调拨"):
		return "transfer"
	case strings.Contains(k, "盘亏"):
		return "adjust_out"
	}
	// 默认当作销售
	return model.RecordSale
}

// manualReasonForKind 非市集出库对应的手工登记原因。
func manualReasonForKind(kind string) string {
	switch kind {
	case "transfer":
		return model.ReasonTransferOut
	case "adjust_out":
		return model.ReasonAdjustOut
	case model.RecordLoss:
		return model.ReasonMarketLoss // 仓库层面的破损损耗
	case model.RecordGift:
		return model.ReasonMarketGift
	}
	return model.ReasonDirectSale // 市集之外的销售
}

func firstNonEmpty(list ...string) string {
	for _, v := range list {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// normalizeImportDate 把 2026年8月7日 / 2026/8/7 / 2026-08-07 统一成 2026-08-07。
func normalizeImportDate(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	v = strings.ReplaceAll(v, "年", "-")
	v = strings.ReplaceAll(v, "月", "-")
	v = strings.ReplaceAll(v, "日", "")
	v = strings.ReplaceAll(v, "/", "-")
	v = strings.ReplaceAll(v, ".", "-")
	parts := strings.Split(v, "-")
	if len(parts) < 3 {
		return ""
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	y, m, d := parts[0], parts[1], parts[2]
	if len(y) == 2 {
		y = "20" + y
	}
	if len(m) == 1 {
		m = "0" + m
	}
	if len(d) == 1 {
		d = "0" + d
	}
	if len(y) != 4 || len(m) != 2 || len(d) != 2 {
		return ""
	}
	return y + "-" + m + "-" + d
}

// supplierByName 按名称精确查找供应商（不存在返回 nil）。
func (s *Service) supplierByName(ctx context.Context, name string) (*model.Supplier, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	list, err := s.Store.ListSuppliers(ctx, name, true)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if strings.TrimSpace(list[i].Name) == name {
			return &list[i], nil
		}
	}
	return nil, nil
}

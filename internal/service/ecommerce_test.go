package service

import (
	"context"
	"testing"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

func ecTestRows() []EcOrderRow {
	return []EcOrderRow{
		{OrderNo: "T1", SubOrderNo: "T1-1", Title: "威代尔冰酒200ml", EcProductID: "999",
			Qty: "2", UnitPrice: "429", PaidAmt: "798", PayableAmt: "858",
			ItemStatus: "交易成功", RefundStatus: "没有申请退款",
			CreatedAt: "2026-10-03 10:48:5", PaidAt: "2026-10-03 10:50:04"},
		{OrderNo: "T2", SubOrderNo: "T2-1", Title: "开瓶器", EcProductID: "888",
			Qty: "1", UnitPrice: "60", PaidAmt: "60",
			ItemStatus: "交易成功", RefundStatus: "没有申请退款",
			CreatedAt: "2026/10/04 09:00", SellerNote: "送礼品袋"},
		{OrderNo: "T3", SubOrderNo: "T3-1", Title: "威代尔冰酒200ml", EcProductID: "999",
			Qty: "1", UnitPrice: "429", PaidAmt: "0", RefundAmt: "429",
			ItemStatus: "交易关闭", RefundStatus: "退款成功",
			CreatedAt: "2026-10-05 11:00"},
	}
}

// TestImportEcOrders 平台订单导入：数量、时间归一化、重复导入不重复。
func TestImportEcOrders(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	summary, err := svc.ImportEcOrders(ctx, model.EcTaobao, ecTestRows(), admin)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if summary.Orders != 3 || summary.Items != 3 {
		t.Fatalf("应导入 3 单 3 行，实际 %d 单 %d 行", summary.Orders, summary.Items)
	}
	if summary.Matched != 0 || len(summary.UnmatchedEc) != 2 {
		t.Errorf("还没绑定产品时应全部未匹配且汇总 2 种商品，实际匹配 %d / 未匹配 %d 种",
			summary.Matched, len(summary.UnmatchedEc))
	}

	orders, err := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 3 {
		t.Fatalf("应有 3 张订单，实际 %d", len(orders))
	}
	// 时间要归一化，SQLite 的 date() 才能用来筛选
	for _, o := range orders {
		if o.OrderNo == "T1" && o.CreatedAt != "2026-10-03 10:48:05" {
			t.Errorf("创建时间应补零成 2026-10-03 10:48:05，实际 %q", o.CreatedAt)
		}
		if o.OrderNo == "T2" && o.CreatedAt != "2026-10-04 09:00:00" {
			t.Errorf("斜杠日期应归一化，实际 %q", o.CreatedAt)
		}
		if o.OrderNo == "T3" && o.RefundStatus != "退款成功" {
			t.Errorf("整单退款应识别成退款成功，实际 %q", o.RefundStatus)
		}
	}
	// 有效行的金额口径
	var paid, refund model.Money
	for _, o := range orders {
		for _, it := range o.Items {
			if !it.Counts() {
				continue
			}
			paid += it.NetPaid()
			refund += it.RefundAmount
		}
	}
	if paid != model.MustMoney("858") { // 798 + 60
		t.Errorf("有效行实收应为 858，实际 %s", paid)
	}
	if refund != 0 {
		t.Errorf("有效行不应有退款，实际 %s", refund)
	}

	// 重复导入同一份数据不应产生重复
	again, err := svc.ImportEcOrders(ctx, model.EcTaobao, ecTestRows(), admin)
	if err != nil {
		t.Fatalf("重复导入失败: %v", err)
	}
	if again.Orders != 0 || again.OrdersUpd != 3 {
		t.Errorf("重复导入应全部走更新，实际新建 %d 更新 %d", again.Orders, again.OrdersUpd)
	}
	after, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	if len(after) != 3 {
		t.Fatalf("重复导入后仍是 3 张订单，实际 %d", len(after))
	}
}

// TestBindEcLink 平台商品绑定：可绑多个、补齐历史行、冲突与解绑。
func TestBindEcLink(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	rows := ecTestRows()
	// 再加一个淘宝链接，验证一个产品能绑多个商品ID
	rows = append(rows, EcOrderRow{
		OrderNo: "T4", SubOrderNo: "T4-1", Title: "威代尔冰酒200ml（另一个链接）",
		EcProductID: "777", Qty: "1", UnitPrice: "429", PaidAmt: "399",
		ItemStatus: "交易成功", RefundStatus: "没有申请退款", CreatedAt: "2026-10-06 10:00",
	})
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, rows, admin); err != nil {
		t.Fatal(err)
	}
	product, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "EC-1", Name: "威代尔冰酒200ml", Unit: "瓶", IsActive: true, IsWine: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "EC-2", Name: "另一个产品", Unit: "瓶", IsActive: true, IsWine: true,
	})

	// 绑第一个商品ID：应顺带补齐 2 行历史明细（T1、T3）
	fixed, err := svc.BindEcLink(ctx, model.EcTaobao, "999", "", "威代尔链接A", product, admin)
	if err != nil {
		t.Fatalf("绑第一个链接失败: %v", err)
	}
	if fixed != 2 {
		t.Errorf("应补齐 2 行历史明细，实际 %d", fixed)
	}
	// 再绑第二个商品ID：一个产品两条绑定
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "777", "", "威代尔链接B", product, admin); err != nil {
		t.Fatalf("绑第二个链接失败: %v", err)
	}
	links, _ := svc.Store.ProductLinks(ctx, product)
	if len(links) != 2 {
		t.Fatalf("一个产品应能绑 2 条，实际 %d", len(links))
	}
	// 再绑一个别的平台的商品ID
	if _, err := svc.BindEcLink(ctx, model.EcDouyin, "DY-001", "", "抖音链接", product, admin); err != nil {
		t.Fatalf("绑抖音商品失败: %v", err)
	}
	links, _ = svc.Store.ProductLinks(ctx, product)
	if len(links) != 3 {
		t.Fatalf("跨平台后应有 3 条绑定，实际 %d", len(links))
	}

	// 同一个平台商品ID 不能绑给第二个产品
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "999", "", "", other, admin); err == nil {
		t.Error("同一个平台商品ID 绑给第二个产品时应报错")
	}
	// 这两个商品ID 的订单行都应绑到这个产品上
	orders, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	bound := 0
	for _, o := range orders {
		for _, it := range o.Items {
			if it.EcProductID == "999" || it.EcProductID == "777" {
				if it.Matched() {
					bound++
				}
			}
		}
	}
	if bound != 3 {
		t.Errorf("两个商品ID 共 3 行应都绑上，实际 %d", bound)
	}

	// 解绑其中一条：只有它对应的行松绑，另一个商品ID 的行不受影响
	var target int64
	for _, l := range links {
		if l.EcProductID == "999" {
			target = l.ID
		}
	}
	if err := svc.UnbindEcLink(ctx, target, admin); err != nil {
		t.Fatalf("解绑失败: %v", err)
	}
	after, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	var stillMatched, freed int
	for _, o := range after {
		for _, it := range o.Items {
			if it.EcProductID == "999" && !it.Matched() {
				freed++
			}
			if it.EcProductID == "777" && it.Matched() {
				stillMatched++
			}
		}
	}
	if freed != 2 {
		t.Errorf("解绑后商品ID 999 的 2 行应松开，实际 %d", freed)
	}
	if stillMatched != 1 {
		t.Errorf("另一个商品ID 的绑定不应受影响，实际仍有 %d 行", stillMatched)
	}
}

// TestNonWineProduct 非酒类产品：不填年份容量也能正常保存与读取。
func TestNonWineProduct(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	id, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "ACC-1", Name: "多功能红酒开瓶器", Category: "酒具",
		Unit: "把", IsActive: true, IsWine: false,
		SalePrice: model.MustMoney("68"), EcCost: model.MustMoney("18"),
	})
	if err != nil {
		t.Fatalf("创建非酒类产品失败: %v", err)
	}
	got, _ := svc.Store.ProductByID(ctx, id)
	if got == nil {
		t.Fatal("应能读回产品")
	}
	if got.IsWine {
		t.Error("开瓶器不应被当成酒类")
	}
	if got.Vintage != 0 || got.VolumeML != 0 || got.ABV != 0 {
		t.Errorf("非酒类不该有年份/容量/酒精度，实际 %d/%d/%d", got.Vintage, got.VolumeML, got.ABV)
	}
	if got.Unit != "把" || got.Category != "酒具" {
		t.Errorf("单位与品类应保留，实际 %q / %q", got.Unit, got.Category)
	}
	// 酒类默认值要保持
	wine, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "WINE-1", Name: "威代尔冰酒", Category: "冰酒", Unit: "瓶",
		IsActive: true, IsWine: true, Vintage: 2019, VolumeML: 375, ABV: 1150,
	})
	wg, _ := svc.Store.ProductByID(ctx, wine)
	if !wg.IsWine || wg.Vintage != 2019 || wg.VolumeML != 375 || wg.ABV != 1150 {
		t.Errorf("酒类字段应完整保存，实际 %+v", wg)
	}
}

// TestEcCostIsSeparateFromAvgCost 电商成本与平均成本是两套口径。
func TestEcCostIsSeparateFromAvgCost(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	// 入库 10 瓶 @100：平均成本变成 100，电商成本必须保持 88 不变
	p, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "EC-COST", Name: "成本口径测试", Unit: "瓶", IsActive: true,
		SalePrice: model.MustMoney("398"), EcCost: model.MustMoney("88"),
	})
	if err != nil {
		t.Fatal(err)
	}
	stockIn(t, svc, admin, p, "10", "100")
	after, _ := svc.Store.ProductByID(ctx, p)
	if after.AvgCost != model.MustMoney("100") {
		t.Fatalf("平均成本应为 100，实际 %s", after.AvgCost)
	}
	if after.EcCost != model.MustMoney("88") {
		t.Fatalf("电商成本应保持 88 不受入库影响，实际 %s", after.EcCost)
	}
	if after.EcCostOrAvg() != model.MustMoney("88") {
		t.Errorf("算电商毛利应优先用电商成本 88，实际 %s", after.EcCostOrAvg())
	}

	// 订单行的成本只用电商成本
	item := model.EcOrderItem{
		Qty: model.MustQty("2"), PaidAmount: model.MustMoney("700"),
		EcCost: after.EcCostOrAvg(), ProductID: &p,
	}
	if item.ItemCost() != model.MustMoney("176") {
		t.Errorf("2 瓶电商成本应为 176，实际 %s", item.ItemCost())
	}
	if item.ItemProfit() != model.MustMoney("524") {
		t.Errorf("毛利应为 700-176=524，实际 %s", item.ItemProfit())
	}
}

// TestVirtualBundle 虚拟组套：成本按组成累加，可售数量按组成最小值。
func TestVirtualBundle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	makeProduct := func(sku, name, ecCost string) int64 {
		id, err := svc.Store.CreateProduct(ctx, &model.Product{
			SKU: sku, Name: name, Unit: "瓶", IsActive: true,
			EcCost: model.MustMoney(ecCost),
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	a := makeProduct("B-A", "A 酒", "100")
	b := makeProduct("B-B", "B 酒", "200")
	bundle := makeProduct("B-SET", "A+B 套装", "0")

	if err := svc.SaveBundles(ctx, bundle, []model.ProductBundle{
		{ComponentID: a, Qty: model.MustQty("1")},
		{ComponentID: b, Qty: model.MustQty("2")},
	}, admin); err != nil {
		t.Fatalf("保存组套失败: %v", err)
	}
	list, _ := svc.Store.BundlesOf(ctx, bundle)
	if len(list) != 2 {
		t.Fatalf("应有 2 个组成，实际 %d", len(list))
	}
	// 成本 = 100×1 + 200×2 = 500
	var cost model.Money
	for _, x := range list {
		cost += model.MulQty(x.Qty, x.CostBasis())
	}
	if cost != model.MustMoney("500") {
		t.Errorf("组套成本应为 500，实际 %s", cost)
	}

	// 不能把自己组进自己
	if err := svc.SaveBundles(ctx, bundle, []model.ProductBundle{
		{ComponentID: bundle, Qty: model.MustQty("1")},
	}, admin); err == nil {
		t.Error("把自己组进自己应报错")
	}
	// 组套不能嵌套
	nested := makeProduct("B-NEST", "嵌套组套", "0")
	if err := svc.SaveBundles(ctx, nested, []model.ProductBundle{
		{ComponentID: bundle, Qty: model.MustQty("1")},
	}, admin); err == nil {
		t.Error("把组套当组成产品应报错")
	}

	// 可售数量：A 有 10 瓶、B 有 6 瓶 → 1×A + 2×B 最多做 3 套
	stockIn(t, svc, admin, a, "10", "100")
	stockIn(t, svc, admin, b, "6", "200")
	canMake, err := svc.BundleStock(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if canMake != model.MustQty("3") {
		t.Errorf("可做套数应为 3，实际 %s", canMake)
	}
}

// TestPerProductNegativeStock 单个产品的负库存开关覆盖系统设置。
func TestPerProductNegativeStock(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	mk := func(sku, name string, neg int) int64 {
		id, err := svc.Store.CreateProduct(ctx, &model.Product{
			SKU: sku, Name: name, Unit: "瓶", IsActive: true, IsWine: true,
			AllowNegative: neg,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	follow := mk("N-FOLLOW", "跟随系统", model.NegativeFollowSystem)
	allow := mk("N-ALLOW", "允许负库存", model.NegativeAllow)
	forbid := mk("N-FORBID", "禁止负库存", model.NegativeForbid)

	// 策略要能存下来
	for id, want := range map[int64]int{
		follow: model.NegativeFollowSystem,
		allow:  model.NegativeAllow,
		forbid: model.NegativeForbid,
	} {
		got, _ := svc.Store.ProductByID(ctx, id)
		if got.AllowNegative != want {
			t.Errorf("产品 %s 的策略应为 %d，实际 %d", got.Name, want, got.AllowNegative)
		}
	}

	// 出库超过库存：系统不允许时
	//   跟随 → 拦住；允许 → 放过；禁止 → 拦住
	outs := func(id int64) error {
		return svc.AdjustStock(ctx, AdjustInput{
			ProductID: id, Reason: model.ReasonAdjustOut, Qty: model.MustQty("-3"),
			OccurredOn: "2026-03-01",
		}, admin)
	}
	if err := outs(follow); err == nil {
		t.Error("系统不允许负库存时，跟随的产品出库超量应被拦住")
	}
	if err := outs(allow); err != nil {
		t.Errorf("单独允许负库存的产品应放过，实际 %v", err)
	}
	if err := outs(forbid); err == nil {
		t.Error("单独禁止负库存的产品应被拦住")
	}
	allowed, _ := svc.Store.ProductByID(ctx, allow)
	if allowed.StockQty != model.MustQty("-3") {
		t.Errorf("允许负库存的产品库存应变成 -3，实际 %s", allowed.StockQty)
	}

	// 系统打开全局开关后：跟随的应放过，单独禁止的仍要拦住
	settings, err := svc.Store.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.AllowNegative = true
	if err := svc.Store.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := outs(follow); err != nil {
		t.Errorf("系统打开开关后，跟随的产品应放过，实际 %v", err)
	}
	if err := outs(forbid); err == nil {
		t.Error("系统打开开关后，单独禁止的产品仍应被拦住")
	}
}

// TestSameEcIDWithMultipleSKUs 同一个商品ID 下的不同规格分别绑定。
//
// 淘宝一个链接里会分规格（导出表的「商品属性」，
// 例如「商品规格:1瓶装」「商品规格:手拎袋」），
// 这些规格共用同一个商品ID，但属于不同产品。
// 所以绑定与匹配的键必须是「平台 + 商品ID + 规格」，
// 不能只用商品ID——否则第二个规格根本绑不上，或者会认错产品。
func TestSameEcIDWithMultipleSKUs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	// 同一个商品ID "8888" 三种规格：1瓶装、手拎袋、以及空规格
	rows := []EcOrderRow{
		{OrderNo: "S1", SubOrderNo: "S1-1", Title: "冰酒包装礼盒", EcProductID: "8888",
			EcSKU: "商品规格:1瓶装礼盒", Qty: "1", PaidAmt: "60",
			ItemStatus: "交易成功", RefundStatus: "没有申请退款", CreatedAt: "2026-10-01 10:00"},
		{OrderNo: "S2", SubOrderNo: "S2-1", Title: "冰酒包装礼盒", EcProductID: "8888",
			EcSKU: "商品规格:手拎袋", Qty: "2", PaidAmt: "30",
			ItemStatus: "交易成功", RefundStatus: "没有申请退款", CreatedAt: "2026-10-02 10:00"},
		{OrderNo: "S3", SubOrderNo: "S3-1", Title: "小冰甜375ML", EcProductID: "9999",
			EcSKU: "", Qty: "1", PaidAmt: "260",
			ItemStatus: "交易成功", RefundStatus: "没有申请退款", CreatedAt: "2026-10-03 10:00"},
	}
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, rows, admin); err != nil {
		t.Fatal(err)
	}
	// 规格文本要规范化（去掉「商品规格:」前缀）
	orders, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	specs := map[string]string{}
	for _, o := range orders {
		for _, it := range o.Items {
			specs[it.SubOrderNo] = it.EcSKU
		}
	}
	if specs["S1-1"] != "1瓶装礼盒" || specs["S2-1"] != "手拎袋" {
		t.Fatalf("规格文本应去掉「商品规格:」前缀，实际 %q / %q", specs["S1-1"], specs["S2-1"])
	}

	giftBox, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "GIFT-BOX", Name: "1瓶装礼盒", Unit: "盒", IsActive: true, IsWine: false})
	bag, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "BAG", Name: "手拎袋", Unit: "个", IsActive: true, IsWine: false})
	wine, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "WINE-X", Name: "小冰甜375ML", Unit: "瓶", IsActive: true, IsWine: true})

	// 同一商品ID 的两个规格绑到**不同产品**——这是关键：不能被判成冲突
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "8888", "商品规格:1瓶装礼盒", "", giftBox, admin); err != nil {
		t.Fatalf("绑第一个规格失败: %v", err)
	}
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "8888", "手拎袋", "", bag, admin); err != nil {
		t.Fatalf("同一商品ID 的第二个规格应能绑到别的产品，实际 %v", err)
	}
	// 换个商品ID 绑第三个
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "9999", "", "", wine, admin); err != nil {
		t.Fatalf("绑无规格商品失败: %v", err)
	}

	// 每个规格的订单行各自绑到对应产品
	after, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	got := map[string]int64{}
	for _, o := range after {
		for _, it := range o.Items {
			if it.ProductID != nil {
				got[it.SubOrderNo] = *it.ProductID
			}
		}
	}
	if got["S1-1"] != giftBox {
		t.Errorf("规格「1瓶装礼盒」的行应绑到礼盒产品，实际 %v", got["S1-1"])
	}
	if got["S2-1"] != bag {
		t.Errorf("规格「手拎袋」的行应绑到袋子产品，实际 %v", got["S2-1"])
	}
	if got["S3-1"] != wine {
		t.Errorf("无规格商品的行应绑到小冰甜，实际 %v", got["S3-1"])
	}

	// 同一商品ID 同一规格再绑给第二个产品要拦住
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "8888", "手拎袋", "", giftBox, admin); err == nil {
		t.Error("同一商品ID 同一规格绑给第二个产品时应报错")
	}

	// 歧义保护：给商品ID 8888 再加一个空规格绑定后，
	// 无规格的订单行不能猜，应该保持未匹配
	p3, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "GIFT-DEF", Name: "礼盒默认规格", Unit: "盒", IsActive: true, IsWine: false})
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "8888", "", "", p3, admin); err != nil {
		t.Fatalf("绑空规格失败: %v", err)
	}
	// 解绑手拎袋后，手拎袋那行应松绑，但礼盒那行不受影响
	links, _ := svc.Store.ProductLinks(ctx, bag)
	if len(links) != 1 {
		t.Fatalf("袋子产品应有 1 条绑定，实际 %d", len(links))
	}
	if err := svc.UnbindEcLink(ctx, links[0].ID, admin); err != nil {
		t.Fatal(err)
	}
	final, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	for _, o := range final {
		for _, it := range o.Items {
			if it.SubOrderNo == "S2-1" && it.Matched() {
				t.Error("解绑手拎袋后，它对应那行应松开")
			}
			if it.SubOrderNo == "S1-1" && !it.Matched() {
				t.Error("解绑手拎袋不应影响同一商品ID 另一个规格（1瓶装礼盒）的绑定")
			}
		}
	}
}

// TestNormalizeEcSKU 规格文本规范化。
func TestNormalizeEcSKU(t *testing.T) {
	cases := map[string]string{
		"商品规格:1瓶装":      "1瓶装",
		"商品规格：手拎袋":      "手拎袋",
		"1瓶装":           "1瓶装",
		"":              "",
		"  商品规格:1瓶装礼盒 ": "1瓶装礼盒",
		// 冒号前不是短标签时保持原样，避免把规格本身切坏
		"规格 说明:很长的一段文字": "规格 说明:很长的一段文字",
	}
	for in, want := range cases {
		if got := model.NormalizeEcSKU(in); got != want {
			t.Errorf("NormalizeEcSKU(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestOfflineSettledOrderExcluded 线下结算的订单要从平台结算里排除。
//
// 用户的实际做法：有些订单线下跟上游结，不走平台结算。
// 标记为线下后，这一单的货款/补贴/费用/数量/成本都要从结算里去掉，
// 否则同一单会被结两次。
//
// 关键坑：不能只把排除条件加在 LEFT JOIN 上——LEFT JOIN 条件不满足时
// 行仍然保留、金额照样汇总，等于没排除；必须写进 WHERE。
func TestOfflineSettledOrderExcluded(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "OFF-P1", Name: "线下结算测试品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("100"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "OFF-EC-1", "", "", pid, admin); err != nil {
		t.Fatal(err)
	}
	rows := []EcOrderRow{
		{OrderNo: "OFF-1", SubOrderNo: "OFF-1", Title: "线下结算测试品", EcProductID: "OFF-EC-1",
			Qty: "2", PaidAmt: "600", ItemStatus: "交易成功",
			RefundStatus: "没有申请退款", CreatedAt: "2026-09-10 10:00"},
		{OrderNo: "OFF-2", SubOrderNo: "OFF-2", Title: "线下结算测试品", EcProductID: "OFF-EC-1",
			Qty: "3", PaidAmt: "900", ItemStatus: "交易成功",
			RefundStatus: "没有申请退款", CreatedAt: "2026-09-11 10:00"},
	}
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, rows, admin); err != nil {
		t.Fatal(err)
	}
	items := []model.EcStatementItem{
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "OFF-1", SubOrderNo: "OFF-1", EcProductID: "OFF-EC-1",
			Qty: model.MustQty("2"), Amount: model.MustMoney("600")},
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "OFF-2", SubOrderNo: "OFF-2", EcProductID: "OFF-EC-1",
			Qty: model.MustQty("3"), Amount: model.MustMoney("900")},
	}
	if err := svc.ImportStatement(ctx, &model.EcStatement{
		Platform: model.EcTaobao, Period: "202609", Kind: model.StmtGoodsPayment,
		Source: model.StmtSourceBill, Direction: "income"}, items, admin); err != nil {
		t.Fatal(err)
	}

	settings, _ := svc.Store.Settings(ctx)
	before, err := svc.BuildSettlement(ctx, model.EcTaobao, "202609", settings)
	if err != nil {
		t.Fatal(err)
	}
	if before.Total.Goods != model.MustMoney("1500") || before.Total.Qty != model.MustQty("5") {
		t.Fatalf("排除前应为 1500 / 5 件，实际 %s / %s", before.Total.Goods, before.Total.Qty)
	}

	// 把 OFF-1（2 件 / 600）标为线下结算
	if err := svc.SetOrderSettleMode(ctx, model.EcTaobao, "OFF-1", model.SettleModeOffline, admin); err != nil {
		t.Fatal(err)
	}
	after, err := svc.BuildSettlement(ctx, model.EcTaobao, "202609", settings)
	if err != nil {
		t.Fatal(err)
	}
	if after.Total.Goods != model.MustMoney("900") {
		t.Errorf("排除后货款应为 900（1500−600），实际 %s", after.Total.Goods)
	}
	if after.Total.Qty != model.MustQty("3") {
		t.Errorf("排除后数量应为 3 件，实际 %s", after.Total.Qty)
	}
	if len(after.Orders) != 1 || after.Orders[0].OrderNo != "OFF-2" {
		t.Errorf("排除后只剩 OFF-2，实际 %d 单", len(after.Orders))
	}
	// 排除掉的订单要被统计出来，便于"账单合计 − 已排除 = 本次结算"对账
	sm, err := svc.Store.OfflineSettledOrders(ctx, model.EcTaobao, "202609")
	if err != nil {
		t.Fatal(err)
	}
	if sm.Orders != 1 || sm.Amount != model.MustMoney("600") {
		t.Errorf("已排除合计应为 1 单 / 600，实际 %d 单 / %s", sm.Orders, sm.Amount)
	}
	// 恢复
	if err := svc.SetOrderSettleMode(ctx, model.EcTaobao, "OFF-1", model.SettleModePlatform, admin); err != nil {
		t.Fatal(err)
	}
	back, _ := svc.BuildSettlement(ctx, model.EcTaobao, "202609", settings)
	if back.Total.Goods != model.MustMoney("1500") {
		t.Errorf("恢复后货款应回到 1500，实际 %s", back.Total.Goods)
	}
}

// TestEcommerceBillingDoesNotTouchStock 电商对账不碰库存。
//
// 用户明确：电商这一块只是在 ERP 里对平台账单做自动对账，
// 只读取产品档案里的「电商成本」，不关联库存数量；
// 发货在别的系统里做，所以导入账单/订单都不该动库存。
//
// 这条测试把这个边界钉住：以后谁不小心在电商路径里加了库存联动，
// 会立刻在这里失败。
func TestEcommerceBillingDoesNotTouchStock(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "EC-NOSTOCK", Name: "电商对账测试品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("140"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 入库 50 瓶（这笔是采购的库存，电商不该动它）
	if err := svc.AdjustStock(ctx, AdjustInput{
		ProductID: pid, Qty: model.MustQty("50"), Reason: model.ReasonOpening,
		OccurredOn: "2026-09-01", Note: "期初",
	}, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "EC-NS-1", "", "", pid, admin); err != nil {
		t.Fatal(err)
	}
	stockBefore := func() (model.Qty, int) {
		p, err := svc.Store.ProductByID(ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		moves, err := svc.Store.ListMovements(ctx, store.MovementFilter{ProductID: pid})
		if err != nil {
			t.Fatal(err)
		}
		return p.StockQty, len(moves)
	}
	q0, m0 := stockBefore()

	// 导入订单（卖出 5 件）
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, []EcOrderRow{{
		OrderNo: "EC-NS-O1", SubOrderNo: "EC-NS-O1", Title: "电商对账测试品",
		EcProductID: "EC-NS-1", Qty: "5", PaidAmt: "1500", ItemStatus: "交易成功",
		RefundStatus: "没有申请退款", CreatedAt: "2026-09-10 10:00",
	}}, admin); err != nil {
		t.Fatal(err)
	}
	// 导入账单（货款 + 服务费）
	for _, it := range []struct {
		kind, dir, amount string
		qty               string
	}{
		{model.StmtGoodsPayment, "income", "1500", "5"},
		{model.StmtBaseService, "expense", "9", ""},
	} {
		item := model.EcStatementItem{
			Period: "202609", Kind: it.kind, Direction: it.dir,
			OrderNo: "EC-NS-O1", SubOrderNo: "EC-NS-O1", EcProductID: "EC-NS-1",
			Amount: model.MustMoney(it.amount),
		}
		if it.qty != "" {
			item.Qty = model.MustQty(it.qty)
		}
		if err := svc.ImportStatement(ctx, &model.EcStatement{
			Platform: model.EcTaobao, Period: "202609", Kind: it.kind,
			Source: model.StmtSourceBill, Direction: it.dir}, []model.EcStatementItem{item}, admin); err != nil {
			t.Fatal(err)
		}
	}
	// 算一遍结算（最容易被顺手加上库存联动的地方）
	settings, _ := svc.Store.Settings(ctx)
	if _, err := svc.BuildSettlement(ctx, model.EcTaobao, "202609", settings); err != nil {
		t.Fatal(err)
	}

	q1, m1 := stockBefore()
	if q1 != q0 {
		t.Errorf("电商对账不该动库存：导入前 %s，导入后 %s", q0, q1)
	}
	if m1 != m0 {
		t.Errorf("电商对账不该产生库存流水：导入前 %d 笔，导入后 %d 笔", m0, m1)
	}
	if q1 != model.MustQty("50") {
		t.Errorf("库存应仍是期初的 50 瓶，实际 %s", q1)
	}
}

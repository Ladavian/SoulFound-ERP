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

// TestBindEcProduct 绑定电商商品ID：写回产品、补齐历史行、被重复绑定要拦住。
func TestBindEcProduct(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, ecTestRows(), admin); err != nil {
		t.Fatal(err)
	}
	product, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "EC-1", Name: "威代尔冰酒200ml", Unit: "瓶", IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "EC-2", Name: "另一个产品", Unit: "瓶", IsActive: true,
	})

	fixed, err := svc.BindEcProduct(ctx, "999", product, admin)
	if err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if fixed != 2 {
		t.Errorf("应顺带补齐 2 行历史明细，实际 %d", fixed)
	}
	got, _ := svc.Store.ProductByID(ctx, product)
	if got.EcProductID != "999" {
		t.Errorf("产品的电商商品ID 应写成 999，实际 %q", got.EcProductID)
	}
	// 已绑定的行要能查出成本来源
	orders, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	bound := 0
	for _, o := range orders {
		for _, it := range o.Items {
			if it.EcProductID == "999" && it.Matched() {
				bound++
			}
		}
	}
	if bound != 2 {
		t.Errorf("商品ID 999 应有 2 行绑定上，实际 %d", bound)
	}

	// 同一个电商商品ID 不能再绑给别的产品
	if _, err := svc.BindEcProduct(ctx, "999", other, admin); err == nil {
		t.Error("同一个电商商品ID 绑给第二个产品时应报错")
	}

	// 解绑后订单明细也应跟着松绑
	if err := svc.UnbindEcProduct(ctx, product, admin); err != nil {
		t.Fatalf("解绑失败: %v", err)
	}
	got2, _ := svc.Store.ProductByID(ctx, product)
	if got2.EcProductID != "" {
		t.Errorf("解绑后产品不应还带着电商商品ID，实际 %q", got2.EcProductID)
	}
	left, _ := svc.Store.CountEcUnmatched(ctx, model.EcTaobao)
	if left != 3 {
		t.Errorf("解绑后 3 行都应回到未匹配，实际 %d", left)
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

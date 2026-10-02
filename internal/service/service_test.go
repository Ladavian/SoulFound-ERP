package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"icewine-erp/internal/config"
	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.sqlite3"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	cfg := &config.Config{
		AppName:        "测试 ERP",
		Version:        "test",
		DataDir:        dir,
		DBPath:         filepath.Join(dir, "test.sqlite3"),
		SecretKey:      []byte("test-secret-key"),
		SessionCookie:  "test_session",
		SessionTTL:     time.Hour,
		Currency:       "CNY",
		CurrencySymbol: "¥",
		DefaultLowQty:  model.QtyFromInt(6),
		AdminUsername:  "admin",
		AdminPassword:  "admin123",
		AdminName:      "测试管理员",
		Location:       time.Local,
	}
	svc := New(st, cfg)
	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	return svc
}

func adminUser(t *testing.T, svc *Service) *model.User {
	t.Helper()
	u, err := svc.Store.UserByUsername(context.Background(), "admin")
	if err != nil || u == nil {
		t.Fatalf("读取管理员失败: %v", err)
	}
	return u
}

func TestMoneyParsingAndRounding(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"35", "35.00"},
		{"35.5", "35.50"},
		{"¥1,234.56", "1,234.56"},
		{"0.125", "0.13"},
		{"-8.005", "-8.01"},
		{"", "0.00"},
		{"¥99.999", "100.00"},
	}
	for _, c := range cases {
		got, err := model.ParseMoney(c.in)
		if err != nil {
			t.Fatalf("解析 %q 出错: %v", c.in, err)
		}
		if got.String() != c.want {
			t.Errorf("ParseMoney(%q) = %s，期望 %s", c.in, got, c.want)
		}
	}

	// 数量解析
	qty, err := model.ParseQty("12.5")
	if err != nil {
		t.Fatal(err)
	}
	if qty.String() != "12.5" {
		t.Errorf("ParseQty(12.5) = %s", qty)
	}

	// 金额 × 数量
	if got := model.MulQty(model.MustQty("12"), model.MustMoney("35.50")); got.String() != "426.00" {
		t.Errorf("12 × 35.50 = %s，期望 426.00", got)
	}
}

func TestApplyMovementWeightedAverage(t *testing.T) {
	// 10 瓶 @ C$10.00
	qty, avg, value := model.ApplyMovement(0, 0, model.MustQty("10"), model.MustMoney("10"))
	if qty != model.MustQty("10") || avg != model.MustMoney("10") || value != model.MustMoney("100") {
		t.Fatalf("首次入库结果错误: qty=%s avg=%s value=%s", qty, avg, value)
	}
	// 再入 10 瓶 @ C$20.00 -> 平均 C$15.00
	qty, avg, value = model.ApplyMovement(qty, avg, model.MustQty("10"), model.MustMoney("20"))
	if avg != model.MustMoney("15") || value != model.MustMoney("300") {
		t.Fatalf("加权平均错误: avg=%s value=%s", avg, value)
	}
	// 出库 5 瓶，按平均成本
	qty, avg, value = model.ApplyMovement(qty, avg, model.MustQty("-5"), avg)
	if qty != model.MustQty("15") || avg != model.MustMoney("15") || value != model.MustMoney("225") {
		t.Fatalf("出库后结存错误: qty=%s avg=%s value=%s", qty, avg, value)
	}
	// 全部出清
	qty, _, value = model.ApplyMovement(qty, avg, model.MustQty("-15"), avg)
	if qty != 0 || value != 0 {
		t.Fatalf("清空后应为 0: qty=%s value=%s", qty, value)
	}
}

func TestPurchaseConfirmAndWeightedCost(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	productID, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "T-001", Name: "测试冰酒", Category: "冰酒", Unit: "瓶",
		BottlesPerCase: 6, SalePrice: model.MustMoney("100"),
		LowStockQty: model.QtyFromInt(2), IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 货款 10 × 40 = 400，运费 100 -> 到岸单价 50
	purchaseID, err := svc.SavePurchase(ctx, PurchaseInput{
		PurchaseDate: store.Today(),
		AllocMethod:  model.AllocByAmount,
		ShippingCost: model.MustMoney("100"),
		Items: []PurchaseItemInput{
			{ProductID: productID, Qty: model.MustQty("10"), UnitPrice: model.MustMoney("40")},
		},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPurchase(ctx, purchaseID, admin); err != nil {
		t.Fatal(err)
	}

	p, err := svc.Store.ProductByID(ctx, productID)
	if err != nil {
		t.Fatal(err)
	}
	if p.StockQty != model.MustQty("10") {
		t.Errorf("库存应为 10，实际 %s", p.StockQty)
	}
	if p.AvgCost != model.MustMoney("50") {
		t.Errorf("到岸平均成本应为 50，实际 %s", p.AvgCost)
	}
	if p.StockValue != model.MustMoney("500") {
		t.Errorf("库存价值应为 500，实际 %s", p.StockValue)
	}

	// 撤销入库后库存应回到 0
	if err := svc.UnconfirmPurchase(ctx, purchaseID, admin); err != nil {
		t.Fatal(err)
	}
	p, _ = svc.Store.ProductByID(ctx, productID)
	if p.StockQty != 0 || p.StockValue != 0 {
		t.Errorf("撤销入库后应清零，实际 qty=%s value=%s", p.StockQty, p.StockValue)
	}
	// 重新入库供后续测试
	if err := svc.ConfirmPurchase(ctx, purchaseID, admin); err != nil {
		t.Fatal(err)
	}
}

func TestMarketSettleProfitAndStock(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	productID, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "T-002", Name: "市集测试冰酒", Category: "冰酒", Unit: "瓶",
		BottlesPerCase: 6, SalePrice: model.MustMoney("100"),
		LowStockQty: model.QtyFromInt(2), IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	purchaseID, err := svc.SavePurchase(ctx, PurchaseInput{
		PurchaseDate: store.Today(),
		Items: []PurchaseItemInput{
			{ProductID: productID, Qty: model.MustQty("10"), UnitPrice: model.MustMoney("40")},
		},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmPurchase(ctx, purchaseID, admin); err != nil {
		t.Fatal(err)
	}

	marketID, err := svc.SaveMarket(ctx, MarketInput{
		Name: "测试市集", Venue: "测试场地", StartDate: store.Today(), EndDate: store.Today(),
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddMarketProduct(ctx, marketID, productID, admin); err != nil {
		t.Fatal(err)
	}
	m, err := svc.Store.MarketByID(ctx, marketID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 1 {
		t.Fatalf("市集应有 1 行明细，实际 %d", len(m.Items))
	}
	if m.Items[0].UnitPrice != model.MustMoney("100") {
		t.Errorf("明细单价应默认取售价 100，实际 %s", m.Items[0].UnitPrice)
	}

	// 试饮 2 杯、销售 3 瓶、赠送 1 瓶；含优惠 10；费用 50
	err = svc.UpdateMarketItemRow(ctx, marketID, m.Items[0].ID, MarketItemUpdate{
		CarriedQty:  model.MustQty("8"),
		TastingQty:  model.MustQty("2"),
		SoldQty:     model.MustQty("3"),
		GiftQty:     model.MustQty("1"),
		UnitPrice:   model.MustMoney("100"),
		DiscountAmt: model.MustMoney("10"),
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveMarketExpenses(ctx, marketID, []model.MarketExpense{
		{Category: model.ExpenseBooth, Amount: model.MustMoney("50"), Note: "摊位费"},
	}, admin); err != nil {
		t.Fatal(err)
	}

	// 结算前为估算值（成本取自当前平均成本 40）
	m, _ = svc.Store.MarketByID(ctx, marketID)
	totals := m.Totals()
	wantRevenue := model.MustMoney("290") // 3 × 100 - 10
	if totals.Revenue != wantRevenue {
		t.Errorf("销售额应为 %s，实际 %s", wantRevenue, totals.Revenue)
	}
	wantNet := model.MustMoney("290").Sub(model.MustMoney("120")).Sub(model.MustMoney("80")).Sub(model.MustMoney("40")).Sub(model.MustMoney("50"))
	if totals.NetProfit != wantNet {
		t.Errorf("净利润应为 %s，实际 %s", wantNet, totals.NetProfit)
	}

	if err := svc.SettleMarket(ctx, marketID, admin); err != nil {
		t.Fatal(err)
	}

	p, _ := svc.Store.ProductByID(ctx, productID)
	if p.StockQty != model.MustQty("4") { // 10 - 3 - 2 - 1
		t.Errorf("结算后库存应为 4，实际 %s", p.StockQty)
	}
	if p.AvgCost != model.MustMoney("40") {
		t.Errorf("出库不应改变平均成本，实际 %s", p.AvgCost)
	}
	if p.StockValue != model.MustMoney("160") {
		t.Errorf("结算后库存价值应为 160，实际 %s", p.StockValue)
	}

	m, _ = svc.Store.MarketByID(ctx, marketID)
	if !m.IsSettled() {
		t.Fatal("市集应为已结算状态")
	}
	if m.NetProfit != wantNet {
		t.Errorf("缓存的净利润应为 %s，实际 %s", wantNet, m.NetProfit)
	}
	if m.Items[0].UnitCost == nil || *m.Items[0].UnitCost != model.MustMoney("40") {
		t.Error("结算后应写入成本快照")
	}

	// 重复结算应被拒绝
	if err := svc.SettleMarket(ctx, marketID, admin); err == nil {
		t.Error("重复结算应返回错误")
	}

	// 撤销结算后库存与成本快照应还原
	if err := svc.UnsettleMarket(ctx, marketID, admin); err != nil {
		t.Fatal(err)
	}
	p, _ = svc.Store.ProductByID(ctx, productID)
	if p.StockQty != model.MustQty("10") || p.StockValue != model.MustMoney("400") {
		t.Errorf("撤销结算后应还原，实际 qty=%s value=%s", p.StockQty, p.StockValue)
	}
	m, _ = svc.Store.MarketByID(ctx, marketID)
	if m.Items[0].UnitCost != nil {
		t.Error("撤销结算后成本快照应清空")
	}
}

func TestInsufficientStockBlocks(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	productID, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "T-003", Name: "库存不足测试", Category: "冰酒", Unit: "瓶",
		SalePrice: model.MustMoney("80"), LowStockQty: 0, IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AdjustStock(ctx, AdjustInput{
		ProductID: productID, Qty: model.MustQty("2"), UnitCost: model.MustMoney("30"),
		Reason: model.ReasonOpening,
	}, admin); err != nil {
		t.Fatal(err)
	}
	err = svc.AdjustStock(ctx, AdjustInput{
		ProductID: productID, Qty: model.MustQty("-5"),
		Reason: model.ReasonAdjustOut,
	}, admin)
	if err == nil {
		t.Fatal("库存不足时应返回错误")
	}
	if _, ok := AsUserError(err); !ok {
		t.Errorf("应为用户可见错误，实际 %v", err)
	}
	p, _ := svc.Store.ProductByID(ctx, productID)
	if p.StockQty != model.MustQty("2") {
		t.Errorf("失败后库存不应变化，实际 %s", p.StockQty)
	}
}

func TestDemoSeedConsistency(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)
	if err := svc.seedDemo(ctx, admin); err != nil {
		t.Fatalf("演示数据写入失败: %v", err)
	}

	products, err := svc.Store.ListProducts(ctx, store.ProductFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(products) == 0 {
		t.Fatal("演示数据应包含产品")
	}
	for _, p := range products {
		want := model.MulQty(p.StockQty, p.AvgCost)
		if p.StockValue != want {
			t.Errorf("%s 库存价值不一致：记录 %s，应为 %s", p.SKU, p.StockValue, want)
		}
		if p.StockQty < 0 {
			t.Errorf("%s 出现负库存 %s", p.SKU, p.StockQty)
		}
	}

	markets, err := svc.Store.ListMarkets(ctx, store.MarketFilter{})
	if err != nil {
		t.Fatal(err)
	}
	settled := 0
	for _, m := range markets {
		if !m.IsSettled() {
			continue
		}
		settled++
		totals := m.Totals()
		if totals.NetProfit != m.NetProfit {
			t.Errorf("%s 缓存的净利润 %s 与重算结果 %s 不一致", m.Code, m.NetProfit, totals.NetProfit)
		}
	}
	if settled < 3 {
		t.Errorf("演示数据应有 3 场已结算市集，实际 %d", settled)
	}

	// 全部产品重算后应与缓存一致
	if _, err := svc.RebuildAllStock(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := svc.Store.ListProducts(ctx, store.ProductFilter{})
	for i, p := range after {
		if p.StockQty != products[i].StockQty {
			t.Errorf("%s 重算后库存变化：%s -> %s", p.SKU, products[i].StockQty, p.StockQty)
		}
	}
}

func TestReportAggregates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)
	if err := svc.seedDemo(ctx, admin); err != nil {
		t.Fatal(err)
	}
	today := fmtDate(todayDate())
	from := fmtDate(yearStart(todayDate()))

	rows, totals, err := svc.MarketSummary(ctx, from, today, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("市集汇总不应为空")
	}
	var revenue model.Money
	for _, r := range rows {
		revenue += r.Revenue
	}
	if revenue != totals.Revenue {
		t.Errorf("汇总销售额 %s 与明细合计 %s 不一致", totals.Revenue, revenue)
	}

	series, err := svc.MonthlySeries(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 12 {
		t.Errorf("应返回 12 个月数据点，实际 %d", len(series))
	}

	dash, err := svc.Dashboard(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	if dash.Stats.SKUCount == 0 {
		t.Error("首页应统计到产品数")
	}
}

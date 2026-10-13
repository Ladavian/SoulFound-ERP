package service

import (
	"context"
	"testing"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// TestSettlementCostNotDuplicated 供货成本只能按商品数量算一次。
//
// 一张订单在账期账单里会出现多行（货款、基础软件服务费、新享礼金…），
// 它们都会关联到同一张订单。成本必须按「订单数量 × 供货价」算一次，
// 不能因为账单行多就重复累加——用户就是看到成本偏大才发现这个问题的。
func TestSettlementCostNotDuplicated(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	// 一款酒，供货价 140
	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "W-1", Name: "维达尔200ML", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("140"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 绑定商品ID 并让订单行挂上产品
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "EC-1", "", "", pid, admin); err != nil {
		t.Fatal(err)
	}
	rows := []EcOrderRow{{
		OrderNo: "O-1", SubOrderNo: "O-1", Title: "维达尔200ML", EcProductID: "EC-1",
		Qty: "2", UnitPrice: "429", PaidAmt: "572",
		ItemStatus: "交易成功", RefundStatus: "没有申请退款", CreatedAt: "2026-08-11 10:00",
	}}
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, rows, admin); err != nil {
		t.Fatal(err)
	}

	// 账单：同一张订单 4 行 —— 货款 + 基础软件服务费 + 新享礼金（含垫支）+ 淘金币费
	items := []model.EcStatementItem{
		{Period: "202608", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "O-1", SubOrderNo: "O-1", EcProductID: "EC-1",
			Qty: model.MustQty("2"), Amount: model.MustMoney("572")},
		{Period: "202608", Kind: model.StmtBaseService, Direction: "expense",
			OrderNo: "O-1", SubOrderNo: "O-1", Qty: model.MustQty("1"), Amount: model.MustMoney("3.43")},
		{Period: "202608", Kind: model.StmtBrandGift, Direction: "expense",
			OrderNo: "O-1", SubOrderNo: "O-1", Qty: model.MustQty("1"),
			Amount: model.MustMoney("46.8"), Advance: model.MustMoney("40"), GrossAmount: model.MustMoney("86.8")},
		{Period: "202608", Kind: model.StmtCoinSubsidy, Direction: "income",
			OrderNo: "O-1", SubOrderNo: "O-1", Amount: model.MustMoney("5.58")},
	}
	// 每类账单各自导入（不能把不同类的行混进同一份账单）
	for _, kind := range []string{model.StmtGoodsPayment, model.StmtBaseService,
		model.StmtBrandGift, model.StmtCoinSubsidy} {
		var sub []model.EcStatementItem
		for _, it := range items {
			if it.Kind == kind {
				sub = append(sub, it)
			}
		}
		if len(sub) == 0 {
			continue
		}
		if err := svc.ImportStatement(ctx, &model.EcStatement{
			Platform: model.EcTaobao, Period: "202608", Kind: kind,
			Direction: model.StmtDirection(kind), RowCount: len(sub)}, sub, admin); err != nil {
			t.Fatalf("导入 %s 失败: %v", kind, err)
		}
	}

	settings, _ := svc.Store.Settings(ctx)
	rep, err := svc.BuildSettlement(ctx, model.EcTaobao, "202608", settings)
	if err != nil {
		t.Fatal(err)
	}
	total := rep.Total

	// 成本只算一次：2 瓶 × 140 = 280
	if total.Cost != model.MustMoney("280") {
		t.Errorf("供货成本应为 280（2 瓶 × 140），实际 %s："+
			"账单里同一订单有多行，成本不能按行重复累加", total.Cost)
	}
	if total.Qty != model.MustQty("2") {
		t.Errorf("数量应为 2，实际 %s", total.Qty)
	}
	// 收入 = 货款 572 + 补贴 5.58 − 垫支 40
	if want := model.MustMoney("537.58"); total.Revenue != want {
		t.Errorf("销售收入应为 %s，实际 %s", want, total.Revenue)
	}
	// 销项 = 537.58/1.13×0.13 = 61.85；进项 = 280/1.13×0.13 = 32.21
	// 平台费抵扣 = (3.43+46.8)/1.06×0.06 = 2.84
	if got := total.OutputVat(rep.Rates); got.Float() < 61.8 || got.Float() > 61.9 {
		t.Errorf("销项税应约 61.85，实际 %.2f", got.Float())
	}
	if got := total.InputVat(rep.Rates); got.Float() < 32.2 || got.Float() > 32.3 {
		t.Errorf("进项税应约 32.21，实际 %.2f", got.Float())
	}
	// 明细里的成本也要一致
	var sum model.Money
	for _, o := range rep.Orders {
		sum += o.S.Cost
	}
	if sum != total.Cost {
		t.Errorf("订单明细成本合计 %s 应与总成本 %s 一致", sum, total.Cost)
	}
	// 按商品汇总的成本也要一致
	var psum model.Money
	// 账单里商品是 2 瓶，商品汇总口径同样是 2 瓶 × 140
	for _, p := range rep.Products {
		psum += p.S.Cost
	}
	if psum != model.MustMoney("280") {
		t.Errorf("按商品汇总的成本应为 280，实际 %s", psum)
	}
}

// TestMonthlyBillWinsOverReconcile 月度账单为准，对账中心只补缺的费用项。
//
// 京东一个账期有两份来源：
//
//	月度账单   平台出的月度账单，订单与数量以它为准
//	对账中心   一单一单的明细，项目更全（商品保险服务费、运费保险服务费…）
//
// 合并规则：月度账单已有的费用项，不能被对账中心覆盖
// （否则会把月度账单的订单范围冲掉）；对账中心独有的照常补进来。
func TestMonthlyBillWinsOverReconcile(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	importOne := func(kind, source, amount string, rows int) {
		items := make([]model.EcStatementItem, 0, rows)
		for i := 0; i < rows; i++ {
			items = append(items, model.EcStatementItem{
				Period: "202609", Kind: kind, Direction: model.StmtDirection(kind),
				OrderNo: "JD-" + kind + "-" + string(rune('A'+i)),
				Amount:  model.MustMoney(amount),
			})
		}
		st := &model.EcStatement{
			Platform: model.EcJD, Period: "202609", Kind: kind, Source: source,
			Direction: model.StmtDirection(kind), RowCount: len(items),
		}
		for _, it := range items {
			st.Amount += it.Amount
		}
		if err := svc.ImportStatement(ctx, st, items, admin); err != nil {
			t.Fatalf("导入 %s 失败: %v", kind, err)
		}
	}

	// 月度账单：货款 2 单 + 佣金
	importOne(model.StmtGoodsPayment, model.StmtSourceBill, "296", 2)
	importOne("京东·佣金", model.StmtSourceBill, "8.14", 2) // 合计 16.28
	// 对账中心：同样的货款（故意用不同数字与行数）+ 它独有的保险服务费
	importOne(model.StmtGoodsPayment, model.StmtSourceReconcile, "999", 5)
	importOne("京东·佣金", model.StmtSourceReconcile, "19.8", 5) // 合计 99，不应生效
	importOne("京东·商品保险服务费", model.StmtSourceReconcile, "0.42", 4)

	stmts, err := svc.Store.ListStatements(ctx, model.EcJD, "202609")
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]model.EcStatement{}
	for _, st := range stmts {
		byKind[st.Kind] = st
	}
	// 月度账单已有的，必须保持 bill 且行数/金额不变
	if st := byKind[model.StmtGoodsPayment]; st.Source != model.StmtSourceBill || st.RowCount != 2 {
		t.Errorf("交易货款应以月度账单为准（bill/2 行），实际 %s/%d 行",
			model.SourceLabel(st.Source), st.RowCount)
	}
	if st := byKind["京东·佣金"]; st.Source != model.StmtSourceBill || st.Amount != model.MustMoney("16.28") {
		t.Errorf("佣金应以月度账单为准（bill/16.28），实际 %s/%s",
			model.SourceLabel(st.Source), st.Amount)
	}
	// 对账中心独有的，要补进来
	if st, ok := byKind["京东·商品保险服务费"]; !ok || st.RowCount != 4 {
		t.Errorf("对账中心独有的商品保险服务费应被补进来，实际 %+v", byKind["京东·商品保险服务费"])
	}
}

// TestShippingNotInVatCredit 运费只在报表里展示，不参与增值税抵扣。
//
// 用户明确：商家寄件服务费（快递费）不从利润里扣，只在报表里给上游看。
// 既然不计入，那它也不该当平台开票的进项来抵扣——
// 之前所有支出都进了抵扣基数，把运费那部分税也抵掉了，
// 应交增值税少算、应结金额多算。
func TestShippingNotInVatCredit(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	// 一张订单：货款 1130、成本 565；费用：佣金 60（可抵扣）+ 运费 106（仅列示）
	rows := []EcOrderRow{{
		OrderNo: "JD-1", SubOrderNo: "JD-1", Title: "测试商品", EcProductID: "JD-P1",
		Qty: "5", PaidAmt: "1130", ItemStatus: "交易成功",
		RefundStatus: "没有申请退款", CreatedAt: "2026-09-10 10:00",
	}}
	if _, err := svc.ImportEcOrders(ctx, model.EcJD, rows, admin); err != nil {
		t.Fatal(err)
	}
	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "JD-P1", Name: "测试商品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("113"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindEcLink(ctx, model.EcJD, "JD-P1", "", "", pid, admin); err != nil {
		t.Fatal(err)
	}

	items := []model.EcStatementItem{
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "JD-1", SubOrderNo: "JD-1", EcProductID: "JD-P1",
			Qty: model.MustQty("5"), Amount: model.MustMoney("1130")},
		{Period: "202609", Kind: model.StmtBaseService, Direction: "expense",
			OrderNo: "JD-1", SubOrderNo: "JD-1", Amount: model.MustMoney("60")},
		{Period: "202609", Kind: model.StmtShipping, Direction: "expense",
			OrderNo: "JD-1", SubOrderNo: "JD-1", Amount: model.MustMoney("106")},
	}
	for _, kind := range []string{model.StmtGoodsPayment, model.StmtBaseService, model.StmtShipping} {
		var sub []model.EcStatementItem
		for _, it := range items {
			if it.Kind == kind {
				sub = append(sub, it)
			}
		}
		if err := svc.ImportStatement(ctx, &model.EcStatement{
			Platform: model.EcJD, Period: "202609", Kind: kind, Source: model.StmtSourceBill,
			Direction: model.StmtDirection(kind), RowCount: len(sub)}, sub, admin); err != nil {
			t.Fatal(err)
		}
	}

	settings, _ := svc.Store.Settings(ctx)
	rep, err := svc.BuildSettlement(ctx, model.EcJD, "202609", settings)
	if err != nil {
		t.Fatal(err)
	}
	total := rep.Total

	// 展示口径：全部费用 60 + 106 = 166
	if total.PlatformFee != model.MustMoney("166") {
		t.Errorf("平台费用（展示）应为 166，实际 %s", total.PlatformFee)
	}
	// 抵扣口径：只算可抵扣的 60，运费 106 不算
	if total.CreditableFee != model.MustMoney("60") {
		t.Errorf("可抵扣费用应为 60（不含运费），实际 %s", total.CreditableFee)
	}
	// 平台费抵扣 = 60/1.06×6% = 3.40；若误把运费算进去会是 9.40
	got := total.PlatformVat(rep.Rates).Float()
	if got < 3.39 || got > 3.41 {
		t.Errorf("平台费抵扣应约 3.40（不含运费），实际 %.2f", got)
	}

	// 收入 1130、成本 5×113 = 565
	if total.Cost != model.MustMoney("565") {
		t.Errorf("供货成本应为 565，实际 %s", total.Cost)
	}
	// 应交 = 销项 130.00 − 进项 65.00 − 抵扣 3.40 = 61.60
	vat := total.VatPayable(rep.Rates).Float()
	if vat < 61.5 || vat > 61.7 {
		t.Errorf("应交增值税应约 61.60，实际 %.2f（运费被算进抵扣就会偏小）", vat)
	}
	// 应结（上游该打给我的利润）
	//   = 销售收入 − 平台费用（承担的 60）− 供货成本 − 应交增值税
	//   = 1130 − 60 − 565 − 61.60 = 443.40
	// 注意必须扣平台费用：钱先进平台再进上游账户，
	// 平台打款前就把费用扣掉了，漏减会把利润算多。
	net := total.Net(rep.Rates).Float()
	if net < 443.3 || net > 443.5 {
		t.Errorf("应结金额应约 443.40（已扣承担的平台费用 60），实际 %.2f", net)
	}
}

// TestOneProductManyEcIDs 同一个产品可以绑多个平台商品ID。
//
// 用户的实际场景：一个产品在淘宝有多个链接（不同标题、不同活动），
// 它们是同一个产品。要求：
//   - 同一平台的多个商品ID 都能绑到同一个产品
//   - 各链接的订单都归到这一个产品，收入与成本合并计算
//   - 同一个商品ID（含规格）不能被两个产品同时占用（唯一的限制）
func TestOneProductManyEcIDs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "P-MULTI", Name: "同一产品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("140"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 同一平台两个商品ID + 另一个平台一个
	for _, l := range []struct{ platform, ecID string }{
		{model.EcTaobao, "TB-A"},
		{model.EcTaobao, "TB-B"},
		{model.EcDouyin, "DY-C"},
	} {
		if _, err := svc.BindEcLink(ctx, l.platform, l.ecID, "", "", pid, admin); err != nil {
			t.Fatalf("绑 %s/%s 失败: %v", l.platform, l.ecID, err)
		}
	}
	links, _ := svc.Store.ProductLinks(ctx, pid)
	if len(links) != 3 {
		t.Fatalf("一个产品应能绑 3 条（同平台两条 + 别的平台一条），实际 %d", len(links))
	}

	// 两个商品ID 各来一单
	rows := []EcOrderRow{
		{OrderNo: "O-A", SubOrderNo: "O-A", Title: "同一产品", EcProductID: "TB-A",
			Qty: "2", PaidAmt: "600", ItemStatus: "交易成功",
			RefundStatus: "没有申请退款", CreatedAt: "2026-08-10 10:00"},
		{OrderNo: "O-B", SubOrderNo: "O-B", Title: "同一产品", EcProductID: "TB-B",
			Qty: "3", PaidAmt: "900", ItemStatus: "交易成功",
			RefundStatus: "没有申请退款", CreatedAt: "2026-08-11 10:00"},
	}
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, rows, admin); err != nil {
		t.Fatal(err)
	}
	// 两单都要匹配到同一个产品
	orders, _ := svc.Store.ListEcOrders(ctx, store.EcOrderFilter{Platform: model.EcTaobao})
	matched := 0
	for _, o := range orders {
		for _, it := range o.Items {
			if it.ProductID != nil && *it.ProductID == pid {
				matched++
			}
		}
	}
	if matched != 2 {
		t.Fatalf("两个商品ID 的订单都应匹配到同一个产品，实际匹配 %d", matched)
	}

	// 账单：一份交易货款，两行（两个商品ID）
	items := []model.EcStatementItem{
		{Period: "202608", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "O-A", SubOrderNo: "O-A", EcProductID: "TB-A",
			Qty: model.MustQty("2"), Amount: model.MustMoney("600")},
		{Period: "202608", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "O-B", SubOrderNo: "O-B", EcProductID: "TB-B",
			Qty: model.MustQty("3"), Amount: model.MustMoney("900")},
	}
	if err := svc.ImportStatement(ctx, &model.EcStatement{
		Platform: model.EcTaobao, Period: "202608", Kind: model.StmtGoodsPayment,
		Source: model.StmtSourceBill, Direction: "income"}, items, admin); err != nil {
		t.Fatal(err)
	}

	settings, _ := svc.Store.Settings(ctx)
	rep, err := svc.BuildSettlement(ctx, model.EcTaobao, "202608", settings)
	if err != nil {
		t.Fatal(err)
	}
	total := rep.Total
	// 收入 600+900；成本 (2+3)×140 = 700；数量 5
	if total.Revenue != model.MustMoney("1500") {
		t.Errorf("销售收入应为 1500（两个链接合并），实际 %s", total.Revenue)
	}
	if total.Cost != model.MustMoney("700") {
		t.Errorf("供货成本应为 700（两个链接的瓶数一起算），实际 %s", total.Cost)
	}
	if total.Qty != model.MustQty("5") {
		t.Errorf("数量应为 5，实际 %s", total.Qty)
	}
	// 按商品汇总里也应只有一行、且是合并后的数
	if len(rep.Products) != 1 {
		t.Fatalf("按商品汇总应合并成 1 行，实际 %d 行", len(rep.Products))
	}
	if rep.Products[0].S.Cost != model.MustMoney("700") ||
		rep.Products[0].S.Revenue != model.MustMoney("1500") {
		t.Errorf("按商品汇总应合并两个链接：成本 700 / 收入 1500，实际 %s / %s",
			rep.Products[0].S.Cost, rep.Products[0].S.Revenue)
	}

	// 唯一的限制：同一个商品ID 不能再绑给别的产品
	other, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "P-OTHER", Name: "另一个产品", Unit: "瓶", IsActive: true, IsWine: true})
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "TB-A", "", "", other, admin); err == nil {
		t.Error("同一个商品ID 绑给第二个产品时应被拒绝")
	}
}

// TestMultiPeriodSettlementNoDoubleFee 多账期合并时平台费用不能翻倍。
//
// 踩过：BuildSettlementFor 里先调 Total.Add(one)（Add 已经累加
// PlatformFee / CreditableFee），然后又 += 了一遍，
// 结果页面上的平台费用正好是实际的两倍（京东显示 76.26 实际 38.13）。
func TestMultiPeriodSettlementNoDoubleFee(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	// 两个账期，各有一个基础软件服务费 60（可抵扣）
	for _, p := range []string{"202608", "202609"} {
		items := []model.EcStatementItem{{
			Period: p, Kind: model.StmtBaseService, Direction: "expense",
			OrderNo: "O-" + p, Amount: model.MustMoney("60"),
		}}
		if err := svc.ImportStatement(ctx, &model.EcStatement{
			Platform: model.EcJD, Period: p, Kind: model.StmtBaseService,
			Source: model.StmtSourceBill, Direction: "expense"}, items, admin); err != nil {
			t.Fatal(err)
		}
	}

	settings, _ := svc.Store.Settings(ctx)
	// 单账期
	one, err := svc.BuildSettlementFor(ctx, model.EcJD, []string{"202608"}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if one.Total.PlatformFee != model.MustMoney("60") {
		t.Errorf("单账期平台费用应为 60，实际 %s", one.Total.PlatformFee)
	}
	// 两个账期合并：应为 120，不能是 120 的两倍
	two, err := svc.BuildSettlementFor(ctx, model.EcJD, []string{"202608", "202609"}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if two.Total.PlatformFee != model.MustMoney("120") {
		t.Errorf("两账期合并平台费用应为 120，实际 %s（翻倍说明重复累加）", two.Total.PlatformFee)
	}
	if two.Total.CreditableFee != model.MustMoney("120") {
		t.Errorf("两账期合并可抵扣费用应为 120，实际 %s", two.Total.CreditableFee)
	}
	// 逐账期小计也要留着
	if len(two.Periods) != 2 {
		t.Errorf("应保留 2 条逐账期小计，实际 %d", len(two.Periods))
	}
}

// TestShippingNotInBorneFee 运费不计入"我方承担的平台费用"。
//
// 用户反馈：平台费用汇总里有快递费，各平台结算里也把快递费加进去了，
// 但快递费不该由他承担（只列示给上游看）。
// 所以「平台费用」必须是剔除运费后的口径，运费单独列示。
func TestShippingNotInBorneFee(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	// 一笔服务费 60（承担）+ 一笔运费 106（仅列示）
	for _, it := range []struct {
		kind   string
		amount string
	}{
		{model.StmtBaseService, "60"},
		{model.StmtShipping, "106"},
	} {
		if err := svc.ImportStatement(ctx, &model.EcStatement{
			Platform: model.EcTaobao, Period: "202609", Kind: it.kind,
			Source: model.StmtSourceBill, Direction: "expense"}, []model.EcStatementItem{{
			Period: "202609", Kind: it.kind, Direction: "expense",
			OrderNo: "T-" + it.kind, Amount: model.MustMoney(it.amount),
		}}, admin); err != nil {
			t.Fatal(err)
		}
	}

	settings, _ := svc.Store.Settings(ctx)
	rep, err := svc.BuildSettlement(ctx, model.EcTaobao, "202609", settings)
	if err != nil {
		t.Fatal(err)
	}
	total := rep.Total
	// 展示口径仍然保留全部（66+106）
	if total.PlatformFee != model.MustMoney("166") {
		t.Errorf("账单全部费用应为 166，实际 %s", total.PlatformFee)
	}
	// 我方承担的口径只算 60
	if total.BorneFee != model.MustMoney("60") {
		t.Errorf("我方承担的平台费用应为 60（不含运费），实际 %s", total.BorneFee)
	}
	if total.ListedFee != model.MustMoney("106") {
		t.Errorf("仅列示的费用应为 106（运费），实际 %s", total.ListedFee)
	}

	// 费用矩阵的合计也只算承担的
	rep2, err := svc.BuildSummary(ctx, []store.StatementKey{
		{Platform: model.EcTaobao, Period: "202609"},
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
	m := rep2.Fees
	if m.Total != model.MustMoney("60") {
		t.Errorf("费用矩阵合计应只算承担的 60，实际 %s", m.Total)
	}
	if m.ListedTotal != model.MustMoney("106") {
		t.Errorf("仅列示合计应为 106，实际 %s", m.ListedTotal)
	}
	for _, k := range m.Kinds {
		if k == model.StmtShipping {
			t.Error("运费不该出现在费用矩阵的费用行里（应单独列示）")
		}
	}
	found := false
	for _, k := range m.ListedKinds {
		if k == model.StmtShipping {
			found = true
		}
	}
	if !found {
		t.Error("运费应出现在「仅列示」列表里")
	}
}

// TestRefundedOrderCostUsesEffectiveQty 退款订单不能按原始数量算成本。
//
// 淘宝账单里退款单保留「数量」，但「订单实际金额」已扣掉退款：
// 例如数量 2、实际金额 0、退款 90 —— 整单退掉。
// 若还按 2 瓶算成本，就凭空多出一笔成本（真实数据里出现过，
// 等于结算时多扣了 280 元）。
func TestRefundedOrderCostUsesEffectiveQty(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "REF-P1", Name: "退款测试品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("140"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BindEcLink(ctx, model.EcTaobao, "REF-EC-1", "", "", pid, admin); err != nil {
		t.Fatal(err)
	}
	rows := []EcOrderRow{
		// 整单退款：数量 2、实际金额 0、退款 180
		{OrderNo: "REF-FULL", SubOrderNo: "REF-FULL", Title: "退款测试品",
			EcProductID: "REF-EC-1", Qty: "2", PaidAmt: "180", ItemStatus: "交易关闭",
			RefundStatus: "退款成功", CreatedAt: "2026-09-10 10:00"},
		// 正常单：数量 3、金额 900
		{OrderNo: "REF-OK", SubOrderNo: "REF-OK", Title: "退款测试品",
			EcProductID: "REF-EC-1", Qty: "3", PaidAmt: "900", ItemStatus: "交易成功",
			RefundStatus: "没有申请退款", CreatedAt: "2026-09-11 10:00"},
	}
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, rows, admin); err != nil {
		t.Fatal(err)
	}
	items := []model.EcStatementItem{
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "REF-FULL", SubOrderNo: "REF-FULL", EcProductID: "REF-EC-1",
			Qty: model.MustQty("2"), Amount: 0, RefundAmount: model.MustMoney("180")},
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "REF-OK", SubOrderNo: "REF-OK", EcProductID: "REF-EC-1",
			Qty: model.MustQty("3"), Amount: model.MustMoney("900")},
	}
	if err := svc.ImportStatement(ctx, &model.EcStatement{
		Platform: model.EcTaobao, Period: "202609", Kind: model.StmtGoodsPayment,
		Source: model.StmtSourceBill, Direction: "income"}, items, admin); err != nil {
		t.Fatal(err)
	}

	settings, _ := svc.Store.Settings(ctx)
	rep, err := svc.BuildSettlement(ctx, model.EcTaobao, "202609", settings)
	if err != nil {
		t.Fatal(err)
	}
	total := rep.Total
	// 收入只有正常单
	if total.Revenue != model.MustMoney("900") {
		t.Errorf("销售收入应为 900（退款单收入为 0），实际 %s", total.Revenue)
	}
	// 数量只算正常单的 3 件（退款单折算为 0）
	if total.Qty != model.MustQty("3") {
		t.Errorf("有效数量应为 3（退款单折算 0），实际 %s", total.Qty)
	}
	// 成本只算 3 × 140 = 420，绝不能是 5 × 140 = 700
	if total.Cost != model.MustMoney("420") {
		t.Errorf("供货成本应为 420（退款单不计成本），实际 %s", total.Cost)
	}
}

// TestSettlementFormulaMatchesRealData 用真实账单数字钉死结算公式。
//
// 用户核对时发现"货款 − 供货成本 − 平台费用 − 应交增值税"对不上，
// 根因是 Net 漏减了平台费用——店铺是上游的，钱先进平台再进上游账户，
// 平台打款前就把费用扣掉了，所以必须减掉，否则利润被算多。
//
// 下面两组数字全部来自用户的真实账单，逐项对齐，
// 任何人改动结算公式都会在这里失败。
func TestSettlementFormulaMatchesRealData(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)
	settings, _ := svc.Store.Settings(ctx)

	// ---------------- 京东 2026-09 ----------------
	// 货款 575.00（2 单）；费用：佣金 31.62 + 交易服务费 3.45 + 保险 3.06 = 38.13
	// 供货成本 295.00（140 + 155）；应交增值税 30.05
	// 应结 = 575.00 − 38.13 − 295.00 − 30.05 = 211.82
	jdP1, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "JD-VQA", Name: "维达尔冰白200ML", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("140")})
	jdP2, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "JD-CAB", Name: "品丽珠冰红200ML", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("155")})
	_, _ = svc.BindEcLink(ctx, model.EcJD, "10215605168506", "", "", jdP1, admin)
	_, _ = svc.BindEcLink(ctx, model.EcJD, "10215605594506", "", "", jdP2, admin)
	if _, err := svc.ImportEcOrders(ctx, model.EcJD, []EcOrderRow{
		{OrderNo: "3600482011154052", SubOrderNo: "3600482011154052", Title: "维达尔冰白200ML",
			EcProductID: "10215605168506", Qty: "1", PaidAmt: "279", ItemStatus: "完成",
			RefundStatus: "没有申请退款", CreatedAt: "2026-08-24 20:05"},
		{OrderNo: "3599406003546153", SubOrderNo: "3599406003546153", Title: "品丽珠冰红200ML",
			EcProductID: "10215605594506", Qty: "1", PaidAmt: "296", ItemStatus: "完成",
			RefundStatus: "没有申请退款", CreatedAt: "2026-08-23 22:32"},
	}, admin); err != nil {
		t.Fatal(err)
	}
	// 按「账单类型」分组导入：同一类型是一份账单、里面多行明细。
	// （拆成多次同类型导入会互相覆盖，这是我第一版测试写错的地方。）
	importByKind := func(platform, period string, items []model.EcStatementItem) {
		groups := map[string][]model.EcStatementItem{}
		var order []string
		for _, it := range items {
			if _, ok := groups[it.Kind]; !ok {
				order = append(order, it.Kind)
			}
			groups[it.Kind] = append(groups[it.Kind], it)
		}
		for _, k := range order {
			list := groups[k]
			st := &model.EcStatement{Platform: platform, Period: period, Kind: k,
				Source: model.StmtSourceBill, Direction: list[0].Direction}
			if err := svc.ImportStatement(ctx, st, list, admin); err != nil {
				t.Fatal(err)
			}
		}
	}
	importByKind(model.EcJD, "202609", []model.EcStatementItem{
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "3600482011154052", SubOrderNo: "3600482011154052",
			EcProductID: "10215605168506", Qty: model.MustQty("1"), Amount: model.MustMoney("279")},
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "3599406003546153", SubOrderNo: "3599406003546153",
			EcProductID: "10215605594506", Qty: model.MustQty("1"), Amount: model.MustMoney("296")},
		{Period: "202609", Kind: "京东·佣金", Direction: "expense",
			OrderNo: "3600482011154052", SubOrderNo: "3600482011154052", Amount: model.MustMoney("15.34")},
		{Period: "202609", Kind: "京东·佣金", Direction: "expense",
			OrderNo: "3599406003546153", SubOrderNo: "3599406003546153", Amount: model.MustMoney("16.28")},
		{Period: "202609", Kind: "京东·交易服务费", Direction: "expense",
			OrderNo: "3600482011154052", SubOrderNo: "3600482011154052", Amount: model.MustMoney("1.67")},
		{Period: "202609", Kind: "京东·交易服务费", Direction: "expense",
			OrderNo: "3599406003546153", SubOrderNo: "3599406003546153", Amount: model.MustMoney("1.78")},
		{Period: "202609", Kind: "京东·商品保险服务费", Direction: "expense",
			OrderNo: "3600482011154052", SubOrderNo: "3600482011154052", Amount: model.MustMoney("1.53")},
		{Period: "202609", Kind: "京东·商品保险服务费", Direction: "expense",
			OrderNo: "3599406003546153", SubOrderNo: "3599406003546153", Amount: model.MustMoney("1.53")},
	})
	jd, err := svc.BuildSettlement(ctx, model.EcJD, "202609", settings)
	if err != nil {
		t.Fatal(err)
	}
	jt := jd.Total
	checkMoney(t, "京东·货款", jt.Goods, "575.00")
	checkMoney(t, "京东·平台费用（承担）", jt.BorneFee, "38.13")
	checkMoney(t, "京东·供货成本", jt.Cost, "295.00")
	checkMoney(t, "京东·应交增值税", jt.VatPayable(jd.Rates), "30.05")
	// 关键：应结 = 货款 − 平台费用 − 供货成本 − 应交增值税
	checkMoney(t, "京东·应结金额", jt.Net(jd.Rates), "211.82")

	// ---------------- 淘宝 2026-08 ----------------
	// 货款 5737.90 + 补贴 39.02 − 垫支 609.00 = 销售收入 5167.92
	// 平台费用：承担 345.39 + 运费 231.90（运费只展示）
	// 下面用单件商品（成本 140）验证公式本身；真实 8 月是 22 件混合品，
	// 供货成本 2810.00，应交增值税 251.71，
	// 应结 = 5167.92 − 345.39 − 2810.00 − 251.71 = 1760.82
	tbP, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "TB-1", Name: "淘宝测试品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("140")})
	_, _ = svc.BindEcLink(ctx, model.EcTaobao, "TB-X", "", "", tbP, admin)
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, []EcOrderRow{{
		OrderNo: "TB-ORD-1", SubOrderNo: "TB-ORD-1", Title: "淘宝测试品",
		EcProductID: "TB-X", Qty: "1", PaidAmt: "5737.90", ItemStatus: "交易成功",
		RefundStatus: "没有申请退款", CreatedAt: "2026-08-10 10:00",
	}}, admin); err != nil {
		t.Fatal(err)
	}
	tbItems := []model.EcStatementItem{
		{Period: "202608", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "TB-ORD-1", SubOrderNo: "TB-ORD-1", EcProductID: "TB-X",
			Qty: model.MustQty("1"), Amount: model.MustMoney("5737.90")},
		{Period: "202608", Kind: model.StmtCoinCoop, Direction: "income",
			OrderNo: "TB-ORD-1", SubOrderNo: "TB-ORD-1", Amount: model.MustMoney("39.02")},
		{Period: "202608", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "TB-ORD-1", SubOrderNo: "TB-ORD-1",
			Qty: model.MustQty("0"), Amount: 0,
			Advance: model.MustMoney("609.00")},
		{Period: "202608", Kind: model.StmtBaseService, Direction: "expense", Amount: model.MustMoney("345.39")},
		{Period: "202608", Kind: model.StmtShipping, Direction: "expense", Amount: model.MustMoney("231.90")},
	}
	importByKind(model.EcTaobao, "202608", tbItems)
	tb, err := svc.BuildSettlement(ctx, model.EcTaobao, "202608", settings)
	if err != nil {
		t.Fatal(err)
	}
	tt := tb.Total
	checkMoney(t, "淘宝·销售收入", tt.Revenue, "5167.92")
	checkMoney(t, "淘宝·平台费用（承担）", tt.BorneFee, "345.39")
	checkMoney(t, "淘宝·仅展示运费", tt.ListedFee, "231.90")
	checkMoney(t, "淘宝·供货成本", tt.Cost, "140.00")
	// 应交增值税 = 销项 5167.92/1.13×13% − 进项 140/1.13×13% − 平台费抵扣 345.39/1.06×6%
	//            = 594.54 − 16.11 − 19.55 = 558.88
	checkMoney(t, "淘宝·应交增值税", tt.VatPayable(tb.Rates), "558.88")
	// 关键：应结 = 销售收入 − 平台费用 − 供货成本 − 应交增值税
	//            = 5167.92 − 345.39 − 140.00 − 558.88 = 4123.65
	checkMoney(t, "淘宝·应结金额", tt.Net(tb.Rates), "4123.65")
}

// checkMoney 按"分"比较金额。
//
// 增值税这类要除以 1.13/1.06 的数，内部精度是 1/10000，
// 30.0487 显示成 30.05 是对的；用严格相等反而会误报，
// 所以这里比到分。
func checkMoney(t *testing.T, name string, got model.Money, want string) {
	t.Helper()
	w := model.MustMoney(want)
	diff := got - w
	if diff < 0 {
		diff = -diff
	}
	if diff > 100 { // 1 分 = 100（Money 精度 1/10000）
		t.Errorf("%s 应为 %s，实际 %s（差 %s）", name, w, got, diff)
	}
}

// TestSummaryRowFormulas 第一页（各平台结算）每一列的公式都要成立。
//
// 用户要求：不只是京东，整页的计算公式都要核对。
// 这里用"公式不变量"的方式钉死——不管底层数据是什么，
// 下面这些等式必须恒成立；任何一处口径改动都会在这里失败。
//
//	销量     = 有效数量合计（退款按实际成交折算）
//	销售收入 = 货款 + 平台补贴 − 平台代付扣回
//	供货成本 = 有效数量 × 电商成本
//	平台费用 = 我方承担的费用（不含仅展示的运费）
//	应交增值税 = 销项税额 − 进项税额 − 平台费抵扣
//	应结金额 = 销售收入 − 平台费用 − 供货成本 − 应交增值税
//	毛利率   = 应结金额 ÷ 销售收入
//	合计行   = 各平台行逐列相加
func TestSummaryRowFormulas(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	// 淘宝：货款 1000 + 补贴 50 − 代付扣回 200；成本 2 件 × 140；费用 30（承担）+ 运费 10
	tbP, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "F-TB", Name: "淘宝公式品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("140")})
	_, _ = svc.BindEcLink(ctx, model.EcTaobao, "F-TB-1", "", "", tbP, admin)
	if _, err := svc.ImportEcOrders(ctx, model.EcTaobao, []EcOrderRow{{
		OrderNo: "F-TB-O1", SubOrderNo: "F-TB-O1", Title: "淘宝公式品",
		EcProductID: "F-TB-1", Qty: "2", PaidAmt: "1000", ItemStatus: "交易成功",
		RefundStatus: "没有申请退款", CreatedAt: "2026-09-10 10:00",
	}}, admin); err != nil {
		t.Fatal(err)
	}
	tbItems := []model.EcStatementItem{
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "F-TB-O1", SubOrderNo: "F-TB-O1", EcProductID: "F-TB-1",
			Qty: model.MustQty("2"), Amount: model.MustMoney("1000")},
		{Period: "202609", Kind: model.StmtCoinCoop, Direction: "income",
			OrderNo: "F-TB-O1", SubOrderNo: "F-TB-O1", Amount: model.MustMoney("50")},
		{Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
			OrderNo: "F-TB-O1", SubOrderNo: "F-TB-O1", Qty: model.MustQty("0"),
			Amount: 0, Advance: model.MustMoney("200")},
		{Period: "202609", Kind: model.StmtBaseService, Direction: "expense",
			OrderNo: "F-TB-O1", SubOrderNo: "F-TB-O1", Amount: model.MustMoney("30")},
		{Period: "202609", Kind: model.StmtShipping, Direction: "expense",
			Amount: model.MustMoney("10")},
	}
	groups := map[string][]model.EcStatementItem{}
	var order []string
	for _, it := range tbItems {
		if _, ok := groups[it.Kind]; !ok {
			order = append(order, it.Kind)
		}
		groups[it.Kind] = append(groups[it.Kind], it)
	}
	for _, k := range order {
		list := groups[k]
		if err := svc.ImportStatement(ctx, &model.EcStatement{
			Platform: model.EcTaobao, Period: "202609", Kind: k,
			Source: model.StmtSourceBill, Direction: list[0].Direction}, list, admin); err != nil {
			t.Fatal(err)
		}
	}

	// 京东：货款 500；成本 2 件 × 155；费用 20
	jdP, _ := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "F-JD", Name: "京东公式品", Unit: "瓶", IsActive: true, IsWine: true,
		EcCost: model.MustMoney("155")})
	_, _ = svc.BindEcLink(ctx, model.EcJD, "F-JD-1", "", "", jdP, admin)
	if _, err := svc.ImportEcOrders(ctx, model.EcJD, []EcOrderRow{{
		OrderNo: "F-JD-O1", SubOrderNo: "F-JD-O1", Title: "京东公式品",
		EcProductID: "F-JD-1", Qty: "2", PaidAmt: "500", ItemStatus: "完成",
		RefundStatus: "没有申请退款", CreatedAt: "2026-09-11 10:00",
	}}, admin); err != nil {
		t.Fatal(err)
	}
	if err := svc.ImportStatement(ctx, &model.EcStatement{
		Platform: model.EcJD, Period: "202609", Kind: model.StmtGoodsPayment,
		Source: model.StmtSourceBill, Direction: "income"}, []model.EcStatementItem{{
		Period: "202609", Kind: model.StmtGoodsPayment, Direction: "income",
		OrderNo: "F-JD-O1", SubOrderNo: "F-JD-O1", EcProductID: "F-JD-1",
		Qty: model.MustQty("2"), Amount: model.MustMoney("500"),
	}}, admin); err != nil {
		t.Fatal(err)
	}
	if err := svc.ImportStatement(ctx, &model.EcStatement{
		Platform: model.EcJD, Period: "202609", Kind: "京东·佣金",
		Source: model.StmtSourceBill, Direction: "expense"}, []model.EcStatementItem{{
		Period: "202609", Kind: "京东·佣金", Direction: "expense",
		OrderNo: "F-JD-O1", SubOrderNo: "F-JD-O1", Amount: model.MustMoney("20"),
	}}, admin); err != nil {
		t.Fatal(err)
	}

	settings, _ := svc.Store.Settings(ctx)
	rep, err := svc.BuildSummary(ctx, []store.StatementKey{
		{Platform: model.EcTaobao, Period: "202609"},
		{Platform: model.EcJD, Period: "202609"},
	}, settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Items) != 2 {
		t.Fatalf("应有 2 个平台行，实际 %d", len(rep.Items))
	}

	// 每一行的公式不变量
	var sumQty model.Qty
	var sumRev, sumSub, sumAdv, sumCost, sumFee, sumVat, sumNet model.Money
	for _, it := range rep.Items {
		s := it.Total
		r := rep.Rates
		name := it.Label + " "
		// 销售收入 = 货款 + 补贴 − 代付扣回
		checkMoney(t, name+"销售收入公式",
			s.Revenue, (s.Goods + s.Subsidy - s.Advance).String())
		// 应交增值税 = 销项 − 进项 − 平台费抵扣
		checkMoney(t, name+"增值税公式",
			s.VatPayable(r),
			(s.OutputVat(r) - s.InputVat(r) - s.PlatformVat(r)).String())
		// 应结 = 销售收入 − 平台费用 − 供货成本 − 应交增值税
		checkMoney(t, name+"应结公式",
			s.Net(r), (s.Revenue - s.BorneFee - s.Cost - s.VatPayable(r)).String())
		// 毛利率 = 应结 ÷ 销售收入（Ratio/Margin 都是百分数，如 88.52）
		if s.Revenue != 0 {
			want := model.Ratio(s.Net(r), s.Revenue)
			if got := s.Margin(r); got < want-0.0001 || got > want+0.0001 {
				t.Errorf("%s毛利率应为 %.4f%%，实际 %.4f%%", name, want, got)
			}
		}
		// 费用拆分：账单全部费用 = 承担 + 仅展示
		checkMoney(t, name+"费用拆分",
			s.PlatformFee, (s.BorneFee + s.ListedFee).String())
		sumQty += s.Qty
		sumRev += s.Revenue
		sumSub += s.Subsidy
		sumAdv += s.Advance
		sumCost += s.Cost
		sumFee += s.BorneFee
		sumVat += s.VatPayable(r)
		sumNet += s.Net(r)
	}
	// 合计行 = 各平台行逐列相加
	tt := rep.Total
	checkMoney(t, "合计·销量", model.Money(sumQty), model.Money(tt.Qty).String())
	checkMoney(t, "合计·销售收入", tt.Revenue, sumRev.String())
	checkMoney(t, "合计·平台补贴", tt.Subsidy, sumSub.String())
	checkMoney(t, "合计·代付扣回", tt.Advance, sumAdv.String())
	checkMoney(t, "合计·供货成本", tt.Cost, sumCost.String())
	checkMoney(t, "合计·平台费用", tt.BorneFee, sumFee.String())
	checkMoney(t, "合计·应交增值税", tt.VatPayable(rep.Rates), sumVat.String())
	checkMoney(t, "合计·应结金额", tt.Net(rep.Rates), sumNet.String())

	// 具体数字（手工核对过的期望值）
	tb := rep.Items[0].Total // 淘宝
	if rep.Items[0].Platform != model.EcTaobao {
		tb = rep.Items[1].Total
	}
	checkMoney(t, "淘宝·销售收入", tb.Revenue, "850.00") // 1000 + 50 − 200
	checkMoney(t, "淘宝·供货成本", tb.Cost, "280.00")    // 2 × 140
	checkMoney(t, "淘宝·平台费用", tb.BorneFee, "30.00") // 不含运费 10
	checkMoney(t, "淘宝·仅展示", tb.ListedFee, "10.00")
	// 应结 = 850 − 30 − 280 − 增值税
	checkMoney(t, "淘宝·应结", tb.Net(rep.Rates),
		(tb.Revenue - tb.BorneFee - tb.Cost - tb.VatPayable(rep.Rates)).String())
}

package service

import (
	"context"
	"testing"

	"icewine-erp/internal/model"
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
	// 应结 = 1130 − 565 − 61.60 = 503.40
	net := total.Net(rep.Rates).Float()
	if net < 503.3 || net > 503.5 {
		t.Errorf("应结金额应约 503.40，实际 %.2f", net)
	}
}

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

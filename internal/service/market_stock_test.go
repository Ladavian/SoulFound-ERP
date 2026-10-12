package service

import (
	"context"
	"testing"

	"icewine-erp/internal/model"
)

// TestMarketCarryDeductsStock 市集备货出库就扣库存，结算时对冲。
//
// 用户反馈：总库存没有减去市集消耗的库存。
// 原因是库存只在结算时才按 sold/tasting/gift/loss 扣，
// 市集进行中时货已经带离仓库，账上却还是满的。
func TestMarketCarryDeductsStock(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "MK-1", Name: "市集测试酒", Unit: "瓶", IsActive: true, IsWine: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 入库 100 瓶
	if err := svc.AdjustStock(ctx, AdjustInput{
		ProductID: pid, Qty: model.MustQty("100"), Reason: model.ReasonOpening,
		OccurredOn: "2026-08-01", Note: "期初",
	}, admin); err != nil {
		t.Fatal(err)
	}
	stockOf := func() float64 {
		p, err := svc.Store.ProductByID(ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		return p.StockQty.Float()
	}
	if got := stockOf(); got != 100 {
		t.Fatalf("期初库存应为 100，实际 %.2f", got)
	}

	// 建市集：备货 30，售 20，试饮 3
	mid, err := svc.SaveMarket(ctx, MarketInput{
		Name: "测试市集", StartDate: "2026-08-10", EndDate: "2026-08-12",
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	itemID, err := svc.AddMarketProduct(ctx, mid, pid, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateMarketItemRow(ctx, mid, itemID, MarketItemUpdate{
		CarriedQty: model.MustQty("30"), SoldQty: model.MustQty("20"),
		TastingQty: model.MustQty("3"), UnitPrice: model.MustMoney("100"),
	}, admin); err != nil {
		t.Fatal(err)
	}

	// 还在「计划中」：货没动，库存不变
	if got := stockOf(); got != 100 {
		t.Errorf("计划中不应动库存，实际 %.2f", got)
	}

	// 切到「进行中」：货带走 → 库存应减 30
	if err := svc.SetMarketStatus(ctx, mid, model.MarketOngoing, admin); err != nil {
		t.Fatal(err)
	}
	if got := stockOf(); got != 70 {
		t.Errorf("进行中应扣掉备货 30（100−30=70），实际 %.2f", got)
	}

	// 重复切状态不能重复扣（幂等）
	if err := svc.SetMarketStatus(ctx, mid, model.MarketOngoing, admin); err != nil {
		t.Fatal(err)
	}
	if got := stockOf(); got != 70 {
		t.Errorf("重复置为进行中不应重复扣，实际 %.2f", got)
	}

	// 结算：备货对冲回库(+30)，再按实际消耗扣（售20 + 试饮3 = 23）
	// 净结果 = 100 − 23 = 77
	if err := svc.SettleMarket(ctx, mid, admin); err != nil {
		t.Fatal(err)
	}
	if got := stockOf(); got != 77 {
		t.Errorf("结算后应为 100 − (20售+3试饮) = 77，实际 %.2f", got)
	}

	// 撤销结算：回到进行中 → 又只剩备货扣减 70
	if err := svc.UnsettleMarket(ctx, mid, admin); err != nil {
		t.Fatal(err)
	}
	if got := stockOf(); got != 70 {
		t.Errorf("撤销结算后回到进行中状态应为 70，实际 %.2f", got)
	}
}

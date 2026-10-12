package service

import (
	"context"
	"testing"

	"icewine-erp/internal/model"
)

// TestPurchaseEditKeepsDraftStatus 编辑草稿采购单后状态必须还是草稿。
//
// 用户反馈："到确认入库这一步，点编辑改一下再提交，就提示这一单采购单作废，
// 但采购单列表里还显示是草稿状态"。
//
// 根因：SavePurchase 的更新分支没有带上状态，而 UpdatePurchase 会把
// status 一起写库，于是保存后状态被写成空字符串——
// 详情页把空值当"已作废"（提示不能入库），列表页又把空值当"草稿"，
// 两边显示矛盾。
func TestPurchaseEditKeepsDraftStatus(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	admin := adminUser(t, svc)

	pid, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "PO-P1", Name: "采购测试品", Unit: "瓶", IsActive: true, IsWine: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 建草稿
	id, err := svc.SavePurchase(ctx, PurchaseInput{
		PurchaseDate: "2026-08-05",
		Items: []PurchaseItemInput{{
			ProductID: pid, Qty: model.MustQty("10"), UnitPrice: model.MustMoney("40"),
		}},
	}, admin)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.Store.PurchaseByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsDraft() {
		t.Fatalf("新建后应是草稿，实际 %q", p.Status)
	}

	// 编辑（改数量）后再保存
	if _, err := svc.SavePurchase(ctx, PurchaseInput{
		ID:           id,
		PurchaseDate: "2026-08-05",
		Items: []PurchaseItemInput{{
			ProductID: pid, Qty: model.MustQty("12"), UnitPrice: model.MustMoney("40"),
		}},
	}, admin); err != nil {
		t.Fatal(err)
	}
	p, err = svc.Store.PurchaseByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != model.PurchaseDraft {
		t.Errorf("编辑保存后状态应仍是 %q，实际 %q（写成空会让详情页误报已作废）",
			model.PurchaseDraft, p.Status)
	}
	if p.IsVoid() {
		t.Error("不该被当成已作废")
	}

	// 状态没坏，就应该能正常入库
	if err := svc.ConfirmPurchase(ctx, id, admin); err != nil {
		t.Fatalf("编辑后应能正常确认入库，实际报错：%v", err)
	}
	p, _ = svc.Store.PurchaseByID(ctx, id)
	if !p.IsConfirmed() {
		t.Errorf("确认入库后状态应为 %q，实际 %q", model.PurchaseConfirmed, p.Status)
	}
}

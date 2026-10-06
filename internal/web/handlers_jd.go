package web

import (
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
)

// ---------------------------------------------------------------- 京东
//
// 京东的账单结构和淘宝完全不同，而且一个平台有三份文件：
//   月度账单 CSV   每行一个费用项（货款/佣金/交易服务费…）+ 收支方向
//   对账中心 xlsx  交易汇总 + 费用明细（费用名称/分类/金额/费率，
//                  含「退换货无忧服务费」这类月度账单里没有的项）
//   订单明细 xlsx  订单号 / 商品ID / 京东价 / 商家应收
// 结算口径与淘宝一致：只承担平台费用与增值税。

// isJDMonthly 京东月度账单 CSV。
func isJDMonthly(sh importSheet) bool {
	j := strings.Join(sh.Headers, " ")
	return strings.Contains(j, "费用项") && strings.Contains(j, "收支方向")
}

// isJDFees 京东对账中心的「费用明细」。
func isJDFees(sh importSheet) bool {
	j := strings.Join(sh.Headers, " ")
	return strings.Contains(j, "费用名称") && strings.Contains(j, "费用分类")
}

// isJDOrders 京东订单明细。
func isJDOrders(sh importSheet) bool {
	j := strings.Join(sh.Headers, " ")
	return strings.Contains(j, "商家应收") && strings.Contains(j, "订购数量")
}

// jdStatementRows 解析京东月度账单：每个费用项一行。
//
// 费用项直接进账单类型（如「京东·佣金」），方向以平台的「收支方向」为准；
// 没有这一列时按关键字猜（货款是收入，其余是支出）。
func jdStatementRows(sh importSheet) (period string, source string, items []model.EcStatementItem) {
	if isJDFees(sh) {
		source = model.StmtSourceReconcile
	} else {
		source = model.StmtSourceBill
	}
	for _, row := range sh.Rows {
		feeName := strings.TrimSpace(sh.cell(row, "费用项", "费用名称"))
		if feeName == "" {
			continue
		}
		kind := jdKind(feeName)
		direction := strings.TrimSpace(sh.cell(row, "收支方向", "费用分类"))
		switch direction {
		case "收入":
			direction = "income"
		case "支出":
			direction = "expense"
		default:
			direction = model.StmtDirection(kind)
		}
		// 金额在京东账单里带符号，取绝对值，方向由上面的字段决定
		amount := parseMoneyLoose(sh.cell(row, "金额"))
		if amount < 0 {
			amount = -amount
		}
		// 账期：月度账单有「账单日期」，对账中心没有，
		// 退回到「结算时间」按年月取（2026-09-14 12:07 → 202609）。
		p := strings.TrimSpace(sh.cell(row, "账单日期", "账期"))
		if p == "" {
			p = periodFromTime(sh.cell(row, "结算时间", "费用结算时间", "完成时间"))
		}
		if len(p) > 6 {
			p = p[:6]
		}
		if period == "" && len(p) == 6 {
			period = p
		}
		orderNo := sh.cell(row, "订单编号", "业务单据编号", "商户订单号")
		it := model.EcStatementItem{
			Period:      p,
			Kind:        kind,
			Direction:   direction,
			OrderNo:     orderNo,
			SubOrderNo:  orderNo,
			EcProductID: sh.cell(row, "商品编号", "商品ID"),
			Title:       sh.cell(row, "商品名称"),
			Qty:         parseQtyLoose(sh.cell(row, "商品数量", "数量")),
			Amount:      amount,
			OccurredAt:  sh.cell(row, "费用结算时间", "费用发生时间"),
			PayTime:     sh.cell(row, "费用结算时间"),
		}
		if it.Title == "" && it.OrderNo == "" {
			continue
		}
		items = append(items, it)
	}
	return period, source, items
}

// jdKind 京东费用项对应的账单类型。
//
// 直接沿用平台的费用名，前面加「京东·」区分平台——
// 这样平台出新费用项时不用改代码，界面上也能看出是京东的哪笔钱。
func jdKind(feeName string) string {
	name := strings.TrimSpace(feeName)
	switch name {
	case "货款", "货款收入":
		return model.StmtGoodsPayment // 与淘宝共用「交易货款」，收入口径一致
	}
	if name == "" {
		return "京东·其它"
	}
	return "京东·" + name
}

// jdOrderRow 京东订单明细 → 订单行。
func jdOrderRow(sh importSheet, row []string) service.EcOrderRow {
	orderNo := sh.cell(row, "订单号", "订单编号")
	return service.EcOrderRow{
		OrderNo:      orderNo,
		SubOrderNo:   orderNo,
		Title:        sh.cell(row, "商品名称", "商品标题"),
		EcProductID:  sh.cell(row, "商品ID", "商品编号"),
		MerchantCode: sh.cell(row, "商家SKUID", "货号"),
		Qty:          sh.cell(row, "订购数量", "商品数量"),
		UnitPrice:    sh.cell(row, "京东价", "商品单价"),
		PaidAmt:      sh.cell(row, "商家应收", "订单金额"),
		ItemStatus:   sh.cell(row, "订单状态"),
		CreatedAt:    sh.cell(row, "下单时间", "订单创建时间"),
		ShippedAt:    sh.cell(row, "订单出库时间"),
		LogisticsNo:  sh.cell(row, "运单号"),
		SellerNote:   sh.cell(row, "商家备注"),
	}
}

// periodFromTime 从时间文本里取账期（YYYYMM）。
//
// 支持 2026-09-14 12:07:44 / 2026/09/14 / 20260914000001 这几种写法。
func periodFromTime(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	// 只收数字、跳过 - / : 空格等分隔符，凑够 6 位（YYYYMM）就停。
	// 之前写成"遇到非数字且已有 4 位就 break"，
	// 结果 2026-09-14 只取到 2026（4 位），账期成了空。
	digits := make([]rune, 0, 8)
	for _, r := range v {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
			if len(digits) >= 6 {
				break
			}
		}
	}
	d := string(digits)
	if len(d) >= 6 {
		return d[:6]
	}
	return ""
}

package web

import (
	"fmt"
	"sort"

	"github.com/xuri/excelize/v2"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// buildSettlementWorkbook 生成结算单 Excel。
//
// 版式按"人能看懂"来排：先结论（应结多少），再构成（怎么算出来的），
// 最后明细（逐单 + 逐商品 + 平台费用），会计和上游都能直接看。
func buildSettlementWorkbook(rep *service.SettlementReport, platformLabel string, offlineOff *store.OfflineDetail, offlineOrders []store.RefundedOrder) (*excelize.File, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	moneyStyle, _ := f.NewStyle(&excelize.Style{
		NumFmt: 4, // #,##0.00
	})
	headStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"F2F2F2"}},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})
	titleStyle, _ := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Size: 14},
	})
	totalStyle, _ := f.NewStyle(&excelize.Style{
		Font:   &excelize.Font{Bold: true},
		NumFmt: 4,
	})

	total := rep.Total
	rates := rep.Rates

	// ---------------- Sheet 1 结算汇总 ----------------
	ws := "结算汇总"
	_ = f.SetSheetName("Sheet1", ws)
	idx := 1
	put := func(label string, amount model.Money, bold bool) {
		cellA := fmt.Sprintf("A%d", idx)
		cellB := fmt.Sprintf("B%d", idx)
		_ = f.SetCellValue(ws, cellA, label)
		_ = f.SetCellValue(ws, cellB, amount.Float())
		if bold {
			_ = f.SetCellStyle(ws, cellA, cellB, totalStyle)
		} else {
			_ = f.SetCellStyle(ws, cellB, cellB, moneyStyle)
		}
		idx++
	}
	_ = f.SetCellValue(ws, "A1", "PondView 销售对账表 "+settlementVolumeLabel(rep)+" · "+platformLabel+" 结算单")
	_ = f.SetCellStyle(ws, "A1", "A1", titleStyle)
	idx = 3

	put("① 货款（平台打款）", total.Goods, false)
	put("② 平台补贴", total.Subsidy, false)
	put("③ 平台代付扣回（从货款扣回）", -total.Advance, false)
	put("④ 销售收入 = ①+②−③", total.Revenue, true)
	put("⑤ 供货成本（上游结算价）", -total.Cost, false)
	put(fmt.Sprintf("⑥ 应交增值税（销项%.0f%% − 进项%.0f%% − 平台费%.0f%%抵扣）",
		rates.Output, rates.Input, rates.Platform), -total.VatPayable(rates), false)
	put("⑦ 应结金额 = ④−⑤−⑥", total.Net(rates), true)
	idx++
	_ = f.SetCellValue(ws, fmt.Sprintf("A%d", idx), "销售数量")
	_ = f.SetCellValue(ws, fmt.Sprintf("B%d", idx), total.Qty.Float())
	idx += 2

	// 计算过程（便于核对）
	_ = f.SetCellValue(ws, fmt.Sprintf("A%d", idx), "增值税计算过程")
	_ = f.SetCellStyle(ws, fmt.Sprintf("A%d", idx), fmt.Sprintf("A%d", idx), headStyle)
	idx++
	put("销项税 = 销售收入 ÷ 1."+fmt.Sprintf("%.0f", rates.Output)+" × "+fmt.Sprintf("%.0f%%", rates.Output), total.OutputVat(rates), false)
	put("进项税 = 供货成本 ÷ (1+税率) × 税率", -total.InputVat(rates), false)
	put("平台费抵扣 = 平台费用 ÷ (1+税率) × 税率", -total.PlatformVat(rates), false)
	put("应交增值税", total.VatPayable(rates), true)
	idx++
	_ = f.SetCellValue(ws, fmt.Sprintf("A%d", idx),
		"说明：运费仅展示；平台代付扣回从收入扣回。")
	_ = f.SetCellStyle(ws, fmt.Sprintf("A%d", idx), fmt.Sprintf("A%d", idx), totalStyle)
	_ = f.SetColWidth(ws, "A", "A", 46)
	_ = f.SetColWidth(ws, "B", "B", 16)

	// ---------------- Sheet 2 按商品汇总 ----------------
	ws2 := "按商品汇总"
	_, _ = f.NewSheet(ws2)
	heads2 := []string{"商品", "销量", "销售金额", "供货成本", "应交增值税", "结算金额", "毛利率"}
	for i, h := range heads2 {
		c, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(ws2, c, h)
	}
	_ = f.SetCellStyle(ws2, "A1", "G1", headStyle)
	var sumQty model.Qty
	var sumRev, sumCost, sumVat, sumNet model.Money
	for i, p := range rep.Products {
		r := i + 2
		vat := p.S.VatPayable(rates)
		net := p.S.Revenue - p.S.Cost - vat
		_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", r), p.Name)
		_ = f.SetCellValue(ws2, fmt.Sprintf("B%d", r), p.S.Qty.Float())
		_ = f.SetCellValue(ws2, fmt.Sprintf("C%d", r), p.S.Revenue.Float())
		_ = f.SetCellValue(ws2, fmt.Sprintf("D%d", r), p.S.Cost.Float())
		_ = f.SetCellValue(ws2, fmt.Sprintf("E%d", r), vat.Float())
		_ = f.SetCellValue(ws2, fmt.Sprintf("F%d", r), net.Float())
		if p.S.Revenue > 0 {
			_ = f.SetCellValue(ws2, fmt.Sprintf("G%d", r), model.Ratio(net, p.S.Revenue)/100)
		}
		_ = f.SetCellStyle(ws2, fmt.Sprintf("C%d", r), fmt.Sprintf("F%d", r), moneyStyle)
		_ = f.SetCellStyle(ws2, fmt.Sprintf("G%d", r), fmt.Sprintf("G%d", r),
			mustStyle(f, "0.0%"))
		sumQty += p.S.Qty
		sumRev += p.S.Revenue
		sumCost += p.S.Cost
		sumVat += vat
		sumNet += net
	}
	last := len(rep.Products) + 2
	_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", last), "合计")
	_ = f.SetCellValue(ws2, fmt.Sprintf("B%d", last), sumQty.Float())
	_ = f.SetCellValue(ws2, fmt.Sprintf("C%d", last), sumRev.Float())
	_ = f.SetCellValue(ws2, fmt.Sprintf("D%d", last), sumCost.Float())
	_ = f.SetCellValue(ws2, fmt.Sprintf("E%d", last), sumVat.Float())
	_ = f.SetCellValue(ws2, fmt.Sprintf("F%d", last), sumNet.Float())
	_ = f.SetCellStyle(ws2, fmt.Sprintf("A%d", last), fmt.Sprintf("F%d", last), totalStyle)
	_ = f.SetColWidth(ws2, "A", "A", 34)
	_ = f.SetColWidth(ws2, "B", "G", 14)

	// ---------------- Sheet 3 订单明细 ----------------
	ws3 := "订单明细"
	_, _ = f.NewSheet(ws3)
	heads3 := []string{"订单号", "商品", "平台商品ID", "数量", "销售金额",
		"单件供货价", "供货成本", "应交增值税", "结算金额"}
	for i, h := range heads3 {
		c, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(ws3, c, h)
	}
	_ = f.SetCellStyle(ws3, "A1", "I1", headStyle)
	for i, o := range rep.Orders {
		r := i + 2
		vat := o.S.VatPayable(rates)
		net := o.S.Revenue - o.S.Cost - vat
		_ = f.SetCellValue(ws3, fmt.Sprintf("A%d", r), o.OrderNo)
		_ = f.SetCellValue(ws3, fmt.Sprintf("B%d", r), o.Product)
		_ = f.SetCellValue(ws3, fmt.Sprintf("C%d", r), o.EcID)
		_ = f.SetCellValue(ws3, fmt.Sprintf("D%d", r), o.S.Qty.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("E%d", r), o.S.Revenue.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("F%d", r), o.UnitCost.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("G%d", r), o.S.Cost.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("H%d", r), vat.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("I%d", r), net.Float())
		_ = f.SetCellStyle(ws3, fmt.Sprintf("E%d", r), fmt.Sprintf("I%d", r), moneyStyle)
	}
	_ = f.SetColWidth(ws3, "A", "A", 22)
	_ = f.SetColWidth(ws3, "B", "B", 30)
	_ = f.SetColWidth(ws3, "C", "I", 14)

	// ---------------- Sheet 4 平台费用 ----------------
	ws4 := "平台费用"
	_, _ = f.NewSheet(ws4)
	_ = f.SetCellValue(ws4, "A1", "平台费用（开票部分可抵扣增值税；运费仅展示）")
	_ = f.SetCellStyle(ws4, "A1", "A1", headStyle)
	_ = f.SetCellValue(ws4, "A2", "费用类型")
	_ = f.SetCellValue(ws4, "B2", "金额")
	_ = f.SetCellStyle(ws4, "A2", "B2", headStyle)
	row := 3
	for _, kind := range model.StmtKinds {
		amt := rep.FeeByKind[kind]
		if amt == 0 {
			continue
		}
		_ = f.SetCellValue(ws4, fmt.Sprintf("A%d", row), model.StmtKindLabel(kind))
		_ = f.SetCellValue(ws4, fmt.Sprintf("B%d", row), amt.Float())
		_ = f.SetCellStyle(ws4, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), moneyStyle)
		row++
	}
	_ = f.SetCellValue(ws4, fmt.Sprintf("A%d", row), "合计")
	_ = f.SetCellValue(ws4, fmt.Sprintf("B%d", row), rep.Total.BorneFee.Float())
	_ = f.SetCellStyle(ws4, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), totalStyle)
	_ = f.SetColWidth(ws4, "A", "A", 34)
	_ = f.SetColWidth(ws4, "B", "B", 16)

	f.SetActiveSheet(0)
	// ---------------- 已排除订单（线下结算） ----------------
	//
	// 用户要求：只需列出订单与金额，其他不用写。
	if offlineOff != nil && offlineOff.Orders > 0 {
		of := offlineOff
		wso := "已排除订单"
		_, _ = f.NewSheet(wso)
		_ = f.SetCellValue(wso, "A1", "已排除订单（线下结算）—— 这些单在线下结算，不计入本次对账单")
		_ = f.SetCellStyle(wso, "A1", "A1", headStyle)
		_ = f.SetCellValue(wso, "A2", "订单号")
		_ = f.SetCellValue(wso, "B2", "商品")
		_ = f.SetCellValue(wso, "C2", "金额")
		_ = f.SetCellStyle(wso, "A2", "C2", headStyle)
		r := 3
		for _, o := range offlineOrders {
			_ = f.SetCellValue(wso, fmt.Sprintf("A%d", r), o.OrderNo)
			title := o.Title
			if title == "" {
				title = "商品ID " + o.EcProductID
			}
			_ = f.SetCellValue(wso, fmt.Sprintf("B%d", r), title)
			_ = f.SetCellValue(wso, fmt.Sprintf("C%d", r), o.Amount.Float())
			_ = f.SetCellStyle(wso, fmt.Sprintf("C%d", r), fmt.Sprintf("C%d", r), moneyStyle)
			r++
		}
		_ = f.SetCellValue(wso, fmt.Sprintf("A%d", r), fmt.Sprintf("合计 %d 单", len(offlineOrders)))
		_ = f.SetCellValue(wso, fmt.Sprintf("C%d", r), of.Goods.Float())
		_ = f.SetCellStyle(wso, fmt.Sprintf("A%d", r), fmt.Sprintf("C%d", r), totalStyle)
		_ = f.SetColWidth(wso, "A", "A", 24)
		_ = f.SetColWidth(wso, "B", "B", 52)
		_ = f.SetColWidth(wso, "C", "C", 16)
	}

	return f, nil
}

func mustStyle(f *excelize.File, numFmt string) int {
	st, _ := f.NewStyle(&excelize.Style{NumFmt: 10, CustomNumFmt: &numFmt})
	return st
}

// settlementVolumeLabel 期次标识（与导出文件名同一口径：Vol.2609）。
func settlementVolumeLabel(rep *service.SettlementReport) string {
	ps := append([]string{}, rep.Periods2...)
	if len(ps) == 0 && rep.Period != "" {
		ps = append(ps, rep.Period)
	}
	if len(ps) == 0 {
		return ""
	}
	sort.Strings(ps)
	if len(ps) == 1 {
		return "Vol." + yymm(ps[0])
	}
	return "Vol." + yymm(ps[0]) + "-" + yymm(ps[len(ps)-1])
}

package web

import (
	"fmt"

	"github.com/xuri/excelize/v2"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
)

// buildSummaryWorkbook 生成多平台合并对账单。
//
// 用户的实际节奏：淘宝按月结，其它平台可能两三个月结一次，
// 所以这张表按「平台 + 账期」逐行列示，最后给一行总计，
// 一次把不同平台、不同账期都装进去，直接发给上游结算。
func buildSummaryWorkbook(rep *service.SummaryReport, det *service.SummaryDetail) (*excelize.File, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	money, _ := f.NewStyle(&excelize.Style{NumFmt: 4})
	pct, _ := f.NewStyle(&excelize.Style{CustomNumFmt: strPtr("0.0%")})
	head, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"F2F2F2"}},
		Alignment: &excelize.Alignment{Horizontal: "center"},
	})
	title, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}, NumFmt: 4})

	// ---------------- 表1 各平台结算 ----------------
	ws := "各平台结算"
	_ = f.SetSheetName("Sheet1", ws)
	_ = f.SetCellValue(ws, "A1", "电商多平台销售对账单")
	_ = f.SetCellStyle(ws, "A1", "A1", title)
	_ = f.SetCellValue(ws, "A2", fmt.Sprintf(
		"税率：销项 %.0f%% · 进项 %.0f%% · 平台费专票 %.0f%%；只承担成本以上的增值税，运费不计入结算",
		rep.Rates.Output, rep.Rates.Input, rep.Rates.Platform))

	heads := []string{"平台", "账期", "销量", "销售收入", "平台补贴",
		"垫支扣回", "供货成本", "平台费用", "应交增值税", "应结金额", "毛利率"}
	for i, h := range heads {
		c, _ := excelize.CoordinatesToCellName(i+1, 4)
		_ = f.SetCellValue(ws, c, h)
	}
	_ = f.SetCellStyle(ws, "A4", "K4", head)

	r := 5
	for _, it := range rep.Items {
		t := it.Total
		net := t.Net(rep.Rates)
		_ = f.SetCellValue(ws, fmt.Sprintf("A%d", r), it.Label)
		_ = f.SetCellValue(ws, fmt.Sprintf("B%d", r), model.PeriodLabel(it.Period))
		_ = f.SetCellValue(ws, fmt.Sprintf("C%d", r), t.Qty.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("D%d", r), t.Revenue.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("E%d", r), t.Subsidy.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("F%d", r), -t.Advance.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("G%d", r), t.Cost.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("H%d", r), t.PlatformFee.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("I%d", r), t.VatPayable(rep.Rates).Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("J%d", r), net.Float())
		if t.Revenue > 0 {
			_ = f.SetCellValue(ws, fmt.Sprintf("K%d", r), model.Ratio(net, t.Revenue)/100)
		}
		_ = f.SetCellStyle(ws, fmt.Sprintf("D%d", r), fmt.Sprintf("J%d", r), money)
		_ = f.SetCellStyle(ws, fmt.Sprintf("K%d", r), fmt.Sprintf("K%d", r), pct)
		r++
	}
	tt := rep.Total
	_ = f.SetCellValue(ws, fmt.Sprintf("A%d", r), "合计")
	_ = f.SetCellValue(ws, fmt.Sprintf("B%d", r), "")
	_ = f.SetCellValue(ws, fmt.Sprintf("C%d", r), tt.Qty.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("D%d", r), tt.Revenue.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("E%d", r), tt.Subsidy.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("F%d", r), -tt.Advance.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("G%d", r), tt.Cost.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("H%d", r), tt.PlatformFee.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("I%d", r), tt.VatPayable(rep.Rates).Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("J%d", r), tt.Net(rep.Rates).Float())
	_ = f.SetCellStyle(ws, fmt.Sprintf("A%d", r), fmt.Sprintf("J%d", r), bold)

	// 增值税计算过程
	r += 2
	_ = f.SetCellValue(ws, fmt.Sprintf("A%d", r), "增值税计算过程（合计）")
	_ = f.SetCellStyle(ws, fmt.Sprintf("A%d", r), fmt.Sprintf("A%d", r), head)
	r++
	steps := [][2]string{
		{"销项税 = 销售收入 ÷ (1+销项率) × 销项率", fmt.Sprintf("%.2f", tt.OutputVat(rep.Rates).Float())},
		{"进项税 = 供货成本 ÷ (1+进项率) × 进项率", fmt.Sprintf("%.2f", -tt.InputVat(rep.Rates).Float())},
		{"平台费抵扣 = 平台费用 ÷ (1+专票率) × 专票率", fmt.Sprintf("%.2f", -tt.PlatformVat(rep.Rates).Float())},
		{"应交增值税", fmt.Sprintf("%.2f", tt.VatPayable(rep.Rates).Float())},
	}
	for _, s2 := range steps {
		_ = f.SetCellValue(ws, fmt.Sprintf("A%d", r), s2[0])
		_ = f.SetCellValue(ws, fmt.Sprintf("D%d", r), s2[1])
		r++
	}
	_ = f.SetColWidth(ws, "A", "A", 40)
	_ = f.SetColWidth(ws, "B", "K", 15)

	// ---------------- 表2 平台费用矩阵 ----------------
	// 一行一个费用类型，一列一个平台，最后合计 ——
	// 这样"哪个平台的哪笔费用"一眼能看清。
	ws2 := "平台费用汇总"
	_, _ = f.NewSheet(ws2)
	_ = f.SetCellValue(ws2, "A1", "平台费用汇总（开票部分可抵扣增值税；商家寄件服务费仅列示不计入结算）")
	_ = f.SetCellStyle(ws2, "A1", "A1", head)
	m := rep.Fees
	_ = f.SetCellValue(ws2, "A2", "费用类型")
	col := 2
	for _, p := range m.Platforms {
		c, _ := excelize.CoordinatesToCellName(col, 2)
		_ = f.SetCellValue(ws2, c, model.EcPlatformLabel(p))
		col++
	}
	totalCol, _ := excelize.CoordinatesToCellName(col, 2)
	_ = f.SetCellValue(ws2, totalCol, "合计")
	_ = f.SetCellStyle(ws2, "A2", totalCol+"2", head)

	row := 3
	for _, kind := range m.Kinds {
		_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", row), model.StmtKindLabel(kind))
		col = 2
		for _, p := range m.Platforms {
			amt := m.Cell[kind+"|"+p]
			c, _ := excelize.CoordinatesToCellName(col, row)
			if amt != 0 {
				_ = f.SetCellValue(ws2, c, amt.Float())
				_ = f.SetCellStyle(ws2, c, c, money)
			}
			col++
		}
		tc, _ := excelize.CoordinatesToCellName(col, row)
		_ = f.SetCellValue(ws2, tc, m.KindTotal[kind].Float())
		_ = f.SetCellStyle(ws2, tc, tc, money)
		row++
	}
	_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", row), "合计")
	col = 2
	for _, p := range m.Platforms {
		c, _ := excelize.CoordinatesToCellName(col, row)
		_ = f.SetCellValue(ws2, c, m.PlatTotal[p].Float())
		col++
	}
	tc, _ := excelize.CoordinatesToCellName(col, row)
	_ = f.SetCellValue(ws2, tc, m.Total.Float())
	_ = f.SetCellStyle(ws2, fmt.Sprintf("A%d", row), tc+fmt.Sprintf("%d", row), bold)
	_ = f.SetColWidth(ws2, "A", "A", 30)
	_ = f.SetColWidth(ws2, "B", "Z", 16)

	// ---------------- 表3 订单明细 ----------------
	// 汇总之外必须能看到每一单：对不上账时才有得核。
	ws3 := "订单明细"
	_, _ = f.NewSheet(ws3)
	_ = f.SetCellValue(ws3, "A1", "订单明细（销售收入 = 货款 + 补贴 − 垫支；应结 = 销售收入 − 供货成本 − 应交增值税）")
	_ = f.SetCellStyle(ws3, "A1", "A1", head)
	oHeads := []string{"平台", "账期", "订单号", "商品 / 产品", "商品ID", "数量",
		"货款", "平台补贴", "垫支扣回", "销售收入", "平台费用", "供货成本", "应交增值税", "应结金额"}
	for i, h := range oHeads {
		c, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue(ws3, c, h)
	}
	_ = f.SetCellStyle(ws3, "A2", "N2", head)
	orow := 3
	for _, o := range det.Orders {
		_ = f.SetCellValue(ws3, fmt.Sprintf("A%d", orow), model.EcPlatformLabel(o.Platform))
		_ = f.SetCellValue(ws3, fmt.Sprintf("B%d", orow), model.PeriodLabel(o.Period))
		_ = f.SetCellValue(ws3, fmt.Sprintf("C%d", orow), o.OrderNo)
		_ = f.SetCellValue(ws3, fmt.Sprintf("D%d", orow), o.Product)
		_ = f.SetCellValue(ws3, fmt.Sprintf("E%d", orow), o.EcID)
		_ = f.SetCellValue(ws3, fmt.Sprintf("F%d", orow), o.Qty.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("G%d", orow), o.Goods.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("H%d", orow), o.Subsidy.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("I%d", orow), o.Advance.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("J%d", orow), o.Revenue.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("K%d", orow), o.PlatformFee.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("L%d", orow), o.Cost.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("M%d", orow), o.Vat.Float())
		_ = f.SetCellValue(ws3, fmt.Sprintf("N%d", orow), (o.Revenue - o.Cost - o.Vat).Float())
		_ = f.SetCellStyle(ws3, fmt.Sprintf("G%d", orow), fmt.Sprintf("N%d", orow), money)
		orow++
	}
	// 合计行（与汇总表对得上）
	if orow > 3 {
		_ = f.SetCellValue(ws3, fmt.Sprintf("A%d", orow), "合计")
		for _, col := range []string{"F", "G", "H", "I", "J", "K", "L", "M", "N"} {
			_ = f.SetCellFormula(ws3, fmt.Sprintf("%s%d", col, orow),
				fmt.Sprintf("SUM(%s3:%s%d)", col, col, orow-1))
		}
		_ = f.SetCellStyle(ws3, fmt.Sprintf("A%d", orow), fmt.Sprintf("N%d", orow), bold)
		_ = f.SetCellStyle(ws3, fmt.Sprintf("G%d", orow), fmt.Sprintf("N%d", orow), money)
	}
	_ = f.SetColWidth(ws3, "A", "B", 12)
	_ = f.SetColWidth(ws3, "C", "E", 20)
	_ = f.SetColWidth(ws3, "F", "N", 14)

	// ---------------- 表4 账单费用明细 ----------------
	// 最细的一层：账单原始每一行都在这里，可以对回平台账单。
	ws4 := "账单费用明细"
	_, _ = f.NewSheet(ws4)
	_ = f.SetCellValue(ws4, "A1", "账单费用明细（平台账单原始每一行）")
	_ = f.SetCellStyle(ws4, "A1", "A1", head)
	iHeads := []string{"平台", "账期", "费用项", "收/支", "订单号", "商品ID", "商品名称", "数量", "金额", "来源"}
	for i, h := range iHeads {
		c, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue(ws4, c, h)
	}
	_ = f.SetCellStyle(ws4, "A2", "J2", head)
	irow := 3
	for _, it := range det.Items {
		_ = f.SetCellValue(ws4, fmt.Sprintf("A%d", irow), model.EcPlatformLabel(it.Platform))
		_ = f.SetCellValue(ws4, fmt.Sprintf("B%d", irow), model.PeriodLabel(it.Period))
		_ = f.SetCellValue(ws4, fmt.Sprintf("C%d", irow), model.StmtKindLabel(it.Kind))
		dir := "收入"
		if it.Direction == "expense" {
			dir = "支出"
		}
		_ = f.SetCellValue(ws4, fmt.Sprintf("D%d", irow), dir)
		_ = f.SetCellValue(ws4, fmt.Sprintf("E%d", irow), it.OrderNo)
		_ = f.SetCellValue(ws4, fmt.Sprintf("F%d", irow), it.EcID)
		_ = f.SetCellValue(ws4, fmt.Sprintf("G%d", irow), it.Title)
		_ = f.SetCellValue(ws4, fmt.Sprintf("H%d", irow), it.Qty.Float())
		_ = f.SetCellValue(ws4, fmt.Sprintf("I%d", irow), it.Amount.Float())
		_ = f.SetCellValue(ws4, fmt.Sprintf("J%d", irow), model.SourceLabel(it.Source))
		_ = f.SetCellStyle(ws4, fmt.Sprintf("I%d", irow), fmt.Sprintf("I%d", irow), money)
		irow++
	}
	_ = f.SetColWidth(ws4, "A", "J", 18)

	// ---------------- 表5 按商品汇总 ----------------
	ws5 := "按商品汇总"
	_, _ = f.NewSheet(ws5)
	_ = f.SetCellValue(ws5, "A1", "按商品汇总（同一产品的多个平台链接已合并）")
	_ = f.SetCellStyle(ws5, "A1", "A1", head)
	pHeads := []string{"商品 / 产品", "数量", "销售收入", "供货成本", "平台费用", "应交增值税", "应结金额", "毛利率"}
	for i, h := range pHeads {
		c, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue(ws5, c, h)
	}
	_ = f.SetCellStyle(ws5, "A2", "H2", head)
	prow := 3
	for _, it := range rep.Items {
		for _, pr := range it.Products {
			net := pr.S.Net(rep.Rates)
			_ = f.SetCellValue(ws5, fmt.Sprintf("A%d", prow), pr.Name)
			_ = f.SetCellValue(ws5, fmt.Sprintf("B%d", prow), pr.S.Qty.Float())
			_ = f.SetCellValue(ws5, fmt.Sprintf("C%d", prow), pr.S.Revenue.Float())
			_ = f.SetCellValue(ws5, fmt.Sprintf("D%d", prow), pr.S.Cost.Float())
			_ = f.SetCellValue(ws5, fmt.Sprintf("E%d", prow), pr.S.PlatformFee.Float())
			_ = f.SetCellValue(ws5, fmt.Sprintf("F%d", prow), pr.S.VatPayable(rep.Rates).Float())
			_ = f.SetCellValue(ws5, fmt.Sprintf("G%d", prow), net.Float())
			if pr.S.Revenue > 0 {
				_ = f.SetCellValue(ws5, fmt.Sprintf("H%d", prow), model.Ratio(net, pr.S.Revenue)/100)
			}
			_ = f.SetCellStyle(ws5, fmt.Sprintf("C%d", prow), fmt.Sprintf("G%d", prow), money)
			_ = f.SetCellStyle(ws5, fmt.Sprintf("H%d", prow), fmt.Sprintf("H%d", prow), pct)
			prow++
		}
	}
	_ = f.SetColWidth(ws5, "A", "A", 34)
	_ = f.SetColWidth(ws5, "B", "H", 15)

	f.SetActiveSheet(0)
	return f, nil
}

func strPtr(s string) *string { return &s }

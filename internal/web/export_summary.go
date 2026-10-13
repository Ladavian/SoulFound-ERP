package web

import (
	"fmt"
	"sort"

	"github.com/xuri/excelize/v2"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// buildSummaryWorkbook 生成多平台合并对账单。
//
// 用户的实际节奏：淘宝按月结，其它平台可能两三个月结一次，
// 所以这张表按「平台 + 账期」逐行列示，最后给一行总计，
// 一次把不同平台、不同账期都装进去，直接发给上游结算。
func buildSummaryWorkbook(rep *service.SummaryReport, det *service.SummaryDetail, offline *store.OfflineDetail, gifts []store.BrandGiftRow) (*excelize.File, error) {
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
	// 应结金额要一眼能看到：加粗 + 底色
	netStyle, _ := f.NewStyle(&excelize.Style{
		Font:   &excelize.Font{Bold: true, Color: "1F5C2E"},
		Fill:   excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"E6F4EA"}},
		NumFmt: 4,
	})

	// ---------------- 表1 各平台结算 ----------------
	ws := "各平台结算"
	_ = f.SetSheetName("Sheet1", ws)
	// 标题与文件名一致，文件转发出去也能一眼认出是哪份。
	_ = f.SetCellValue(ws, "A1", "PondView 销售对账表 "+volumeLabel(rep))
	_ = f.SetCellStyle(ws, "A1", "A1", title)
	_ = f.SetCellValue(ws, "A2", fmt.Sprintf(
		"税率：销项 %.0f%% · 进项 %.0f%% · 平台费专票 %.0f%%；运费不计入结算",
		rep.Rates.Output, rep.Rates.Input, rep.Rates.Platform))

	// 列顺序（后面的列由前面的列用公式算出来，点单元格即可看到推导）：
	//   A 平台 B 账期 C 销量
	//   D 货款 E 平台补贴 F 平台代付扣回
	//   G 销售收入 = D+E−F
	//   H 供货成本 I 平台费用（承担）
	//   J 增值税抵扣 = I/(1+专票率)×专票率
	//   K 应交增值税 = (销项 − 进项 − 抵扣)，销项/进项在这里就地算，
	//                 不再单列两列（用户要求首页不要放这些中间量）
	//   L 应结金额 = D+E−F−H−I−K   ← 把平台代付扣回显式写进去
	//   M 毛利率 = L/G
	heads := []string{"平台", "账期", "销量", "货款", "平台补贴", "平台代付扣回",
		"销售收入", "供货成本", "平台费用", "增值税抵扣", "应交增值税", "应结金额", "毛利率"}
	for i, h := range heads {
		c, _ := excelize.CoordinatesToCellName(i+1, 4)
		_ = f.SetCellValue(ws, c, h)
	}
	_ = f.SetCellStyle(ws, "A4", "M4", head)
	// 列与列之间的等式写清楚，避免手算漏项
	_ = f.SetCellValue(ws, "A3",
		"列间等式：销售收入 = 货款 + 平台补贴 − 平台代付扣回；"+
			"应结金额 = 销售收入 − 平台费用 − 供货成本 − 应交增值税")

	r := 5
	for _, it := range rep.Items {
		t := it.Total
		_ = f.SetCellValue(ws, fmt.Sprintf("A%d", r), it.Label)
		_ = f.SetCellValue(ws, fmt.Sprintf("B%d", r), model.PeriodLabel(it.Period))
		_ = f.SetCellValue(ws, fmt.Sprintf("C%d", r), t.Qty.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("D%d", r), t.Goods.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("E%d", r), t.Subsidy.Float())
		// 这一列存**正数**（要减掉的金额），与供货成本、平台费用一致。
		// 之前存的是负数 −157，而公式写的是 "− F"，
		// 减负数等于加回去，销售收入和应结都被算高（Excel 里算出 2610.75）。
		_ = f.SetCellValue(ws, fmt.Sprintf("F%d", r), t.Advance.Float())
		// 销售收入与后面所有列都用公式互相引用
		_ = f.SetCellFormula(ws, fmt.Sprintf("G%d", r), fmt.Sprintf("D%d+E%d-F%d", r, r, r))
		_ = f.SetCellValue(ws, fmt.Sprintf("H%d", r), t.Cost.Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("I%d", r), t.BorneFee.Float())
		out := rep.Rates.Output / 100
		in := rep.Rates.Input / 100
		plat := rep.Rates.Platform / 100
		_ = f.SetCellFormula(ws, fmt.Sprintf("J%d", r),
			fmt.Sprintf("I%d/(1+%g)*%g", r, plat, plat))
		// 销项/进项就地算，不单列；抵扣用 J 列
		_ = f.SetCellFormula(ws, fmt.Sprintf("K%d", r),
			fmt.Sprintf("G%d/(1+%g)*%g-H%d/(1+%g)*%g-J%d", r, out, out, r, in, in, r))
		// 应结金额把平台代付扣回显式写进去，避免看起来像漏了
		_ = f.SetCellFormula(ws, fmt.Sprintf("L%d", r),
			fmt.Sprintf("D%d+E%d-F%d-H%d-I%d-K%d", r, r, r, r, r, r))
		_ = f.SetCellFormula(ws, fmt.Sprintf("M%d", r), fmt.Sprintf("L%d/G%d", r, r))
		_ = f.SetCellStyle(ws, fmt.Sprintf("D%d", r), fmt.Sprintf("F%d", r), money)
		_ = f.SetCellStyle(ws, fmt.Sprintf("H%d", r), fmt.Sprintf("I%d", r), money)
		_ = f.SetCellStyle(ws, fmt.Sprintf("J%d", r), fmt.Sprintf("K%d", r), money)
		_ = f.SetCellStyle(ws, fmt.Sprintf("L%d", r), fmt.Sprintf("L%d", r), netStyle)
		_ = f.SetCellStyle(ws, fmt.Sprintf("M%d", r), fmt.Sprintf("M%d", r), pct)
		r++
	}
	tt := rep.Total
	_ = f.SetCellValue(ws, fmt.Sprintf("A%d", r), "合计")
	_ = f.SetCellValue(ws, fmt.Sprintf("C%d", r), tt.Qty.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("D%d", r), tt.Goods.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("E%d", r), tt.Subsidy.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("F%d", r), tt.Advance.Float())
	_ = f.SetCellFormula(ws, fmt.Sprintf("G%d", r), fmt.Sprintf("D%d+E%d-F%d", r, r, r))
	_ = f.SetCellValue(ws, fmt.Sprintf("H%d", r), tt.Cost.Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("I%d", r), tt.BorneFee.Float())
	_ = f.SetCellFormula(ws, fmt.Sprintf("J%d", r),
		fmt.Sprintf("I%d/(1+%g)*%g", r, rep.Rates.Platform/100, rep.Rates.Platform/100))
	_ = f.SetCellFormula(ws, fmt.Sprintf("K%d", r),
		fmt.Sprintf("G%d/(1+%g)*%g-H%d/(1+%g)*%g-J%d", r, rep.Rates.Output/100, rep.Rates.Output/100,
			r, rep.Rates.Input/100, rep.Rates.Input/100, r))
	_ = f.SetCellFormula(ws, fmt.Sprintf("L%d", r),
		fmt.Sprintf("D%d+E%d-F%d-H%d-I%d-K%d", r, r, r, r, r, r))
	_ = f.SetCellStyle(ws, fmt.Sprintf("A%d", r), fmt.Sprintf("K%d", r), bold)
	_ = f.SetCellStyle(ws, fmt.Sprintf("D%d", r), fmt.Sprintf("F%d", r), bold)
	_ = f.SetCellStyle(ws, fmt.Sprintf("H%d", r), fmt.Sprintf("I%d", r), bold)
	_ = f.SetCellStyle(ws, fmt.Sprintf("L%d", r), fmt.Sprintf("L%d", r), netStyle)

	_ = f.SetColWidth(ws, "A", "A", 40)
	_ = f.SetColWidth(ws, "B", "M", 15)

	// ---------------- 表2 平台费用矩阵 ----------------
	// 一行一个费用类型，一列一个平台，最后合计 ——
	// 这样"哪个平台的哪笔费用"一眼能看清。
	ws2 := "平台费用汇总"
	_, _ = f.NewSheet(ws2)
	_ = f.SetCellValue(ws2, "A1", "平台费用汇总（开票部分可抵扣增值税；商家寄件服务费仅展示）")
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
	// 仅展示的费用单独列在下面，不计入上面的合计
	if m.ListedTotal != 0 {
		row += 2
		_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", row), "以下仅展示，不计入上面的费用合计")
		_ = f.SetCellStyle(ws2, fmt.Sprintf("A%d", row), fmt.Sprintf("A%d", row), bold)
		row++
		for _, kind := range m.ListedKinds {
			_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", row), model.StmtKindLabel(kind))
			_ = f.SetCellValue(ws2, fmt.Sprintf("B%d", row), m.Listed[kind].Float())
			_ = f.SetCellStyle(ws2, fmt.Sprintf("B%d", row), fmt.Sprintf("B%d", row), money)
			row++
		}
		_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", row), "展示合计")
		_ = f.SetCellValue(ws2, fmt.Sprintf("B%d", row), m.ListedTotal.Float())
		_ = f.SetCellStyle(ws2, fmt.Sprintf("A%d", row), fmt.Sprintf("B%d", row), bold)
	}
	_ = f.SetColWidth(ws2, "A", "A", 30)
	// 增值税计算过程（合计）——用户要求放在平台费用汇总里，首页不放这些中间量
	{
		vr := row + 3
		_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", vr), "增值税计算过程（合计）")
		_ = f.SetCellStyle(ws2, fmt.Sprintf("A%d", vr), fmt.Sprintf("A%d", vr), head)
		vr++
		tt2 := rep.Total
		steps := [][2]string{
			{"销项税额 = 销售收入 ÷ (1+销项率) × 销项率", fmt.Sprintf("%.2f", tt2.OutputVat(rep.Rates).Float())},
			{"减：进项税额 = 供货成本 ÷ (1+进项率) × 进项率", fmt.Sprintf("-%.2f", tt2.InputVat(rep.Rates).Float())},
			{"减：平台费抵扣 = 平台费用 ÷ (1+专票率) × 专票率", fmt.Sprintf("-%.2f", tt2.PlatformVat(rep.Rates).Float())},
			{"应交增值税", fmt.Sprintf("%.2f", tt2.VatPayable(rep.Rates).Float())},
		}
		for _, st := range steps {
			_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", vr), st[0])
			_ = f.SetCellValue(ws2, fmt.Sprintf("B%d", vr), st[1])
			vr++
		}
		_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", vr), "（明细见「对账校验」表与该平台的账单费用明细）")
	}

	_ = f.SetColWidth(ws2, "B", "Z", 16)

	// ---------------- 表3 订单明细 ----------------
	// 汇总之外必须能看到每一单：对不上账时才有得核。
	ws3 := "订单明细"
	_, _ = f.NewSheet(ws3)
	_ = f.SetCellValue(ws3, "A1", "订单明细（销售收入 = 货款 + 补贴 − 垫支；应结 = 销售收入 − 供货成本 − 应交增值税）")
	_ = f.SetCellStyle(ws3, "A1", "A1", head)
	oHeads := []string{"平台", "账期", "订单号", "商品 / 产品", "商品ID", "数量",
		"货款", "平台补贴", "平台代付扣回", "销售收入", "平台费用", "供货成本", "应交增值税", "应结金额"}
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
	iHeads := []string{"平台", "账期", "费用项", "收/支", "订单号", "商品ID",
		"商品名称", "数量", "金额", "退款金额", "来源"}
	for i, h := range iHeads {
		c, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue(ws4, c, h)
	}
	_ = f.SetCellStyle(ws4, "A2", "K2", head)
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
		_ = f.SetCellValue(ws4, fmt.Sprintf("J%d", irow), (-it.Refund).Float())
		_ = f.SetCellValue(ws4, fmt.Sprintf("K%d", irow), model.SourceLabel(it.Source))
		_ = f.SetCellStyle(ws4, fmt.Sprintf("I%d", irow), fmt.Sprintf("J%d", irow), money)
		irow++
	}
	_ = f.SetColWidth(ws4, "A", "K", 18)

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
			_ = f.SetCellValue(ws5, fmt.Sprintf("E%d", prow), pr.S.BorneFee.Float())
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

	// ---------------- 表6 已排除订单（线下结算） ----------------
	// 用户要求：导出的总对账单也要体现被排除的线下结算订单，
	// 收入和支出都要说明白。
	if offline != nil && offline.Orders > 0 {
		of := offline
		ws6 := "已排除订单"
		_, _ = f.NewSheet(ws6)
		_ = f.SetCellValue(ws6, "A1", "已排除订单（线下结算）—— 本次结算的收入与支出都不含这些单")
		_ = f.SetCellStyle(ws6, "A1", "A1", head)
		_ = f.SetCellValue(ws6, "A2", "项目")
		_ = f.SetCellValue(ws6, "B2", "说明")
		_ = f.SetCellValue(ws6, "C2", "金额")
		_ = f.SetCellStyle(ws6, "A2", "C2", head)

		orow := 3
		put := func(name, note string, amt float64, style int) {
			_ = f.SetCellValue(ws6, fmt.Sprintf("A%d", orow), name)
			_ = f.SetCellValue(ws6, fmt.Sprintf("B%d", orow), note)
			_ = f.SetCellValue(ws6, fmt.Sprintf("C%d", orow), amt)
			if style != 0 {
				_ = f.SetCellStyle(ws6, fmt.Sprintf("C%d", orow), fmt.Sprintf("C%d", orow), style)
			}
			orow++
		}
		put("订单数", fmt.Sprintf("共 %d 单 · %s 件", of.Orders, of.Qty.String()), 0, 0)
		put("货款", "这些单的平台打款", of.Goods.Float(), money)
		if of.Subsidy != 0 {
			put("平台补贴", "淘金币等", of.Subsidy.Float(), money)
		}
		if of.Advance != 0 {
			put("平台代付垫支", "从货款扣回", -of.Advance.Float(), money)
		}
		put("收入合计（销售收入）", "货款 + 补贴 − 垫支", of.Revenue.Float(), bold)
		var kinds []string
		for k := range of.FeeByKind {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			put(model.StmtKindLabel(k), "支出 · 已排除", -of.FeeByKind[k].Float(), money)
		}
		if of.FeeTotal != 0 {
			put("支出合计（平台费用）", fmt.Sprintf("共 %d 行", of.FeeRows), -of.FeeTotal.Float(), bold)
		}
		orow++
		_ = f.SetCellValue(ws6, fmt.Sprintf("A%d", orow), "收入侧对账")
		_ = f.SetCellValue(ws6, fmt.Sprintf("B%d", orow),
			fmt.Sprintf("平台账单全部货款 %s − 已排除货款 %s = 本次结算货款 %s",
				rep.Total.Goods.Add(of.Goods).String(), of.Goods.String(), rep.Total.Goods.String()))
		orow++
		_ = f.SetCellValue(ws6, fmt.Sprintf("A%d", orow), "支出侧对账")
		_ = f.SetCellValue(ws6, fmt.Sprintf("B%d", orow),
			fmt.Sprintf("平台账单全部费用 %s − 已排除费用 %s = 本次结算费用 %s",
				rep.Total.BorneFee.Add(of.FeeTotal).String(), of.FeeTotal.String(), rep.Total.BorneFee.String()))
		_ = f.SetColWidth(ws6, "A", "A", 22)
		_ = f.SetColWidth(ws6, "B", "B", 82)
		_ = f.SetColWidth(ws6, "C", "C", 16)
	}

	// ---------------- 表7 品牌新享礼金明细 ----------------
	// 用户要能查：这笔礼金是哪一单、平台收了多少服务费、代扣了多少。
	// 账单原文里 抽佣金额 = 礼金 + 服务费，账单金额 = 服务费，
	// 所以 代付扣回 = 抽佣金额 − 账单金额。
	if len(gifts) > 0 {
		ws7 := "品牌新享礼金"
		_, _ = f.NewSheet(ws7)
		_ = f.SetCellValue(ws7, "A1", "品牌新享礼金明细（代付扣回 = 抽佣金额 − 账单金额）")
		_ = f.SetCellStyle(ws7, "A1", "A1", head)
		gHeads := []string{"账期", "订单号", "抽佣金额", "账单金额（服务费）",
			"代付扣回", "商品名称"}
		for i, h := range gHeads {
			c, _ := excelize.CoordinatesToCellName(i+1, 2)
			_ = f.SetCellValue(ws7, c, h)
		}
		_ = f.SetCellStyle(ws7, "A2", "F2", head)
		grow := 3
		var sumComm, sumFee, sumAdv float64
		for _, g := range gifts {
			_ = f.SetCellValue(ws7, fmt.Sprintf("A%d", grow), model.PeriodLabel(g.Period))
			_ = f.SetCellValue(ws7, fmt.Sprintf("B%d", grow), g.OrderNo)
			_ = f.SetCellValue(ws7, fmt.Sprintf("C%d", grow), g.Commission.Float())
			_ = f.SetCellValue(ws7, fmt.Sprintf("D%d", grow), g.ServiceFee.Float())
			// 代扣写成公式，点开就看得到关系
			_ = f.SetCellFormula(ws7, fmt.Sprintf("E%d", grow),
				fmt.Sprintf("C%d-D%d", grow, grow))
			_ = f.SetCellValue(ws7, fmt.Sprintf("F%d", grow), g.Title)
			_ = f.SetCellStyle(ws7, fmt.Sprintf("C%d", grow), fmt.Sprintf("E%d", grow), money)
			sumComm += g.Commission.Float()
			sumFee += g.ServiceFee.Float()
			sumAdv += g.Advance.Float()
			grow++
		}
		_ = f.SetCellValue(ws7, fmt.Sprintf("A%d", grow), "合计")
		_ = f.SetCellValue(ws7, fmt.Sprintf("C%d", grow), sumComm)
		_ = f.SetCellValue(ws7, fmt.Sprintf("D%d", grow), sumFee)
		_ = f.SetCellValue(ws7, fmt.Sprintf("E%d", grow), sumAdv)
		_ = f.SetCellStyle(ws7, fmt.Sprintf("A%d", grow), fmt.Sprintf("E%d", grow), bold)
		_ = f.SetCellStyle(ws7, fmt.Sprintf("C%d", grow), fmt.Sprintf("E%d", grow), money)
		_ = f.SetColWidth(ws7, "A", "B", 22)
		_ = f.SetColWidth(ws7, "C", "E", 18)
		_ = f.SetColWidth(ws7, "F", "F", 40)
	}

	// ---------------- 表8 对账校验 ----------------
	// 表1 的每一列都能在明细表里查到，避免"数字不知道哪来的"。
	ws8 := "对账校验"
	_, _ = f.NewSheet(ws8)
	_ = f.SetCellValue(ws8, "A1", "表1「各平台结算」每列的来源（都能在明细表里逐条查到）")
	_ = f.SetCellStyle(ws8, "A1", "A1", head)
	for i, h := range []string{"表1 的列", "来源明细", "合计"} {
		c, _ := excelize.CoordinatesToCellName(i+1, 2)
		_ = f.SetCellValue(ws8, c, h)
	}
	_ = f.SetCellStyle(ws8, "A2", "C2", head)
	lines := [][3]string{
		{"货款", "账单费用明细 · 交易货款行", tt.Goods.String()},
		{"平台补贴", "账单费用明细 · 补贴类行", tt.Subsidy.String()},
		{"平台代付扣回", "品牌新享礼金 · 代付扣回合计", tt.Advance.String()},
		{"销售收入", "= 货款 + 平台补贴 − 平台代付扣回", tt.Revenue.String()},
		{"供货成本", "订单明细 · 逐单 数量 × 电商成本", tt.Cost.String()},
		{"平台费用", "平台费用汇总 · 承担的费用合计", tt.BorneFee.String()},
		{"销项税额", "= 销售收入 ÷ (1+销项率) × 销项率", tt.OutputVat(rep.Rates).String()},
		{"进项税额", "= 供货成本 ÷ (1+进项率) × 进项率", tt.InputVat(rep.Rates).String()},
		{"平台费抵扣", "= 平台费用 ÷ (1+专票率) × 专票率", tt.PlatformVat(rep.Rates).String()},
		{"应交增值税", "= 销项税额 − 进项税额 − 平台费抵扣", tt.VatPayable(rep.Rates).String()},
		{"应结金额", "= 销售收入 − 平台费用 − 供货成本 − 应交增值税", tt.Net(rep.Rates).String()},
		{"仅展示（不计入）", "平台费用汇总 · 仅展示区块（运费）", tt.ListedFee.String()},
	}
	r8 := 3
	for _, ln := range lines {
		_ = f.SetCellValue(ws8, fmt.Sprintf("A%d", r8), ln[0])
		_ = f.SetCellValue(ws8, fmt.Sprintf("B%d", r8), ln[1])
		_ = f.SetCellValue(ws8, fmt.Sprintf("C%d", r8), ln[2])
		_ = f.SetCellStyle(ws8, fmt.Sprintf("C%d", r8), fmt.Sprintf("C%d", r8), money)
		r8++
	}
	_ = f.SetCellStyle(ws8, fmt.Sprintf("A%d", r8-1), fmt.Sprintf("C%d", r8-1), bold)
	_ = f.SetColWidth(ws8, "A", "A", 22)
	_ = f.SetColWidth(ws8, "B", "B", 46)
	_ = f.SetColWidth(ws8, "C", "C", 16)

	f.SetActiveSheet(0)
	return f, nil
}

func strPtr(s string) *string { return &s }

// volumeLabel 期次标识：Vol.2609 / Vol.2608-2609（与导出文件名同一口径）。
func volumeLabel(rep *service.SummaryReport) string {
	uniq := map[string]bool{}
	var ps []string
	for _, it := range rep.Items {
		if it.Period == "" || uniq[it.Period] {
			continue
		}
		uniq[it.Period] = true
		ps = append(ps, it.Period)
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

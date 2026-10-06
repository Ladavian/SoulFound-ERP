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
func buildSummaryWorkbook(rep *service.SummaryReport) (*excelize.File, error) {
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
		"垫支扣回", "供货成本", "应交增值税", "应结金额", "毛利率"}
	for i, h := range heads {
		c, _ := excelize.CoordinatesToCellName(i+1, 4)
		_ = f.SetCellValue(ws, c, h)
	}
	_ = f.SetCellStyle(ws, "A4", "J4", head)

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
		_ = f.SetCellValue(ws, fmt.Sprintf("H%d", r), t.VatPayable(rep.Rates).Float())
		_ = f.SetCellValue(ws, fmt.Sprintf("I%d", r), net.Float())
		if t.Revenue > 0 {
			_ = f.SetCellValue(ws, fmt.Sprintf("J%d", r), model.Ratio(net, t.Revenue)/100)
		}
		_ = f.SetCellStyle(ws, fmt.Sprintf("D%d", r), fmt.Sprintf("I%d", r), money)
		_ = f.SetCellStyle(ws, fmt.Sprintf("J%d", r), fmt.Sprintf("J%d", r), pct)
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
	_ = f.SetCellValue(ws, fmt.Sprintf("H%d", r), tt.VatPayable(rep.Rates).Float())
	_ = f.SetCellValue(ws, fmt.Sprintf("I%d", r), tt.Net(rep.Rates).Float())
	_ = f.SetCellStyle(ws, fmt.Sprintf("A%d", r), fmt.Sprintf("I%d", r), bold)

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
	_ = f.SetColWidth(ws, "B", "J", 15)

	// ---------------- 表2 平台费用 ----------------
	ws2 := "平台费用明细"
	_, _ = f.NewSheet(ws2)
	_ = f.SetCellValue(ws2, "A1", "各平台费用（开票部分可抵扣增值税；运费仅列示不计入结算）")
	_ = f.SetCellStyle(ws2, "A1", "A1", head)
	_ = f.SetCellValue(ws2, "A2", "平台 / 账期")
	_ = f.SetCellValue(ws2, "B2", "费用类型")
	_ = f.SetCellValue(ws2, "C2", "金额")
	_ = f.SetCellStyle(ws2, "A2", "C2", head)
	rr := 3
	for _, it := range rep.Items {
		for _, kind := range model.StmtKinds {
			amt := it.FeeByKind[kind]
			if amt == 0 {
				continue
			}
			_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", rr), it.Label+" "+model.PeriodLabel(it.Period))
			_ = f.SetCellValue(ws2, fmt.Sprintf("B%d", rr), model.StmtKindLabel(kind))
			_ = f.SetCellValue(ws2, fmt.Sprintf("C%d", rr), amt.Float())
			_ = f.SetCellStyle(ws2, fmt.Sprintf("C%d", rr), fmt.Sprintf("C%d", rr), money)
			rr++
		}
	}
	_ = f.SetCellValue(ws2, fmt.Sprintf("A%d", rr), "合计")
	_ = f.SetCellValue(ws2, fmt.Sprintf("C%d", rr), rep.FeeTotal.Float())
	_ = f.SetCellStyle(ws2, fmt.Sprintf("A%d", rr), fmt.Sprintf("C%d", rr), bold)
	_ = f.SetColWidth(ws2, "A", "A", 26)
	_ = f.SetColWidth(ws2, "B", "B", 28)
	_ = f.SetColWidth(ws2, "C", "C", 16)

	f.SetActiveSheet(0)
	return f, nil
}

func strPtr(s string) *string { return &s }

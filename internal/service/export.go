package service

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// Column 导出表格的列定义。
type Column struct {
	Title string
	Width float64
	Money bool
}

// Table 一个工作表。
type Table struct {
	Name    string
	Columns []Column
	Rows    [][]any
}

func normalizeCell(v any) any {
	switch t := v.(type) {
	case model.Money:
		return t.Float()
	case model.Qty:
		return t.Float()
	case *model.Money:
		if t == nil {
			return ""
		}
		return t.Float()
	}
	return v
}

// WriteXLSX 生成带表头样式、列宽、冻结首行与自动筛选的 Excel 文件。
//
// currencySymbol 用于金额列的数字格式（例如 ¥ 会显示成 ¥1,234.56），
// 单元格里仍然是纯数字，可以继续参与 Excel 计算。
func WriteXLSX(tables []Table, currencySymbol string) ([]byte, string, error) {
	f := excelize.NewFile()
	defer f.Close()

	headerStyle, err := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "#FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"#2F5D50"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	if err != nil {
		return nil, "", err
	}
	moneyFmt := "#,##0.00"
	if sym := strings.TrimSpace(currencySymbol); sym != "" {
		moneyFmt = `"` + sym + `"#,##0.00`
	}
	moneyStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: &moneyFmt})
	if err != nil {
		return nil, "", err
	}

	created := 0
	for _, table := range tables {
		name := sanitizeSheetName(table.Name)
		if name == "" {
			name = fmt.Sprintf("Sheet%d", created+1)
		}
		if created == 0 {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				return nil, "", err
			}
		} else {
			if _, err := f.NewSheet(name); err != nil {
				return nil, "", err
			}
		}
		created++

		if len(table.Columns) == 0 {
			continue
		}
		for i, col := range table.Columns {
			cell, err := excelize.CoordinatesToCellName(i+1, 1)
			if err != nil {
				return nil, "", err
			}
			if err := f.SetCellValue(name, cell, col.Title); err != nil {
				return nil, "", err
			}
			width := col.Width
			if width <= 0 {
				width = 14
			}
			colName, err := excelize.ColumnNumberToName(i + 1)
			if err != nil {
				return nil, "", err
			}
			if err := f.SetColWidth(name, colName, colName, width); err != nil {
				return nil, "", err
			}
		}
		lastCol, err := excelize.ColumnNumberToName(len(table.Columns))
		if err != nil {
			return nil, "", err
		}
		if err := f.SetCellStyle(name, "A1", lastCol+"1", headerStyle); err != nil {
			return nil, "", err
		}
		if err := f.SetRowHeight(name, 1, 22); err != nil {
			return nil, "", err
		}

		for r, row := range table.Rows {
			rowNum := r + 2
			for c := 0; c < len(table.Columns); c++ {
				cell, err := excelize.CoordinatesToCellName(c+1, rowNum)
				if err != nil {
					return nil, "", err
				}
				var value any
				if c < len(row) {
					value = normalizeCell(row[c])
				} else {
					value = ""
				}
				if err := f.SetCellValue(name, cell, value); err != nil {
					return nil, "", err
				}
			}
		}
		if len(table.Rows) > 0 {
			if err := f.SetCellStyle(name, "A2", lastCol+fmt.Sprint(len(table.Rows)+1), moneyStyle); err != nil {
				return nil, "", err
			}
		}
		if err := f.SetPanes(name, &excelize.Panes{
			Freeze: true, Split: false, XSplit: 0, YSplit: 1,
			TopLeftCell: "A2", ActivePane: "bottomLeft", Selection: []excelize.Selection{{SQRef: "A2", ActiveCell: "A2", Pane: "bottomLeft"}},
		}); err != nil {
			return nil, "", err
		}
	}

	buf, err := f.WriteToBuffer()
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
}

func sanitizeSheetName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '[', ']', ':', '*', '?', '/', '\\':
			return '-'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if len([]rune(name)) > 31 {
		name = string([]rune(name)[:31])
	}
	return name
}

// WriteCSV 生成带 BOM 的 CSV（Excel 直接双击不会乱码）。
func WriteCSV(headers []string, rows [][]string) ([]byte, string) {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF")
	buf.WriteString(strings.Join(headers, ","))
	buf.WriteString("\r\n")
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = escapeCSV(cell)
		}
		buf.WriteString(strings.Join(cells, ","))
		buf.WriteString("\r\n")
	}
	return buf.Bytes(), "text/csv; charset=utf-8"
}

func escapeCSV(s string) string {
	if s == "" {
		return ""
	}
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// ---------------------------------------------------------------- 具体报表

var marketSummaryColumns = []Column{
	{"单据号", 16, false}, {"市集名称", 26, false}, {"日期", 22, false}, {"状态", 10, false},
	{"天数", 6, false}, {"试饮(瓶)", 10, false}, {"销售(瓶)", 10, false}, {"试饮带动倍数", 12, false},
	{"销售额", 13, true}, {"售出成本", 13, true}, {"试饮成本", 13, true}, {"赠送损耗", 13, true},
	{"活动费用", 13, true}, {"毛利", 13, true}, {"净利润", 13, true}, {"净利率%", 10, false},
}

func marketSummaryRowToCells(r model.MarketSummaryRow) []any {
	return []any{
		r.Code, r.Name, r.DateRange, model.MarketStatusLabels[r.Status], r.Days,
		r.TastingQty, r.SoldQty, round2(r.Conversion),
		r.Revenue, r.CogsSold, r.TastingCost, r.LossCost, r.ExpenseTotal,
		r.GrossProfit, r.NetProfit, round2(r.NetMargin),
	}
}

// ExportMarketSummary 市集汇总报表（含汇总行与明细页）。
func (s *Service) ExportMarketSummary(ctx context.Context, from, to, status, keyword string) ([]byte, string, error) {
	rows, totals, err := s.MarketSummary(ctx, from, to, status, keyword)
	if err != nil {
		return nil, "", err
	}
	body := make([][]any, 0, len(rows)+1)
	for _, r := range rows {
		body = append(body, marketSummaryRowToCells(r))
	}
	body = append(body, []any{
		"合计", fmt.Sprintf("%d 场", totals.Count), "", "", "",
		totals.TastingQty, totals.SoldQty, round2(totals.Conversion),
		totals.Revenue, totals.CogsSold, totals.TastingCost, totals.LossCost,
		totals.ExpenseTotal, totals.GrossProfit, totals.NetProfit, round2(totals.NetMargin),
	})

	detailCols := []Column{
		{"单据号", 16, false}, {"市集名称", 24, false}, {"日期", 20, false}, {"产品编码", 14, false},
		{"产品名称", 26, false}, {"带去", 8, false}, {"试饮", 8, false}, {"销售", 8, false},
		{"赠送", 8, false}, {"损耗", 8, false}, {"单价", 12, true}, {"优惠", 10, true},
		{"销售额", 12, true}, {"单位成本", 12, true}, {"售出成本", 12, true},
		{"试饮成本", 12, true}, {"净利润", 12, true},
	}
	var detail [][]any
	for _, r := range rows {
		m, err := s.Store.MarketByID(ctx, r.ID)
		if err != nil {
			return nil, "", err
		}
		if m == nil {
			continue
		}
		for _, it := range m.Items {
			detail = append(detail, []any{
				m.Code, m.Name, m.DateRange, it.ProductSKU, it.ProductName,
				it.CarriedQty, it.TastingQty, it.SoldQty, it.GiftQty, it.LossQty,
				it.UnitPrice, it.DiscountAmt, it.Revenue(), it.CostBasis(),
				it.Cogs(), it.TastingCost(), it.NetProfit(),
			})
		}
	}

	expenseCols := []Column{
		{"单据号", 16, false}, {"市集名称", 26, false}, {"费用类别", 14, false},
		{"计算方式", 14, false}, {"比例", 8, false}, {"本场实际", 12, true}, {"备注", 30, false},
	}
	var expenseRows [][]any
	for _, r := range rows {
		m, err := s.Store.MarketByID(ctx, r.ID)
		if err != nil {
			return nil, "", err
		}
		if m == nil {
			continue
		}
		for _, e := range m.Expenses {
			expenseRows = append(expenseRows, []any{
				m.Code, m.Name, e.CategoryLabel(),
				e.CalcLabel(), e.RatePercentText(), m.ExpenseActual(e), e.Note,
			})
		}
	}

	return WriteXLSX([]Table{
		{Name: "市集汇总", Columns: marketSummaryColumns, Rows: body},
		{Name: "市集明细", Columns: detailCols, Rows: detail},
		{Name: "活动费用", Columns: expenseCols, Rows: expenseRows},
	}, s.currencySymbol(ctx))
}

// ExportMarketDetail 单场市集完整报表。
func (s *Service) ExportMarketDetail(ctx context.Context, marketID int64) ([]byte, string, error) {
	m, err := s.Store.MarketByID(ctx, marketID)
	if err != nil {
		return nil, "", err
	}
	if m == nil {
		return nil, "", fmt.Errorf("市集不存在")
	}
	totals := m.Totals()

	overviewCols := []Column{{"项目", 22, false}, {"数值", 20, false}}
	overview := [][]any{
		{"单据号", m.Code},
		{"市集名称", m.Name},
		{"场地", m.Venue},
		{"城市", m.City},
		{"主办方", m.Organizer},
		{"日期", m.DateRange()},
		{"天数", m.Days()},
		{"状态", m.StatusLabel()},
		{"试饮瓶数", m.TastingQty()},
		{"销售瓶数", m.SoldQty()},
		{"试饮带动倍数", round2(m.ConversionRate())},
		{"销售额", totals.Revenue},
		{"售出成本", totals.CogsSold},
		{"试饮成本", totals.TastingCost},
		{"赠送损耗成本", totals.LossCost},
		{"活动费用", totals.ExpenseCost},
		{"毛利", totals.GrossProfit()},
		{"毛利率%", round2(totals.GrossMargin())},
		{"净利润", totals.NetProfit},
		{"净利率%", round2(totals.NetMargin())},
		{"每瓶试饮成本", m.CostPerTasting()},
		{"备注", m.Notes},
	}

	itemCols := []Column{
		{"产品编码", 14, false}, {"产品名称", 26, false}, {"品类", 10, false},
		{"带去", 8, false}, {"试饮", 8, false}, {"销售", 8, false}, {"赠送", 8, false},
		{"损耗", 8, false}, {"单价", 12, true}, {"优惠", 10, true}, {"销售额", 12, true},
		{"单位成本", 12, true}, {"售出成本", 12, true}, {"试饮成本", 12, true},
		{"赠送损耗成本", 12, true}, {"净贡献", 12, true}, {"试饮带动倍数", 12, false},
	}
	itemRows := make([][]any, 0, len(m.Items))
	for _, it := range m.Items {
		itemRows = append(itemRows, []any{
			it.ProductSKU, it.ProductName, it.Category,
			it.CarriedQty, it.TastingQty, it.SoldQty, it.GiftQty, it.LossQty,
			it.UnitPrice, it.DiscountAmt, it.Revenue(), it.CostBasis(),
			it.Cogs(), it.TastingCost(), it.LossCost(), it.NetProfit(), round2(it.Conversion()),
		})
	}

	expenseCols := []Column{
		{"费用类别", 14, false}, {"计算方式", 14, false}, {"比例", 8, false},
		{"本场实际", 12, true}, {"备注", 30, false},
	}
	expenseRows := make([][]any, 0, len(m.Expenses))
	for _, e := range m.Expenses {
		expenseRows = append(expenseRows, []any{
			e.CategoryLabel(), e.CalcLabel(), e.RatePercentText(),
			m.ExpenseActual(e), e.Note,
		})
	}

	return WriteXLSX([]Table{
		{Name: "市集概览", Columns: overviewCols, Rows: overview},
		{Name: "产品销售明细", Columns: itemCols, Rows: itemRows},
		{Name: "活动费用", Columns: expenseCols, Rows: expenseRows},
	}, s.currencySymbol(ctx))
}

// ExportProductSales 产品销售报表。
func (s *Service) ExportProductSales(ctx context.Context, from, to string) ([]byte, string, error) {
	rows, err := s.ProductSalesRanking(ctx, from, to, 0)
	if err != nil {
		return nil, "", err
	}
	cols := []Column{
		{"产品编码", 14, false}, {"产品名称", 28, false}, {"试饮(瓶)", 10, false},
		{"销售(瓶)", 10, false}, {"试饮带动倍数", 12, false}, {"销售额", 14, true},
		{"售出成本", 14, true}, {"毛利", 14, true}, {"毛利率%", 10, false},
	}
	var body [][]any
	var (
		totalRevenue model.Money
		totalCogs    model.Money
		totalSold    model.Qty
		totalTasting model.Qty
	)
	for _, r := range rows {
		margin := 0.0
		if r.Revenue != 0 {
			margin = float64(r.Profit) / float64(r.Revenue) * 100
		}
		body = append(body, []any{
			r.ProductSKU, r.ProductName, r.TastingQty, r.SoldQty, round2(r.Conversion),
			r.Revenue, r.Cogs, r.Profit, round2(margin),
		})
		totalRevenue += r.Revenue
		totalCogs += r.Cogs
		totalSold += r.SoldQty
		totalTasting += r.TastingQty
	}
	totalMargin := 0.0
	if totalRevenue != 0 {
		totalMargin = float64(totalRevenue-totalCogs) / float64(totalRevenue) * 100
	}
	body = append(body, []any{
		"合计", fmt.Sprintf("%d 个产品", len(rows)), totalTasting, totalSold, "",
		totalRevenue, totalCogs, totalRevenue - totalCogs, round2(totalMargin),
	})
	return WriteXLSX([]Table{{Name: "产品销售", Columns: cols, Rows: body}}, s.currencySymbol(ctx))
}

// ExportInventory 库存报表（含流水）。
func (s *Service) ExportInventory(ctx context.Context, includeMovements bool) ([]byte, string, error) {
	products, err := s.Store.ListProducts(ctx, store.ProductFilter{IncludeInactive: true, Sort: "category"})
	if err != nil {
		return nil, "", err
	}
	cols := []Column{
		{"产品编码", 14, false}, {"产品名称", 28, false}, {"英文名", 22, false},
		{"品牌", 14, false}, {"品类", 10, false}, {"产地", 16, false},
		{"年份", 7, false}, {"容量ml", 8, false}, {"酒精度%", 9, false},
		{"规格参数", 30, false}, {"条形码", 16, false}, {"供应商", 20, false},
		{"库存数量", 11, false}, {"参考成本", 11, true}, {"平均成本", 12, true}, {"库存成本", 13, true},
		{"建议售价", 12, true}, {"毛利率%", 10, false}, {"库存预警", 10, false}, {"状态", 8, false},
	}
	var (
		body     [][]any
		totalQty model.Qty
		totalVal model.Money
	)
	for _, p := range products {
		margin := 0.0
		if p.SalePrice != 0 {
			margin = float64(p.SalePrice-p.AvgCost) / float64(p.SalePrice) * 100
		}
		status := "在用"
		if !p.IsActive {
			status = "停用"
		}
		warn := ""
		if p.IsLowStock() && p.IsActive {
			warn = "低于预警线"
		}
		var volume any = ""
		if p.VolumeML > 0 {
			volume = p.VolumeML
		}
		body = append(body, []any{
			p.SKU, p.Name, p.NameEn, p.Brand, p.Category, p.Origin,
			p.Vintage, volume, p.ABVText(),
			specsOneLine(p), p.Barcode, p.SupplierName,
			p.StockQty, p.CostPrice, p.AvgCost, p.StockValue,
			p.SalePrice, round2(margin), warn, status,
		})
		totalQty += p.StockQty
		totalVal += p.StockValue
	}
	body = append(body, []any{"合计", "", "", "", "", "", "", "", "", "", "", "",
		totalQty, "", "", totalVal, "", "", "", ""})

	tables := []Table{{Name: "库存总览", Columns: cols, Rows: body}}
	if includeMovements {
		movements, err := s.Store.ListMovements(ctx, store.MovementFilter{Limit: 20000})
		if err != nil {
			return nil, "", err
		}
		moveCols := []Column{
			{"日期", 12, false}, {"产品编码", 14, false}, {"产品名称", 26, false},
			{"类型", 12, false}, {"方向", 7, false}, {"数量", 10, false},
			{"销售单价", 11, true}, {"销售额", 12, true}, {"客户", 14, false},
			{"单位成本", 12, true}, {"金额", 13, true}, {"结存数量", 11, false},
			{"结存均价", 12, true}, {"结存金额", 13, true}, {"来源单号", 18, false},
			{"备注", 26, false}, {"操作人", 12, false},
		}
		var moveRows [][]any
		for _, m := range movements {
			moveRows = append(moveRows, []any{
				m.OccurredOn, m.ProductSKU, m.ProductName, m.ReasonLabel(), m.DirectionLabel(),
				m.AbsQty(), m.SalePrice, m.SaleAmount(), m.CustomerName,
				m.UnitCost, m.TotalCost, m.QtyAfter, m.AvgCostAfter, m.ValueAfter,
				m.RefCode, m.Note, m.CreatedByName,
			})
		}
		tables = append(tables, Table{Name: "库存流水", Columns: moveCols, Rows: moveRows})
	}
	return WriteXLSX(tables, s.currencySymbol(ctx))
}

// currencySymbol 报表里使用的货币符号，优先取系统设置。
func (s *Service) currencySymbol(ctx context.Context) string {
	if st, err := s.Store.Settings(ctx); err == nil && strings.TrimSpace(st.CurrencySymbol) != "" {
		return st.CurrencySymbol
	}
	return s.Cfg.CurrencySymbol
}

// ExportMovementsCSV 库存流水 CSV。
func (s *Service) ExportMovementsCSV(ctx context.Context, f store.MovementFilter) ([]byte, string, error) {
	f.Limit = 20000
	movements, err := s.Store.ListMovements(ctx, f)
	if err != nil {
		return nil, "", err
	}
	headers := []string{"日期", "产品编码", "产品名称", "类型", "方向", "数量",
		"单位成本", "金额", "结存数量", "结存均价", "结存金额", "来源单号", "备注", "操作人"}
	rows := make([][]string, 0, len(movements))
	for _, m := range movements {
		rows = append(rows, []string{
			m.OccurredOn, m.ProductSKU, m.ProductName, m.ReasonLabel(), m.DirectionLabel(),
			m.AbsQty().Plain(), m.UnitCost.Plain4(), m.TotalCost.Plain(), m.QtyAfter.Plain(),
			m.AvgCostAfter.Plain4(), m.ValueAfter.Plain(), m.RefCode, m.Note, m.CreatedByName,
		})
	}
	data, contentType := WriteCSV(headers, rows)
	return data, contentType, nil
}

func round2(f float64) float64 {
	return float64(int64(f*100+copySign(0.5, f))) / 100
}

func copySign(magnitude, sign float64) float64 {
	if sign < 0 {
		return -magnitude
	}
	return magnitude
}

// specsOneLine 把多行规格参数合成一行，便于放进 Excel 单元格。
func specsOneLine(p model.Product) string {
	items := p.SpecList()
	if len(items) == 0 {
		return p.Spec() // 没填规格参数时退回"容量 · 装箱"这种摘要
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if it.Label == "" {
			parts = append(parts, it.Value)
			continue
		}
		parts = append(parts, it.Label+": "+it.Value)
	}
	return strings.Join(parts, "；")
}

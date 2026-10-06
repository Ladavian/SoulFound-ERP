package web

import (
	"fmt"
	"net/http"
	"strings"

	"icewine-erp/internal/model"
)

// stmtKindFromSheet 按列名/内容判断这份账单是哪一类。
//
// 平台不同账单的文件名与列都不一样，所以先看「业务大类 / 业务小类」，
// 再看表头特征列，最后才用文件名兜底。
func stmtKindFromSheet(sh importSheet) string {
	probe := func(row []string) string {
		for _, col := range []string{"业务大类", "业务小类", "账单大类", "支出项目"} {
			if v := strings.TrimSpace(sh.cell(row, col)); v != "" {
				if kind, ok := matchStmtKind(v); ok {
					return kind
				}
			}
		}
		return ""
	}
	if len(sh.Headers) > 0 {
		if k := probe(sh.Headers); k != "" {
			return k
		}
	}
	if len(sh.Rows) > 0 {
		if k := probe(sh.Rows[0]); k != "" {
			return k
		}
	}
	// 表头特征兜底
	joined := strings.Join(sh.Headers, " ")
	switch {
	case strings.Contains(joined, "运单号") && strings.Contains(joined, "寄件"):
		return model.StmtShipping
	case strings.Contains(joined, "费率") && strings.Contains(joined, "扣费基数"):
		return model.StmtBaseService
	case strings.Contains(joined, "积分类服务费金额"):
		return model.StmtCoinCoop
	case strings.Contains(joined, "流水类型"):
		return model.StmtCoinSubsidy
	case strings.Contains(joined, "订单实际金额"):
		return model.StmtGoodsPayment
	}
	return ""
}

// matchStmtKind 账单类型名可能带后缀，做包含匹配。
func matchStmtKind(v string) (string, bool) {
	v = strings.TrimSpace(v)
	for _, kind := range model.StmtKinds {
		if v == kind || strings.Contains(v, kind) {
			return kind, true
		}
	}
	// 明细行里会出现更细的名字，做个别名映射
	switch {
	case strings.Contains(v, "淘宝新客礼金"):
		return model.StmtBrandGift, true
	case strings.Contains(v, "物流费用"), strings.Contains(v, "商家寄件"):
		return model.StmtShipping, true
	case strings.Contains(v, "基础软件服务费"):
		return model.StmtBaseService, true
	}
	return "", false
}

// stmtItemsFromSheet 把一份账单解析成明细行。
func stmtItemsFromSheet(sh importSheet, kind string) (period string, items []model.EcStatementItem) {
	direction := model.StmtDirection(kind)
	for _, row := range sh.Rows {
		rawPeriod := strings.TrimSpace(sh.cell(row, "账期"))
		p := rawPeriod
		if len(p) > 6 {
			p = p[:6] // 账期有时带日期，如 20260801
		}
		if period == "" && len(p) == 6 {
			period = p
		}
		it := model.EcStatementItem{
			Period:      p,
			Kind:        kind,
			Direction:   direction,
			OrderNo:     sh.cell(row, "订单号", "交易主订单号", "交易主单号", "主订单编号"),
			SubOrderNo:  sh.cell(row, "子订单号", "交易子订单号"),
			EcProductID: sh.cell(row, "商品ID"),
			EcSKU:       sh.cell(row, "sku", "商品属性"),
			Title:       sh.cell(row, "商品名称", "商品标题"),
			FeeRate:     sh.cell(row, "费率", "佣金率"),
			TrackingNo:  sh.cell(row, "运单号"),
			OccurredAt:  sh.cell(row, "扣费日期", "时间", "确认收货时间", "打款时间"),
			PayTime:     sh.cell(row, "打款时间"),
		}
		it.Qty = parseQtyLoose(sh.cell(row, "数量"))
		it.UnitPrice = parseMoneyLoose(sh.cell(row, "单价（元）", "单价"))
		it.Amount = parseMoneyLoose(sh.cell(row, "订单实际金额（元）", "扣费金额", "账单金额",
			"扣费金额(元)", "积分类服务费金额", "金额"))
		it.FeeBase = parseMoneyLoose(sh.cell(row, "扣费基数", "扣费交易金额"))
		it.GrossAmount = parseMoneyLoose(sh.cell(row, "抽佣金额"))
		// 品牌新享礼金：抽佣金额 = 平台代付垫支 + 服务费（账单金额）。
		// 服务费开票、可抵扣；垫支是平台替商家垫给消费者的钱，
		// 事后从货款扣回，既不是费用也不开票，必须单独拿出来。
		if kind == model.StmtBrandGift && it.GrossAmount > it.Amount {
			it.Advance = it.GrossAmount - it.Amount
		}
		it.RefundAmount = parseMoneyLoose(sh.cell(row, "退款金额（元）", "退款金额(元)"))
		if it.SubOrderNo == "" && it.OrderNo != "" {
			it.SubOrderNo = it.OrderNo
		}
		// 一行完全没金额也没订单号的，跳过（多为小计行）
		if it.Amount == 0 && it.OrderNo == "" && it.TrackingNo == "" && it.Title == "" {
			continue
		}
		items = append(items, it)
	}
	return period, items
}

// handleImportStatement 导入平台的账期账单（可一次选多个文件）。
func (s *Server) handleImportStatement(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxImportSize); err != nil {
		s.fail(w, r, "/ecommerce/reconcile", fmt.Errorf("读取上传内容失败：%w", err))
		return
	}
	platform := strings.TrimSpace(r.FormValue("platform"))
	if platform == "" {
		platform = model.EcTaobao
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		s.fail(w, r, "/ecommerce/reconcile", fmt.Errorf("请选择要导入的账单文件（可多选）"))
		return
	}

	var summaries []string
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			s.fail(w, r, "/ecommerce/reconcile", err)
			return
		}
		sh, err := readImportFile(f, fh.Filename)
		f.Close()
		if err != nil {
			s.fail(w, r, "/ecommerce/reconcile", fmt.Errorf("%s：%w", fh.Filename, err))
			return
		}
		kind := stmtKindFromSheet(sh)
		if kind == "" {
			summaries = append(summaries, fmt.Sprintf("%s：认不出是哪类账单，已跳过", fh.Filename))
			continue
		}
		period, items := stmtItemsFromSheet(sh, kind)
		if period == "" || len(items) == 0 {
			summaries = append(summaries, fmt.Sprintf("%s：没有可导入的数据行", fh.Filename))
			continue
		}
		st := &model.EcStatement{
			Platform:  platform,
			Period:    period,
			Kind:      kind,
			Direction: model.StmtDirection(kind),
			FileName:  fh.Filename,
			RowCount:  len(items),
		}
		for _, it := range items {
			st.Amount += it.Amount
		}
		if err := s.svc.ImportStatement(r.Context(), st, items, userFrom(r)); err != nil {
			s.fail(w, r, "/ecommerce/reconcile", fmt.Errorf("%s：%w", fh.Filename, err))
			return
		}
		summaries = append(summaries, fmt.Sprintf("%s → %s %s ¥%s（%d 行）",
			fh.Filename, model.PeriodLabel(period), model.StmtKindLabel(kind), st.Amount, len(items)))
	}
	s.ok(w, r, "/ecommerce/reconcile?period="+strings.TrimSpace(r.FormValue("period")),
		"账单导入完成："+strings.Join(summaries, "；"))
}

// handleReconcile 电商对账单。
func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fr := newFormReader(r)
	platform := s.ecPlatform(r)

	periods, err := s.svc.Store.StatementPeriods(ctx, platform)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	period := fr.Str("period")
	if period == "" && len(periods) > 0 {
		period = periods[0]
	}

	rec, err := s.svc.Store.Reconcile(ctx, platform, period)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	statements, err := s.svc.Store.ListStatements(ctx, platform, period)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 各账单类型的明细（页面上按类型展开）
	detail := map[string][]model.EcStatementItem{}
	for _, st := range statements {
		list, err := s.svc.Store.StatementByKindRows(ctx, platform, period, st.Kind)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		detail[st.Kind] = list
	}

	// 订单维度的到手金额：货款 − 该单各项费用
	type orderRow struct {
		OrderNo   string
		Title     string
		EcProduct string
		Product   string
		Payment   model.Money
		Fee       model.Money
		Cost      model.Money
		Net       model.Money
		Profit    model.Money
		Matched   bool
	}
	var orders []orderRow
	{
		payments := map[string]*orderRow{}
		var seq []string
		for _, kind := range model.StmtKinds {
			for _, it := range detail[kind] {
				key := it.OrderNo
				if key == "" {
					continue
				}
				row, ok := payments[key]
				if !ok {
					row = &orderRow{OrderNo: key}
					payments[key] = row
					seq = append(seq, key)
				}
				if it.Title != "" {
					row.Title = it.Title
				}
				if it.EcProductID != "" {
					row.EcProduct = it.EcProductID
				}
				if it.ProductName != "" {
					row.Product = it.ProductName
					row.Matched = true
				}
				if kind == model.StmtGoodsPayment {
					row.Payment += it.Amount
					if it.ProductID != nil && it.EcCost > 0 {
						row.Cost += model.MulQty(it.Qty, it.EcCost)
					}
				} else if it.Direction == "expense" {
					row.Fee += it.Amount
				}
			}
		}
		for _, k := range seq {
			row := payments[k]
			row.Net = row.Payment - row.Fee
			row.Profit = row.Net - row.Cost
			orders = append(orders, *row)
		}
	}

	noCache(w)
	page := s.newPage(r, "电商对账单", "reconcile")
	page["Platform"] = platform
	page["PlatformLabel"] = model.EcPlatformLabel(platform)
	page["Periods"] = periods
	page["Period"] = period
	page["PeriodLabel"] = model.PeriodLabel(period)
	page["Rec"] = rec
	page["Statements"] = statements
	page["Detail"] = detail
	page["Orders"] = orders
	page["Kinds"] = model.StmtKinds
	page["PlatformOptions"] = model.EcPlatformOptions()
	if msg := fr.Str("msg"); msg != "" {
		page["Flash"] = []Flash{{Level: "info", Text: msg}}
	}
	if err := s.rnd.Render(w, "reconcile", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// parseMoneyLoose 账单金额：可能是空、带千分位或带 ¥。
func parseMoneyLoose(raw string) model.Money {
	v := strings.TrimSpace(raw)
	v = strings.ReplaceAll(v, ",", "")
	v = strings.ReplaceAll(v, "￥", "")
	v = strings.ReplaceAll(v, "¥", "")
	v = strings.ReplaceAll(v, "%", "")
	if v == "" || v == "-" {
		return 0
	}
	m, err := model.ParseMoney(v)
	if err != nil {
		return 0
	}
	return m
}

// parseQtyLoose 账单数量。
func parseQtyLoose(raw string) model.Qty {
	v := strings.TrimSpace(raw)
	if v == "" || v == "-" {
		return 0
	}
	q, err := model.ParseQty(v)
	if err != nil {
		return 0
	}
	return q
}

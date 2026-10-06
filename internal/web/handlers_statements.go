package web

import (
	"fmt"
	"net/http"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
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
		// 平台规格：账单写「6177628402264|商品规格#3B1瓶装礼盒」，
		// 订单导出写「商品规格:1瓶装礼盒」。拆成 SKU ID 与标签，
		// 分别用于跟账单、跟订单匹配。
		rawSKU := sh.cell(row, "sku", "商品属性")
		it.SKUId, it.SKULabel = model.ParseEcSKU(rawSKU)
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
		func() {
			f, err := fh.Open()
			if err != nil {
				summaries = append(summaries, fmt.Sprintf("%s：打不开（%v）", fh.Filename, err))
				return
			}
			sheets, err := readImportSheets(f, fh.Filename)
			f.Close()
			if err != nil {
				summaries = append(summaries, fmt.Sprintf("%s：%v", fh.Filename, err))
				return
			}
			for _, sh := range sheets {
				summaries = append(summaries, s.importOneSheet(r, platform, fh.Filename, sh)...)
			}
		}()
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
	sel := selectedPeriods(r, periods, 1)

	// 多账期：逐个算再合并（账期不多，循环足够）
	var rec model.EcReconcile
	var statements []model.EcStatement
	for _, p := range sel {
		one, err := s.svc.Store.Reconcile(ctx, platform, p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rec.IncomeTotal += one.IncomeTotal
		rec.ExpenseTotal += one.ExpenseTotal
		rec.IncomeItems += one.IncomeItems
		rec.ExpenseItems += one.ExpenseItems
		if rec.IncomeByKind == nil {
			rec.IncomeByKind = map[string]model.Money{}
			rec.ExpenseByKind = map[string]model.Money{}
			rec.IncomeKindRows = map[string]int{}
			rec.ExpenseKindRows = map[string]int{}
		}
		for k, v := range one.IncomeByKind {
			rec.IncomeByKind[k] += v
		}
		for k, v := range one.ExpenseByKind {
			rec.ExpenseByKind[k] += v
		}
		for k, v := range one.IncomeKindRows {
			rec.IncomeKindRows[k] += v
		}
		for k, v := range one.ExpenseKindRows {
			rec.ExpenseKindRows[k] += v
		}
		list, err := s.svc.Store.ListStatements(ctx, platform, p)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		statements = append(statements, list...)
	}
	rec.Platform, rec.Period = platform, ""
	period := ""
	if len(sel) > 0 {
		period = sel[0]
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 各账单类型的明细（页面上按类型展开）
	// 两份账单的对账校验结果（只列有问题的）
	var checks []model.EcStatementCheck
	checkStats := map[string]int{}
	hasChecks := false
	for _, p := range sel {
		cs, _ := s.svc.Store.StatementChecks(ctx, platform, p)
		checks = append(checks, cs...)
		st, _ := s.svc.Store.StatementCheckStats(ctx, platform, p)
		for k, v := range st {
			checkStats[k] += v
		}
		if ok, _ := s.svc.Store.HasStatementChecks(ctx, platform, p); ok {
			hasChecks = true
		}
	}
	// 结算口径：多账期合并
	settings, _ := s.svc.Store.Settings(ctx)
	settle, err := s.svc.BuildSettlementFor(ctx, platform, sel, settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 页面上展示的结算口径直接用结算单算出来的数，
	// 不在这里再算一遍，避免两处口径不一致。
	rates := settle.Rates
	rec.Advance = settle.Total.Advance
	rec.RealRevenue = settle.Total.Revenue
	rec.Cost = settle.Total.Cost
	rec.CostTotal = settle.Total.Cost
	rec.Vat = settle.Total.VatPayable(rates)
	rec.Profit = settle.Total.Net(rates)
	rec.Fee = settle.Total.PlatformFee
	rec.ExpenseTotal = settle.Total.PlatformFee

	detail := map[string][]model.EcStatementItem{}
	for _, st := range statements {
		list, err := s.svc.Store.StatementByKindRows(ctx, platform, st.Period, st.Kind)
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

	// 各平台已导入多少份账单：用来在"当前平台没数据"时提示去哪看
	allKeys, _ := s.svc.Store.StatementKeys(ctx)
	platformCounts := map[string]int{}
	for _, k := range allKeys {
		platformCounts[k.Platform]++
	}
	type platHint struct {
		Label string
		Value string
		Count int
	}
	var hints []platHint
	for _, opt := range model.EcPlatformOptions() {
		if n := platformCounts[opt.Value]; n > 0 && opt.Value != platform {
			hints = append(hints, platHint{Label: opt.Label, Value: opt.Value, Count: n})
		}
	}

	noCache(w)
	page := s.newPage(r, "电商对账单", "reconcile")
	page["PlatformHints"] = hints
	page["PlatformStatements"] = platformCounts[platform]
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
	page["Checks"] = checks
	page["CheckStats"] = checkStats
	page["HasChecks"] = hasChecks
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

// handleSettlement 单个平台的结算单（含增值税与应结金额）。
func (s *Server) handleSettlement(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fr := newFormReader(r)
	platform := s.ecPlatform(r)

	periods, err := s.svc.Store.StatementPeriods(ctx, platform)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 账期支持多选：小平台两三个月结一次，得能把几个月合起来看
	sel := selectedPeriods(r, periods, 1)
	settings, _ := s.svc.Store.Settings(ctx)
	rep, err := s.svc.BuildSettlementFor(ctx, platform, sel, settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rep.Products = service.MergeProducts(rep.Products)
	period := ""
	if len(sel) > 0 {
		period = sel[0]
	}

	noCache(w)
	page := s.newPage(r, "结算单", "settlement")
	page["PeriodChips"] = periodChips(r, periods, sel, nil)
	page["Platform"] = platform
	page["PlatformLabel"] = model.EcPlatformLabel(platform)
	page["PlatformOptions"] = model.EcPlatformOptions()
	page["Periods"] = periods
	page["SelectedPeriods"] = sel
	page["Period"] = period
	page["PeriodLabel"] = periodLabelMulti(sel)
	page["Rep"] = rep
	page["Rates"] = rep.Rates
	page["Kinds"] = model.StmtKinds
	if msg := fr.Str("msg"); msg != "" {
		page["Flash"] = []Flash{{Level: "info", Text: msg}}
	}
	if err := s.rnd.Render(w, "settlement", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleSettlementExport 导出结算单 Excel（给上游结算用）。
func (s *Server) handleSettlementExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	platform := s.ecPlatform(r)
	period := strings.TrimSpace(r.URL.Query().Get("period"))
	settings, _ := s.svc.Store.Settings(ctx)
	rep, err := s.svc.BuildSettlement(ctx, platform, period, settings)
	if err != nil {
		s.fail(w, r, "/ecommerce/settlement", err)
		return
	}
	f, err := buildSettlementWorkbook(rep, model.EcPlatformLabel(platform))
	if err != nil {
		s.fail(w, r, "/ecommerce/settlement", err)
		return
	}
	name := fmt.Sprintf("结算单-%s-%s.xlsx", platform, model.PeriodLabel(period))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEncode(name))
	w.Header().Set("Cache-Control", "no-store")
	if err := f.Write(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// urlEncode 文件名编码（Content-Disposition 用）。
func urlEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// handleSummary 多平台合并对账单。
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	keys, err := s.svc.Store.StatementKeys(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 勾选单位是「平台 + 账期」这一对，不是"平台 × 账期"的笛卡尔积。
	//
	// 用户的实际结算节奏：淘宝按月做，小平台可能两个月做一次。
	// 9 月要结"淘宝 8 月 + 京东 7-8 月"，如果按平台 × 账期来筛，
	// 选淘宝+京东、7月+8月 会把已经结过的淘宝 7 月也算进来。
	pp, err := s.svc.Store.StatementPlatformPeriods(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pickedSet := map[string]bool{}
	rawSel := r.URL.Query()["sel"]
	if len(rawSel) == 0 {
		// 默认只勾每个平台最新的一期：最贴近"这次要结的账"，
		// 不会把以前已经结过的月份又算一遍。
		seen := map[string]bool{}
		for _, it := range pp {
			if !seen[it.Platform] {
				seen[it.Platform] = true
				pickedSet[store.StatementKeyString(it.Platform, it.Period)] = true
			}
		}
	} else {
		for _, v := range rawSel {
			if _, _, ok := store.ParseStatementKey(v); ok {
				pickedSet[v] = true
			}
		}
	}
	type ppRow struct {
		store.PlatformPeriod
		Key     string
		Label   string
		PeriodL string
		Checked bool
	}
	var ppRows []ppRow
	var selKeys []string
	for _, it := range pp {
		k := store.StatementKeyString(it.Platform, it.Period)
		row := ppRow{PlatformPeriod: it, Key: k,
			Label: model.EcPlatformLabel(it.Platform), PeriodL: model.PeriodLabel(it.Period),
			Checked: pickedSet[k]}
		if row.Checked {
			selKeys = append(selKeys, k)
		}
		ppRows = append(ppRows, row)
	}

	var picked []store.StatementKey
	for _, k := range keys {
		if pickedSet[store.StatementKeyString(k.Platform, k.Period)] {
			picked = append(picked, k)
		}
	}

	settings, _ := s.svc.Store.Settings(ctx)
	rep, err := s.svc.BuildSummary(ctx, picked, settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	noCache(w)
	page := s.newPage(r, "总对账单", "summary")
	page["PPRows"] = ppRows
	page["Picked"] = picked
	page["AllCount"] = len(keys)
	page["SelKeys"] = selKeys
	page["Rep"] = rep
	page["Rates"] = rep.Rates
	page["Keys"] = keys
	page["Kinds"] = model.StmtKinds
	if msg := newFormReader(r).Str("msg"); msg != "" {
		page["Flash"] = []Flash{{Level: "info", Text: msg}}
	}
	if err := s.rnd.Render(w, "summary", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleSummaryExport 导出合并对账单 Excel。
func (s *Server) handleSummaryExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	keys, err := s.svc.Store.StatementKeys(ctx)
	if err != nil {
		s.fail(w, r, "/ecommerce/summary", err)
		return
	}
	// 导出跟页面选择保持一致（同样是"平台 + 账期"逐条勾选）
	keys = filterStatementKeys(r, keys)
	settings, _ := s.svc.Store.Settings(ctx)
	rep, err := s.svc.BuildSummary(ctx, keys, settings)
	if err != nil {
		s.fail(w, r, "/ecommerce/summary", err)
		return
	}
	f, err := buildSummaryWorkbook(rep)
	if err != nil {
		s.fail(w, r, "/ecommerce/summary", err)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+urlEncode("总对账单.xlsx"))
	w.Header().Set("Cache-Control", "no-store")
	if err := f.Write(w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// importOneSheet 处理上传文件里的一张工作表，返回给用户看的说明。
//
// 一张表可能是：京东月度账单 / 京东对账中心费用明细 / 京东订单明细 /
// 淘宝各类账期账单。按表头特征识别，再按「账单类型」分别落库
// （同一账期同一类型整体覆盖，不会重复计账）。
func (s *Server) importOneSheet(r *http.Request, platform, filename string, sh importSheet) []string {
	label := filename
	if sh.Name != "" {
		label += "[" + sh.Name + "]"
	}

	// 京东：月度账单与对账中心费用明细都是"每行一个费用项"
	if isJDMonthly(sh) || isJDFees(sh) {
		jdPeriod, jdSource, items := jdStatementRows(sh)
		if jdPeriod == "" || len(items) == 0 {
			return []string{label + "：没有可导入的数据行"}
		}
		return s.saveStatementGroupsSource(r, model.EcJD, jdPeriod, jdSource, label, items)
	}
	// 京东订单明细
	if isJDOrders(sh) {
		rows := make([]service.EcOrderRow, 0, len(sh.Rows))
		for _, row := range sh.Rows {
			rows = append(rows, jdOrderRow(sh, row))
		}
		summary, err := s.svc.ImportEcOrders(r.Context(), model.EcJD, rows, userFrom(r))
		if err != nil {
			return []string{fmt.Sprintf("%s：京东订单导入失败（%v）", label, err)}
		}
		return []string{fmt.Sprintf("%s → 京东订单 %d 单 / %d 行",
			label, summary.Orders+summary.OrdersUpd, summary.Items)}
	}

	// 淘宝（及其它平台）的账期账单
	kind := stmtKindFromSheet(sh)
	if kind == "" {
		return []string{label + "：认不出是哪类账单，已跳过"}
	}
	period, items := stmtItemsFromSheet(sh, kind)
	if period == "" || len(items) == 0 {
		return []string{label + "：没有可导入的数据行"}
	}
	return s.saveStatementGroups(r, platform, period, label, items)
}

// saveStatementGroups 按账单类型分组落库（来源默认按平台月度账单）。
func (s *Server) saveStatementGroups(r *http.Request, platform, period, label string, items []model.EcStatementItem) []string {
	return s.saveStatementGroupsSource(r, platform, period, model.StmtSourceBill, label, items)
}

// saveStatementGroupsSource 按账单类型分组落库，并记录来源。
//
// 京东一个账期有两份来源：月度账单（订单与数量以它为准）与
// 对账中心（项目更全）。存储层会保证"月度账单已有的费用项不被
// 对账中心覆盖"，对账中心独有的费用项照常补进来。
func (s *Server) saveStatementGroupsSource(r *http.Request, platform, period, source, label string, items []model.EcStatementItem) []string {
	grouped := map[string][]model.EcStatementItem{}
	var order []string
	for _, it := range items {
		if _, ok := grouped[it.Kind]; !ok {
			order = append(order, it.Kind)
		}
		grouped[it.Kind] = append(grouped[it.Kind], it)
	}
	var out []string
	for _, k := range order {
		list := grouped[k]
		st := &model.EcStatement{
			Platform: platform, Period: period, Kind: k, Source: source,
			Direction: list[0].Direction, FileName: label, RowCount: len(list),
		}
		for _, it := range list {
			st.Amount += it.Amount
		}
		if err := s.svc.ImportStatement(r.Context(), st, list, userFrom(r)); err != nil {
			out = append(out, fmt.Sprintf("%s：%s 导入失败（%v）", label, model.StmtKindLabel(k), err))
			continue
		}
		out = append(out, fmt.Sprintf("%s → %s %s %s（%d 行）",
			label, model.PeriodLabel(period), model.StmtKindLabel(k), st.Amount, len(list)))
		// 对账中心与月度账单都有的费用项，按订单号比一遍，防止有错
		if source == model.StmtSourceReconcile {
			if checks, err := s.svc.CompareStatement(r.Context(), platform, period, k, source, list); err == nil && len(checks) > 0 {
				var same, diff, onlyP, onlyO int
				for _, c := range checks {
					switch c.Status {
					case model.CheckSame:
						same++
					case model.CheckDiff:
						diff++
					case model.CheckOnlyPrimary:
						onlyP++
					case model.CheckOnlyOther:
						onlyO++
					}
				}
				out = append(out, fmt.Sprintf("　对账校验 %s：一致 %d · 不一致 %d · 只在月度账单 %d · 只在对账中心 %d",
					model.StmtKindLabel(k), same, diff, onlyP, onlyO))
			}
		}
	}
	return out
}

// filterStatementKeys 按 URL 上的「平台|账期」勾选过滤账单。
//
// 注意不是按平台、按账期分别过滤（那会变成笛卡尔积），
// 而是逐条勾选：淘宝8月 + 京东7月 + 京东8月 这样。
func filterStatementKeys(r *http.Request, keys []store.StatementKey) []store.StatementKey {
	raw := r.URL.Query()["sel"]
	if len(raw) == 0 {
		return keys
	}
	picked := map[string]bool{}
	for _, v := range raw {
		picked[v] = true
	}
	var out []store.StatementKey
	for _, k := range keys {
		if picked[store.StatementKeyString(k.Platform, k.Period)] {
			out = append(out, k)
		}
	}
	return out
}

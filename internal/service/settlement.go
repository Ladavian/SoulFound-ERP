package service

import (
	"context"
	"database/sql"
	"sort"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// SettlementReport 一个账期的完整结算报表。
type SettlementReport struct {
	Platform  string
	Period    string
	Rates     model.VatRates
	Total     model.Settlement
	Orders    []SettlementOrderRow
	Products  []SettlementProductRow
	PeriodFee []SettlementPeriodFee // 没有订单号的整期费用
	FeeByKind map[string]model.Money
	FeeRows   int
}

// SettlementOrderRow 一单的结算。
type SettlementOrderRow struct {
	OrderNo  string
	Title    string
	Product  string
	EcID     string
	S        model.Settlement
	UnitCost model.Money
}

// SettlementProductRow 按商品汇总的一行。
type SettlementProductRow struct {
	Name string
	S    model.Settlement
}

// SettlementPeriodFee 整期一笔的费用（如消费者体验提升计划）。
type SettlementPeriodFee struct {
	Kind   string
	Amount model.Money
}

// VatRatesFromSettings 从设置里取税率。
func VatRatesFromSettings(s *model.Settings) model.VatRates {
	r := model.DefaultVatRates()
	if s == nil {
		return r
	}
	if v := parseRate(s.VatOutputRate); v > 0 {
		r.Output = v
	}
	if v := parseRate(s.VatInputRate); v > 0 {
		r.Input = v
	}
	if v := parseRate(s.VatPlatformRate); v > 0 {
		r.Platform = v
	}
	return r
}

func parseRate(raw string) float64 {
	f := 0.0
	for _, ch := range raw {
		if ch >= '0' && ch <= '9' {
			f = f*10 + float64(ch-'0')
		} else if ch == '.' {
			continue
		} else {
			return 0
		}
	}
	return f
}

// BuildSettlement 组装一个账期的结算报表。
//
// 收入口径：货款 + 平台补贴 − 平台代付垫支。
// 垫支（淘宝品牌新享礼金里平台替商家垫给消费者的钱）从货款扣回，
// 既不是费用也不开票，所以单独扣收入，不进可抵扣的平台费用。
//
// 增值税口径：应交 = 销项 − 进项 − 平台费专票抵扣，
// 也就是"只承担成本以上的税"，运费不计入。
func (s *Service) BuildSettlement(ctx context.Context, platform, period string, settings *model.Settings) (*SettlementReport, error) {
	rates := VatRatesFromSettings(settings)
	orders, periodFees, err := s.Store.StatementSettlement(ctx, platform, period)
	if err != nil {
		return nil, err
	}
	products, err := s.Store.StatementSettlementProducts(ctx, platform, period)
	if err != nil {
		return nil, err
	}
	statements, err := s.Store.ListStatements(ctx, platform, period)
	if err != nil {
		return nil, err
	}

	rep := &SettlementReport{
		Platform:  platform,
		Period:    period,
		Rates:     rates,
		FeeByKind: map[string]model.Money{},
	}
	for _, st := range statements {
		if st.Direction == "expense" {
			rep.FeeByKind[st.Kind] += st.Amount
			rep.FeeRows += st.RowCount
		}
	}

	for _, o := range orders {
		row := model.Settlement{
			Goods:   o.Goods,
			Subsidy: o.Subsidy,
			Advance: o.Advance,
			Qty:     o.Qty,
			Cost:    model.MulQty(o.Qty, o.UnitCost),
		}
		row.Revenue = row.Goods + row.Subsidy - row.Advance
		row.PlatformFee = o.FeePaid
		rep.Orders = append(rep.Orders, SettlementOrderRow{
			OrderNo: o.OrderNo, Title: o.Title, Product: o.ProductName,
			EcID: o.EcProductID, S: row, UnitCost: o.UnitCost,
		})
		rep.Total.Add(row)
	}

	for _, p := range products {
		row := model.Settlement{Qty: p.Qty, Revenue: p.Revenue, Goods: p.Revenue, Cost: p.Cost}
		rep.Products = append(rep.Products, SettlementProductRow{Name: p.Name, S: row})
	}

	// 整期一笔的费用（无订单号）计入总费用，但没法分摊到单
	for kind, amt := range periodFees {
		if model.StmtDirection(kind) == "expense" {
			rep.PeriodFee = append(rep.PeriodFee, SettlementPeriodFee{Kind: kind, Amount: amt})
		}
	}

	// 总费用按所有支出账单口径（含整期一笔）；
	// 其中"仅列示"的（运费）单独扣出来，不参与增值税抵扣。
	rep.Total.PlatformFee = 0
	rep.Total.CreditableFee = 0
	for kind, amt := range rep.FeeByKind {
		rep.Total.PlatformFee += amt
		if model.StmtCreditable(kind) {
			rep.Total.CreditableFee += amt
		}
	}
	return rep, nil
}

// PlatformSettlement 单个平台在一个账期的结算结果。
type PlatformSettlement struct {
	Platform  string
	Period    string
	Label     string
	Total     model.Settlement
	FeeByKind map[string]model.Money
	Rows      int
}

// FeeMatrix 费用类型 × 平台的矩阵。
//
// 用户要"汇总清楚、分离说清楚哪个平台的什么费用"，
// 矩阵是最直观的：一行一个费用类型，一列一个平台，最后合计。
type FeeMatrix struct {
	Platforms []string               // 列（平台）
	Kinds     []string               // 行（费用类型）
	Cell      map[string]model.Money // kind|platform → 金额
	KindTotal map[string]model.Money // 每行合计
	PlatTotal map[string]model.Money // 每列合计
	Total     model.Money            // 总计
}

// SummaryReport 多平台合并对账单。
//
// 各平台结算节奏不同（淘宝按月，其它平台可能两三个月一次），
// 所以合并表按「平台 + 账期」逐行列示，再给一行总计。
type SummaryReport struct {
	Rates    model.VatRates
	Items    []PlatformSettlement
	Total    model.Settlement
	FeeTotal model.Money
	Fees     FeeMatrix
}

// BuildSummary 合并多个平台/账期的结算。
func (s *Service) BuildSummary(ctx context.Context, keys []store.StatementKey, settings *model.Settings) (*SummaryReport, error) {
	rates := VatRatesFromSettings(settings)
	rep := &SummaryReport{Rates: rates}
	for _, k := range keys {
		st, err := s.BuildSettlement(ctx, k.Platform, k.Period, settings)
		if err != nil {
			return nil, err
		}
		rep.Items = append(rep.Items, PlatformSettlement{
			Platform: k.Platform, Period: k.Period,
			Label:     model.EcPlatformLabel(k.Platform),
			Total:     st.Total,
			FeeByKind: st.FeeByKind,
			Rows:      st.FeeRows,
		})
		rep.Total.Add(st.Total)
		rep.FeeTotal += st.Total.PlatformFee
	}
	rep.Fees = buildFeeMatrix(rep.Items)
	return rep, nil
}

// buildFeeMatrix 汇总成「费用类型 × 平台」的矩阵。
func buildFeeMatrix(items []PlatformSettlement) FeeMatrix {
	m := FeeMatrix{
		Cell:      map[string]model.Money{},
		KindTotal: map[string]model.Money{},
		PlatTotal: map[string]model.Money{},
	}
	platSeen, kindSeen := map[string]bool{}, map[string]bool{}
	for _, it := range items {
		for _, kind := range model.StmtKinds {
			amt := it.FeeByKind[kind]
			if amt == 0 {
				continue
			}
			if !platSeen[it.Platform] {
				platSeen[it.Platform] = true
				m.Platforms = append(m.Platforms, it.Platform)
			}
			if !kindSeen[kind] {
				kindSeen[kind] = true
				m.Kinds = append(m.Kinds, kind)
			}
			m.Cell[kind+"|"+it.Platform] += amt
			m.KindTotal[kind] += amt
			m.PlatTotal[it.Platform] += amt
			m.Total += amt
		}
	}
	// 京东等平台的费用项是动态的（京东·佣金、京东·商品保险服务费…），
	// 不在固定清单里，要补进行里。
	// 必须排序：Go 的 map 遍历顺序是随机的，不排会导致每次刷新
	// 费用行的顺序都不一样，对账时容易看漏。
	var extra []string
	for _, it := range items {
		for kind := range it.FeeByKind {
			if !kindSeen[kind] {
				kindSeen[kind] = true
				extra = append(extra, kind)
			}
		}
	}
	sort.Strings(extra)
	for _, kind := range extra {
		m.Kinds = append(m.Kinds, kind)
		for _, it := range items {
			amt := it.FeeByKind[kind]
			if amt == 0 {
				continue
			}
			if !platSeen[it.Platform] {
				platSeen[it.Platform] = true
				m.Platforms = append(m.Platforms, it.Platform)
			}
			m.Cell[kind+"|"+it.Platform] += amt
			m.KindTotal[kind] += amt
			m.PlatTotal[it.Platform] += amt
			m.Total += amt
		}
	}
	return m
}

// ---------------------------------------------------------------- 对账校验

// StmtCheckResult 一次比对的汇总。
type StmtCheckResult struct {
	Kind        string
	Same        int
	Diff        int
	OnlyPrimary int
	OnlyOther   int
	DiffAmount  model.Money
}

// CompareStatement 把「为准的来源」与「另一来源」按订单号逐笔比对并落库。
//
// 京东同一账期的月度账单与对账中心都含佣金、交易服务费，
// 对一遍才能确认账目没错。规则（用户明确）：
//
//	订单号以月度账单为准 —— 先把为准那一份按订单号聚合，
//	再拿另一来源的明细逐笔比：
//	  两边都有且金额相同 → 一致
//	  两边都有但金额不同 → 金额不一致（记差额）
//	  只在月度账单里       → 对账中心缺这一单
//	  只在对账中心里       → 月度账单缺这一单（要留意）
//
// 对账中心的明细按合并规则不会落进账单明细表，
// 所以要在导入的那一刻比——那时两份数据都在手上。
func (s *Service) CompareStatement(ctx context.Context, platform, period, kind, otherSource string, otherItems []model.EcStatementItem) ([]model.EcStatementCheck, error) {
	if kind == "" || otherSource == "" {
		return nil, nil
	}
	primarySource := model.StmtSourceBill
	if otherSource == model.StmtSourceBill {
		primarySource = model.StmtSourceReconcile
	}
	// 为准那一份是否真的存在；不存在就没什么可比
	sources, err := s.Store.StatementSources(ctx, platform, period, kind)
	if err != nil {
		return nil, err
	}
	found := false
	for _, src := range sources {
		if src == primarySource {
			found = true
		}
	}
	if !found {
		return nil, nil
	}

	primaryItems, err := s.Store.StatementKindItems(ctx, platform, period, kind)
	if err != nil {
		return nil, err
	}
	agg := func(items []model.EcStatementItem) (map[string]model.Money, []string) {
		m := map[string]model.Money{}
		var order []string
		for _, it := range items {
			if it.OrderNo == "" {
				continue
			}
			if _, ok := m[it.OrderNo]; !ok {
				order = append(order, it.OrderNo)
			}
			m[it.OrderNo] += it.Amount
		}
		return m, order
	}
	pm, pOrder := agg(primaryItems)
	om, oOrder := agg(otherItems)

	var checks []model.EcStatementCheck
	add := func(orderNo string, pa, oa model.Money, status string) {
		checks = append(checks, model.EcStatementCheck{
			Platform: platform, Period: period, Kind: kind, OrderNo: orderNo,
			PrimarySource: primarySource, PrimaryAmount: pa,
			OtherSource: otherSource, OtherAmount: oa, Status: status,
		})
	}
	for _, o := range pOrder {
		oa, ok := om[o]
		switch {
		case !ok:
			add(o, pm[o], 0, model.CheckOnlyPrimary)
		case oa == pm[o]:
			add(o, pm[o], oa, model.CheckSame)
		default:
			add(o, pm[o], oa, model.CheckDiff)
		}
	}
	for _, o := range oOrder {
		if _, ok := pm[o]; !ok {
			add(o, 0, om[o], model.CheckOnlyOther)
		}
	}
	if err := s.SaveStatementChecks(ctx, checks); err != nil {
		return nil, err
	}
	return checks, nil
}

// SaveStatementChecks 保存比对结果。
func (s *Service) SaveStatementChecks(ctx context.Context, checks []model.EcStatementCheck) error {
	if len(checks) == 0 {
		return nil
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		return s.Store.ReplaceStatementChecks(ctx, tx,
			checks[0].Platform, checks[0].Period, checks[0].Kind, checks)
	})
}

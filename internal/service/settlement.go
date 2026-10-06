package service

import (
	"context"

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

	// 总费用按所有支出账单口径（含整期一笔）
	rep.Total.PlatformFee = 0
	for _, amt := range rep.FeeByKind {
		rep.Total.PlatformFee += amt
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

// SummaryReport 多平台合并对账单。
//
// 各平台结算节奏不同（淘宝按月，其它平台可能两三个月一次），
// 所以合并表按「平台 + 账期」逐行列示，再给一行总计。
type SummaryReport struct {
	Rates    model.VatRates
	Items    []PlatformSettlement
	Total    model.Settlement
	FeeTotal model.Money
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
	return rep, nil
}

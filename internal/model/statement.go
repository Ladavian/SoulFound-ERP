package model

import "strings"

// 账单类型。与平台各文件对应，收入/支出分开记。
const (
	StmtGoodsPayment   = "交易货款"
	StmtCoinSubsidy    = "淘金币合作费用-流水"
	StmtCoinCoop       = "淘金币合作费用"
	StmtBaseService    = "基础软件服务费"
	StmtBrandGift      = "品牌新享淘宝礼金软件服务费"
	StmtShipping       = "商家寄件服务费"
	StmtCoinService    = "淘金币软件服务费"
	StmtCouponClawback = "消费券代付资金扣回"
	StmtExperience     = "消费者体验提升计划服务费"
)

// StmtKinds 账单类型清单（顺序即对账单上的显示顺序）。
var StmtKinds = []string{
	StmtGoodsPayment,
	StmtCoinSubsidy,
	StmtCoinCoop,
	StmtBaseService,
	StmtBrandGift,
	StmtShipping,
	StmtCoinService,
	StmtCouponClawback,
	StmtExperience,
}

// StmtIncomeKinds 收入类账单。
//
// 淘金币两项按用户确认算收入：平台把它们放在收入账单里导出。
var StmtIncomeKinds = map[string]bool{
	StmtGoodsPayment: true,
	StmtCoinSubsidy:  true,
	StmtCoinCoop:     true,
}

// StmtDirection 账单方向。
func StmtDirection(kind string) string {
	if StmtIncomeKinds[kind] {
		return "income"
	}
	return "expense"
}

// StmtKindLabel 账单类型的中文名（明细表里显示用）。
func StmtKindLabel(kind string) string {
	if v, ok := stmtKindLabels[kind]; ok {
		return v
	}
	return kind
}

var stmtKindLabels = map[string]string{
	StmtGoodsPayment:   "交易货款",
	StmtCoinSubsidy:    "淘金币补贴",
	StmtCoinCoop:       "淘金币合作费用",
	StmtBaseService:    "基础软件服务费",
	StmtBrandGift:      "品牌新享礼金服务费",
	StmtShipping:       "商家寄件服务费",
	StmtCoinService:    "淘金币软件服务费",
	StmtCouponClawback: "消费券代付扣回",
	StmtExperience:     "消费者体验提升计划",
}

// StmtKindHint 这类账单怎么匹配订单（界面提示用）。
func StmtKindHint(kind string) string {
	switch kind {
	case StmtGoodsPayment:
		return "按订单号匹配"
	case StmtShipping:
		return "按运单号匹配订单"
	case StmtExperience:
		return "整期一笔，无订单号"
	default:
		return "按订单号匹配"
	}
}

// ---------------------------------------------------------------- 账期账单

// EcStatement 一份账单文件。
type EcStatement struct {
	ID         int64
	Platform   string
	Period     string // YYYYMM
	Kind       string
	Direction  string
	FileName   string
	RowCount   int
	Amount     Money
	ImportedAt string
}

// EcStatementItem 账单里的一行。
type EcStatementItem struct {
	ID           int64
	StatementID  int64
	Platform     string
	Period       string
	Kind         string
	Direction    string
	OrderNo      string
	SubOrderNo   string
	EcProductID  string
	EcSKU        string
	Title        string
	Qty          Qty
	UnitPrice    Money
	Amount       Money
	FeeBase      Money
	FeeRate      string
	RefundAmount Money
	SKUId        string // 平台 SKU ID（账单里有，如 6177628402264）
	SKULabel     string // 规格标签（两边都有，如 1瓶装礼盒）
	TrackingNo   string
	Advance      Money // 平台代付垫支（新享垫给消费者的钱），要从货款扣回
	GrossAmount  Money // 原始金额（如抽佣金额，含垫付），仅备查
	OccurredAt   string
	PayTime      string
	Raw          string
	ImportedAt   string

	// 关联查询
	ProductID   *int64
	ProductName string
	ProductSKU  string
	EcCost      Money
}

// Signed 金额带方向：收入为正，支出为负。
func (i EcStatementItem) Signed() Money {
	if i.Direction == "expense" {
		return -i.Amount
	}
	return i.Amount
}

// PeriodLabel 账期显示成 2026-08。
func PeriodLabel(period string) string {
	p := strings.TrimSpace(period)
	if len(p) == 6 {
		return p[:4] + "-" + p[4:]
	}
	return p
}

// EcReconcile 一个账期的对账汇总。
type EcReconcile struct {
	Platform string
	Period   string

	// 收入
	IncomeItems    int
	IncomeTotal    Money
	IncomeByKind   map[string]Money
	IncomeKindRows map[string]int

	// 支出
	ExpenseItems    int
	ExpenseTotal    Money
	ExpenseByKind   map[string]Money
	ExpenseKindRows map[string]int

	// 产品成本（电商成本 × 数量，只算能匹配到产品的行）
	CostTotal     Money
	CostKnownRows int
	CostMissing   int // 没匹配到产品或没填电商成本的行数

	// 匹配情况
	MatchedOrders int
	TotalOrders   int
	UnmatchedShip int // 运费单里找不到对应订单的运单数
}

// Net 净得 = 收入 − 支出（平台实际结算给商家的钱）。
func (r EcReconcile) Net() Money { return r.IncomeTotal - r.ExpenseTotal }

// NetProfit 净利 = 净得 − 产品成本。
func (r EcReconcile) NetProfit() Money { return r.Net() - r.CostTotal }

// Margin 净利率（%）。
func (r EcReconcile) Margin() float64 { return Ratio(r.NetProfit(), r.IncomeTotal) }

// ---------------------------------------------------------------- 增值税与结算

// VatRates 增值税率配置（百分数，如 13 / 6）。
type VatRates struct {
	Output   float64 // 销项税率：销售额含的税率
	Input    float64 // 进项税率：成本含的税率
	Platform float64 // 平台服务费专票税率
}

// DefaultVatRates 默认税率（加拿大冰酒按 13% 走）。
func DefaultVatRates() VatRates { return VatRates{Output: 13, Input: 13, Platform: 6} }

// Settlement 一行（或一个平台/整月）的结算明细。
//
// 收入按"实际能拿到手的"算：
//
//	收入 = 货款 + 平台补贴 − 平台代付垫支
//
// 平台代付垫支（淘宝的品牌新享礼金）是平台替商家先垫给消费者的钱，
// 事后从商家货款里扣回；它不是费用、也不开票，所以不能混进可抵扣的
// 平台费用里，必须直接从收入扣掉。
//
// 增值税按"只承担成本以上的税"计算：
//
//	应交 = 销项 − 进项 − 平台费专票抵扣
//
// 运费不参与（用户明确：运费不计入结算）。
type Settlement struct {
	// 收入（含税，已扣平台代付垫支）
	Revenue Money // 销售金额 = 货款 + 补贴 − 垫支扣回
	Qty     Qty   // 数量

	// 收入构成（列示用）
	Goods   Money // 货款（平台打款的商品款）
	Subsidy Money // 平台补贴（如淘金币）
	Advance Money // 平台代付垫支（从货款扣回，负数体现）

	// 成本（含税，上游供货价）
	Cost Money

	// 平台费用（现金口径：只算真正从商家扣走、且开票的）
	PlatformFee Money
}

// OutputVat 销项税 = 销售额 ÷ (1+销项率) × 销项率。
func (s Settlement) OutputVat(r VatRates) Money {
	return vatOf(s.Revenue, r.Output)
}

// InputVat 进项税（成本可抵扣部分）。
func (s Settlement) InputVat(r VatRates) Money {
	return vatOf(s.Cost, r.Input)
}

// PlatformVat 平台服务费专票可抵扣的进项。
func (s Settlement) PlatformVat(r VatRates) Money {
	return vatOf(s.PlatformFee, r.Platform)
}

// VatPayable 应交增值税 = 销项 − 进项 − 平台费抵扣。
//
// 也就是"只承担成本以上的税"：成本那部分的税可以抵掉，
// 平台开票的服务费也能抵掉。
func (s Settlement) VatPayable(r VatRates) Money {
	v := s.OutputVat(r) - s.InputVat(r) - s.PlatformVat(r)
	if v < 0 {
		return v // 留抵（负数）如实体现，不强行归零
	}
	return v
}

// Net 结算金额 = 销售 − 成本 − 应交增值税。
//
// 注意：平台费用不再重复扣——平台打款时已经扣过了，
// 这里的 Revenue 用的就是实际到账的货款。
func (s Settlement) Net(r VatRates) Money {
	return s.Revenue - s.Cost - s.VatPayable(r)
}

// GrossProfit 毛利（不含税口径）= 销售 − 成本 − 应交增值税。
func (s Settlement) GrossProfit(r VatRates) Money { return s.Net(r) }

// Margin 毛利率（%）。
func (s Settlement) Margin(r VatRates) float64 { return Ratio(s.Net(r), s.Revenue) }

// vatOf 从含税金额里拆出税额：金额 ÷ (1+率) × 率。
func vatOf(amount Money, ratePercent float64) Money {
	if amount == 0 || ratePercent == 0 {
		return 0
	}
	base := amount.Float() / (1 + ratePercent/100)
	return MoneyFromFloat(base * ratePercent / 100)
}

// Add 累加另一个结算行。
func (s *Settlement) Add(o Settlement) {
	s.Revenue += o.Revenue
	s.Goods += o.Goods
	s.Subsidy += o.Subsidy
	s.Advance += o.Advance
	s.Qty += o.Qty
	s.Cost += o.Cost
	s.PlatformFee += o.PlatformFee
}

// EcSettlementRow 结算表里的一行（按订单或按商品）。
type EcSettlementRow struct {
	Key      string // 订单号或商品名
	OrderNo  string
	Title    string
	EcID     string
	Product  string
	Accounts []SettlementDetail
	Sum      Settlement
	Vat      VatRates
}

// SettlementDetail 一次平台扣费。
type SettlementDetail struct {
	Period string
	Kind   string
	Amount Money
	Note   string
}

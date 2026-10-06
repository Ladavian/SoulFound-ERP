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
	TrackingNo   string
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

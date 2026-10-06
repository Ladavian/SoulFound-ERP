package model

import "strings"

// EcPlatform 平台标识。
const (
	EcTaobao      = "taobao"
	EcDouyin      = "douyin"
	EcJD          = "jd"
	EcXiaohongshu = "xiaohongshu"
	EcWechat      = "wechat"
	EcOther       = "other"
)

// EcPlatformLabels 平台中文名。
var EcPlatformLabels = map[string]string{
	EcTaobao:      "淘宝 / 天猫",
	EcDouyin:      "抖音小店",
	EcJD:          "京东",
	EcXiaohongshu: "小红书",
	EcWechat:      "微信小店",
	EcOther:       "其它平台",
}

// EcPlatforms 平台顺序（界面下拉用）。先做淘宝，其余已预留。
var EcPlatforms = []string{EcTaobao, EcDouyin, EcJD, EcXiaohongshu, EcWechat, EcOther}

// EcPlatformOptions 下拉选项。
func EcPlatformOptions() []Option {
	out := make([]Option, 0, len(EcPlatforms))
	for _, p := range EcPlatforms {
		out = append(out, Option{Value: p, Label: EcPlatformLabel(p)})
	}
	return out
}

// EcPlatformSupported 是否已经跑通的平台（导入解析按平台区分）。
func EcPlatformSupported(p string) bool { return p == EcTaobao }

// NormalizeEcSKU 规范化平台规格文本。
//
// 淘宝导出的「商品属性」形如「商品规格:1瓶装」「商品规格:手拎袋」，
// 同一个商品ID 下会有多个规格。绑定与匹配都要用同一个规范值，
// 所以统一把前面的「键:」前缀去掉，只留规格本身。
// 全角冒号也一起处理。
func NormalizeEcSKU(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	for _, sep := range []string{":", "："} {
		if i := strings.Index(s, sep); i > 0 {
			key := s[:i]
			// 前缀必须是短标签（没有空格），避免把规格里本身带冒号的内容切坏
			if len([]rune(key)) <= 8 && !strings.ContainsAny(key, " \t") {
				s = strings.TrimSpace(s[i+len(sep):])
			}
		}
	}
	return s
}

// ProductEcLink 产品与平台商品的绑定关系。
//
// 一个 ERP 产品可以绑多个平台商品ID：
// 同一个产品在淘宝可能有好几个链接，再加上抖音、京东等各一个。
type ProductEcLink struct {
	ID          int64
	ProductID   int64
	Platform    string
	EcProductID string
	EcSKUId     string
	Title       string
	Note        string
	CreatedAt   string

	// 关联查询
	ProductName string
	ProductSKU  string
}

// PlatformLabel 该绑定的平台中文名。
func (l ProductEcLink) PlatformLabel() string { return EcPlatformLabel(l.Platform) }

// EcPlatformLabel 平台中文名。
func EcPlatformLabel(p string) string {
	if v, ok := EcPlatformLabels[p]; ok {
		return v
	}
	return p
}

// ---------------------------------------------------------------- 虚拟组套

// ProductBundle 虚拟组套：把一个产品定义成若干产品的组合。
//
// 平台上会卖「2瓶装」「A+B套装」这类组合，它们在仓库里没有实体库存，
// 由组成产品拼出来。成本按组成累加，不占用自己的库存。
type ProductBundle struct {
	ID          int64
	ProductID   int64
	ComponentID int64

	// 关联查询字段
	ComponentName string
	ComponentSKU  string
	ComponentUnit string
	ComponentEC   Money // 组成产品的电商成本
	ComponentAvg  Money // 组成产品的平均成本（电商成本没填时兜底）

	Qty       Qty
	SortOrder int
}

// CostBasis 算成本时该组成用哪个单价：优先电商成本。
func (b ProductBundle) CostBasis() Money {
	if b.ComponentEC > 0 {
		return b.ComponentEC
	}
	return b.ComponentAvg
}

// ---------------------------------------------------------------- 平台订单

// EcOrder 平台订单（主订单）。
type EcOrder struct {
	ID           int64
	Platform     string
	OrderNo      string
	Status       string
	RefundStatus string
	CreatedAt    string
	PaidAt       string
	ShippedAt    string
	BuyerNote    string
	SellerNote   string
	ImportedAt   string

	Items []EcOrderItem
}

// EcOrderItem 平台订单明细（子订单）。
type EcOrderItem struct {
	ID            int64
	OrderID       int64
	Platform      string
	SubOrderNo    string
	Title         string
	EcProductID   string
	EcSKU         string
	MerchantCode  string
	Qty           Qty
	UnitPrice     Money
	PayableAmount Money
	PaidAmount    Money
	RefundStatus  string
	RefundAmount  Money
	ItemStatus    string
	LogisticsNo   string
	LogisticsCo   string
	ProductID     *int64
	ImportedAt    string

	// 关联查询字段
	ProductName string
	ProductSKU  string
	EcCost      Money // 绑定产品的电商成本
}

// IsClosed 订单是否已关闭（关闭的单不计入销售）。
func (o EcOrder) IsClosed() bool { return strings.Contains(o.Status, "关闭") }

// IsRefunded 是否整单退款成功。
func (o EcOrder) IsRefunded() bool { return strings.Contains(o.RefundStatus, "退款成功") }

// IsShipped 是否已发货。
func (o EcOrder) IsShipped() bool {
	return o.ShippedAt != "" || strings.Contains(o.Status, "已发货")
}

// Counts 该行是否算作有效销售：未关闭、未退款。
func (i EcOrderItem) Counts() bool {
	if strings.Contains(i.ItemStatus, "关闭") || strings.Contains(i.RefundStatus, "退款成功") {
		return false
	}
	return true
}

// NetPaid 扣掉退款后的实付金额。
func (i EcOrderItem) NetPaid() Money {
	if i.RefundAmount > 0 && i.RefundAmount < i.PaidAmount {
		return i.PaidAmount - i.RefundAmount
	}
	if strings.Contains(i.RefundStatus, "退款成功") {
		return 0
	}
	return i.PaidAmount
}

// ItemCost 该行的商品成本 = 电商成本 × 数量。
//
// 没填电商成本时返回 0，界面上会提示去补，
// 避免用平均成本算出一个"看起来对但口径不对"的毛利。
func (i EcOrderItem) ItemCost() Money {
	if i.EcCost <= 0 {
		return 0
	}
	return MulQty(i.Qty, i.EcCost)
}

// ItemProfit 该行毛利 = 实付 − 成本。
func (i EcOrderItem) ItemProfit() Money { return i.NetPaid() - i.ItemCost() }

// Matched 是否已绑定到 ERP 产品。
func (i EcOrderItem) Matched() bool { return i.ProductID != nil }

// QtyTotal 订单总件数。
func (o EcOrder) QtyTotal() Qty {
	var q Qty
	for _, it := range o.Items {
		q += it.Qty
	}
	return q
}

// PaidTotal 订单实付合计（含退款，未扣）。
func (o EcOrder) PaidTotal() Money {
	var t Money
	for _, it := range o.Items {
		t += it.PaidAmount
	}
	return t
}

// RefundTotal 订单退款合计。
func (o EcOrder) RefundTotal() Money {
	var t Money
	for _, it := range o.Items {
		t += it.RefundAmount
	}
	return t
}

// DiffNet 实付与实收的差额（= 退款）。
func (o EcOrder) DiffNet() Money { return o.PaidTotal() - o.NetTotal() }

// NetTotal 订单实收合计（扣掉退款）。
func (o EcOrder) NetTotal() Money {
	var t Money
	for _, it := range o.Items {
		t += it.NetPaid()
	}
	return t
}

// CostTotal 订单成本合计。
func (o EcOrder) CostTotal() Money {
	var t Money
	for _, it := range o.Items {
		t += it.ItemCost()
	}
	return t
}

// Profit 订单毛利。
func (o EcOrder) Profit() Money { return o.NetTotal() - o.CostTotal() }

// Margin 毛利率（%）。
func (o EcOrder) Margin() float64 { return Ratio(o.Profit(), o.NetTotal()) }

// HasMissingCost 是否还有没填电商成本的行（这种行算出来的毛利不准）。
func (o EcOrder) HasMissingCost() bool {
	for _, it := range o.Items {
		if it.Matched() && it.EcCost <= 0 {
			return true
		}
	}
	return false
}

// UnmatchedCount 未绑定产品的行数。
func (o EcOrder) UnmatchedCount() int {
	n := 0
	for _, it := range o.Items {
		if !it.Matched() {
			n++
		}
	}
	return n
}

// EcImportSummary 一次导入的结果。
type EcImportSummary struct {
	Platform    string
	Orders      int // 新建订单数
	OrdersUpd   int // 更新订单数
	Items       int // 明细行数
	Matched     int // 自动匹配到产品的行数
	Unmatched   int // 未匹配的行数
	Skipped     int // 跳过的行数
	Errors      []string
	UnmatchedEc []EcUnmatchedItem // 未匹配的商品（按电商商品ID 归并）
}

// EcUnmatchedItem 未匹配上产品的电商商品。
type EcUnmatchedItem struct {
	EcProductID  string
	EcSKU        string
	Title        string
	MerchantCode string
	Qty          Qty
	Orders       int
}

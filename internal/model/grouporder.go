package model

import "strings"

// 团单状态。
const (
	GroupDraft     = "draft"     // 草稿
	GroupConfirmed = "confirmed" // 已确认
	GroupShipped   = "shipped"   // 已出货
	GroupDone      = "done"      // 已完成
	GroupCancelled = "cancelled" // 已取消
)

// GroupStatusLabels 状态中文名。
var GroupStatusLabels = map[string]string{
	GroupDraft:     "草稿",
	GroupConfirmed: "已确认",
	GroupShipped:   "已出货",
	GroupDone:      "已完成",
	GroupCancelled: "已取消",
}

// GroupStatusOptions 状态选项。
var GroupStatusOptions = []Option{
	{GroupDraft, "草稿"},
	{GroupConfirmed, "已确认"},
	{GroupShipped, "已出货"},
	{GroupDone, "已完成"},
	{GroupCancelled, "已取消"},
}

// GroupStatusLabel 状态中文名。
func GroupStatusLabel(status string) string {
	if v, ok := GroupStatusLabels[status]; ok {
		return v
	}
	return status
}

// GroupOrder 线下大团单。
//
// 这类订单由大仓发货，不经过本系统的仓库，因此**不产生任何库存流水**，
// 只作为销售订单记录留存：客户、明细、金额、状态。
type GroupOrder struct {
	ID            int64
	Code          string
	CustomerID    *int64
	CustomerName  string
	Contact       string
	Phone         string
	OrderDate     string
	ShipDate      string
	Warehouse     string
	Status        string
	Discount      Money // 整单优惠
	ExtraFee      Money // 运费等其它费用
	Note          string
	CreatedBy     *int64
	CreatedByName string
	CreatedAt     string
	UpdatedAt     string

	Items []GroupOrderItem
}

// GroupOrderItem 团单明细行。
//
// ProductID 可以为空：团单里可能有系统档案里没有的产品，
// 这时用 ProductName 手填名称，只做记录。
type GroupOrderItem struct {
	ID          int64
	OrderID     int64
	ProductID   *int64
	ProductName string
	SKU         string
	Qty         Qty
	Unit        string
	UnitPrice   Money
	Note        string
	SortOrder   int
}

// Amount 该行金额。
func (i GroupOrderItem) Amount() Money { return MulQty(i.Qty, i.UnitPrice) }

// StatusLabel 状态中文名。
func (o GroupOrder) StatusLabel() string { return GroupStatusLabel(o.Status) }

// IsClosed 是否已结束（完成或取消）。
func (o GroupOrder) IsClosed() bool {
	return o.Status == GroupDone || o.Status == GroupCancelled
}

// TotalQty 明细数量合计。
func (o GroupOrder) TotalQty() Qty {
	var q Qty
	for _, it := range o.Items {
		q += it.Qty
	}
	return q
}

// Subtotal 明细金额合计（未减优惠）。
func (o GroupOrder) Subtotal() Money {
	var t Money
	for _, it := range o.Items {
		t += it.Amount()
	}
	return t
}

// Total 订单应付金额 = 明细合计 − 优惠 + 其它费用。
func (o GroupOrder) Total() Money { return o.Subtotal() - o.Discount + o.ExtraFee }

// ItemCount 明细行数。
func (o GroupOrder) ItemCount() int { return len(o.Items) }

// Title 订单标题：客户名，没有就显示单号。
func (o GroupOrder) Title() string {
	if strings.TrimSpace(o.CustomerName) != "" {
		return o.CustomerName
	}
	return o.Code
}

// GroupOrderSummary 团单汇总（列表页顶部指标）。
type GroupOrderSummary struct {
	Count    int
	Qty      Qty
	Amount   Money
	ByStatus map[string]int
}

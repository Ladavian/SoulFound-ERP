package model

import (
	"fmt"
	"strings"
	"time"
)

// Option 下拉框选项。
type Option struct {
	Value string
	Label string
}

// Options 由 "value,label" 交替参数构造选项列表。
func Options(pairs ...string) []Option {
	out := make([]Option, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Option{Value: pairs[i], Label: pairs[i+1]})
	}
	return out
}

// ---------------------------------------------------------------- 角色

// 系统角色。
const (
	RoleAdmin   = "admin"   // 管理员：全部权限
	RoleManager = "manager" // 经理：全部业务权限（含成本与结算）
	RoleStaff   = "staff"   // 店员：录入市集/采购数据，看不到成本利润
	RoleViewer  = "viewer"  // 只读
)

// RoleLabels 角色中文名。
var RoleLabels = map[string]string{
	RoleAdmin:   "管理员",
	RoleManager: "经理",
	RoleStaff:   "店员",
	RoleViewer:  "只读",
}

// RoleOptions 角色下拉选项。
var RoleOptions = []Option{
	{RoleManager, "经理（全部业务权限）"},
	{RoleStaff, "店员（录单、市集数据录入）"},
	{RoleViewer, "只读（仅查看）"},
}

// User 系统用户。
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	FullName     string
	Role         string
	IsActive     bool
	LastLoginAt  string
	CreatedAt    string

	// Permissions 自定义权限点。
	//
	// 为空表示沿用角色的默认权限（老账号与默认行为不变）；
	// 非空则完全以它为准，角色只作为勾选时的预设模板。
	Permissions []string
}

// PermissionNone 表示"一项权限都不给"的显式标记。
//
// 空列表在存储上等于"沿用角色默认"，所以要用一个标记把
// "用户明确选择了不给任何权限"表达出来。
const PermissionNone = "none"

// PermissionsText 自定义权限的存储文本（逗号分隔）。
func (u User) PermissionsText() string {
	return strings.Join(u.Permissions, ",")
}

// HasCustomPermissions 是否单独指定过权限。
func (u User) HasCustomPermissions() bool { return len(u.Permissions) > 0 }

// DisplayName 展示名，优先真实姓名。
func (u User) DisplayName() string {
	if strings.TrimSpace(u.FullName) != "" {
		return u.FullName
	}
	return u.Username
}

// Initial 头像首字母。
func (u User) Initial() string {
	name := u.DisplayName()
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// RoleLabel 角色中文名。
func (u User) RoleLabel() string { return RoleLabel(u.Role) }

// RoleLabel 角色中文名。
func RoleLabel(role string) string {
	if v, ok := RoleLabels[role]; ok {
		return v
	}
	return role
}

// ---------------------------------------------------------------- 往来单位

// Supplier 供应商（酒庄/进口商）。
type Supplier struct {
	ID          int64
	Name        string
	ContactName string
	Phone       string
	Email       string
	Country     string
	Address     string
	Notes       string
	IsActive    bool
	CreatedAt   string
}

// Customer 客户（市集现场零售客户，可留空）。
type Customer struct {
	ID        int64
	Name      string
	Phone     string
	Email     string
	Address   string
	Notes     string
	IsActive  bool
	CreatedAt string
}

// ---------------------------------------------------------------- 产品

// Product 产品档案，含库存与移动加权平均成本的冗余缓存。
type Product struct {
	ID             int64
	SKU            string
	Name           string
	NameEn         string
	Category       string
	Vintage        int
	VolumeML       int
	Unit           string
	BottlesPerCase int
	SalePrice      Money
	Barcode        string
	LowStockQty    Qty
	SupplierID     *int64
	SupplierName   string
	ImageURL       string
	Notes          string
	IsActive       bool

	StockQty   Qty
	AvgCost    Money
	StockValue Money

	CreatedAt string
	UpdatedAt string
}

// Label 下拉框展示文本。
func (p Product) Label() string {
	if p.Vintage > 0 {
		return fmt.Sprintf("%s · %d %s", p.SKU, p.Vintage, p.Name)
	}
	return fmt.Sprintf("%s · %s", p.SKU, p.Name)
}

// IsLowStock 是否低于预警线。
func (p Product) IsLowStock() bool {
	return p.StockQty <= p.LowStockQty
}

// IsOutOfStock 是否无库存。
func (p Product) IsOutOfStock() bool { return p.StockQty <= 0 }

// Spec 规格描述，如 "375ml / 6瓶装"。
func (p Product) Spec() string {
	parts := make([]string, 0, 2)
	if p.VolumeML > 0 {
		parts = append(parts, fmt.Sprintf("%dml", p.VolumeML))
	}
	if p.BottlesPerCase > 1 {
		parts = append(parts, fmt.Sprintf("%d瓶/箱", p.BottlesPerCase))
	}
	return strings.Join(parts, " · ")
}

// Margin 按当前售价与平均成本的毛利率（%）。
func (p Product) Margin() float64 {
	if p.SalePrice == 0 {
		return 0
	}
	return float64(p.SalePrice-p.AvgCost) / float64(p.SalePrice) * 100
}

// Categories 产品品类选项。
var Categories = []string{"冰酒", "晚收甜酒", "红酒", "白酒", "起泡酒", "其他"}

// ---------------------------------------------------------------- 库存流水

// 出入库原因。
const (
	ReasonPurchase      = "purchase"       // 采购入库
	ReasonOpening       = "opening"        // 期初建账
	ReasonAdjustIn      = "adjust_in"      // 盘盈/调整增加
	ReasonAdjustOut     = "adjust_out"     // 盘亏/调整减少
	ReasonReturnIn      = "return_in"      // 退货入库
	ReasonMarketSale    = "market_sale"    // 市集销售
	ReasonMarketTasting = "market_tasting" // 市集试饮
	ReasonMarketGift    = "market_gift"    // 市集赠送
	ReasonMarketLoss    = "market_loss"    // 市集损耗
)

// ReasonLabels 原因中文名。
var ReasonLabels = map[string]string{
	ReasonPurchase:      "采购入库",
	ReasonOpening:       "期初建账",
	ReasonAdjustIn:      "盘点调增",
	ReasonAdjustOut:     "盘点调减",
	ReasonReturnIn:      "退货入库",
	ReasonMarketSale:    "市集销售",
	ReasonMarketTasting: "市集试饮",
	ReasonMarketGift:    "市集赠送",
	ReasonMarketLoss:    "市集损耗",
}

// ReasonLabel 原因中文名。
func ReasonLabel(reason string) string {
	if v, ok := ReasonLabels[reason]; ok {
		return v
	}
	return reason
}

// 出入库方向。界面用「入库 / 出库」两个按钮代替让用户填正负号。
const (
	DirectionIn  = "in"
	DirectionOut = "out"
)

// DirectionInReasons / DirectionOutReasons 手工登记时各方向可选的原因。
var (
	DirectionInReasons = []Option{
		{ReasonOpening, "期初建账（开始用系统时的现有库存）"},
		{ReasonAdjustIn, "盘点调增（实物比账面多）"},
		{ReasonReturnIn, "退货入库（客户退回）"},
	}
	DirectionOutReasons = []Option{
		{ReasonAdjustOut, "盘点调减（实物比账面少）"},
		{ReasonMarketLoss, "破损 / 损耗"},
		{ReasonMarketGift, "赠送 / 公关用酒"},
	}
)

// ReasonDirection 该原因属于哪个方向；不确定时返回空。
func ReasonDirection(reason string) string {
	for _, o := range DirectionInReasons {
		if o.Value == reason {
			return DirectionIn
		}
	}
	for _, o := range DirectionOutReasons {
		if o.Value == reason {
			return DirectionOut
		}
	}
	return ""
}

// ReasonOptions 出库原因选项（手工出库单用）。
var ReasonOptions = []Option{
	{ReasonMarketSale, "市集销售"},
	{ReasonMarketTasting, "市集试饮"},
	{ReasonMarketGift, "市集赠送"},
	{ReasonMarketLoss, "市集损耗"},
	{ReasonAdjustIn, "盘点调增"},
	{ReasonAdjustOut, "盘点调减"},
	{ReasonReturnIn, "退货入库"},
	{ReasonOpening, "期初建账"},
	{ReasonPurchase, "采购入库"},
}

// MovementFilterOptions 库存流水筛选选项。
var MovementFilterOptions = []Option{
	{"", "全部类型"},
	{ReasonPurchase, "采购入库"},
	{ReasonOpening, "期初建账"},
	{ReasonAdjustIn, "盘点调增"},
	{ReasonAdjustOut, "盘点调减"},
	{ReasonReturnIn, "退货入库"},
	{ReasonMarketSale, "市集销售"},
	{ReasonMarketTasting, "市集试饮"},
	{ReasonMarketGift, "市集赠送"},
	{ReasonMarketLoss, "市集损耗"},
}

// StockMovement 库存流水（收付实现制台账）。
type StockMovement struct {
	ID            int64
	ProductID     int64
	ProductName   string
	ProductSKU    string
	OccurredOn    string
	Qty           Qty // 正数入库、负数出库
	UnitCost      Money
	TotalCost     Money
	QtyAfter      Qty
	AvgCostAfter  Money
	ValueAfter    Money
	Reason        string
	RefType       string
	RefID         *int64
	RefCode       string
	Note          string
	CreatedBy     *int64
	CreatedByName string
	CreatedAt     string
}

// ReasonLabel 原因中文名。
func (m StockMovement) ReasonLabel() string { return ReasonLabel(m.Reason) }

// IsIn 是否入库。
func (m StockMovement) IsIn() bool { return m.Qty > 0 }

// DirectionLabel 方向文本。
func (m StockMovement) DirectionLabel() string {
	if m.Qty > 0 {
		return "入库"
	}
	return "出库"
}

// AbsQty 绝对值数量。
func (m StockMovement) AbsQty() Qty {
	if m.Qty < 0 {
		return -m.Qty
	}
	return m.Qty
}

// ---------------------------------------------------------------- 采购单

// 采购单状态。
const (
	PurchaseDraft     = "draft"     // 草稿
	PurchaseConfirmed = "confirmed" // 已入库
	PurchaseVoid      = "void"      // 已作废
)

// PurchaseStatusLabels 采购单状态中文名。
var PurchaseStatusLabels = map[string]string{
	PurchaseDraft:     "草稿",
	PurchaseConfirmed: "已入库",
	PurchaseVoid:      "已作废",
}

// PurchaseStatusOptions 采购单状态筛选项。
var PurchaseStatusOptions = []Option{
	{"", "全部状态"},
	{PurchaseDraft, "草稿"},
	{PurchaseConfirmed, "已入库"},
	{PurchaseVoid, "已作废"},
}

// Purchase 采购入库单。
type Purchase struct {
	ID           int64
	Code         string
	SupplierID   *int64
	SupplierName string
	PurchaseDate string
	Currency     string
	Status       string
	AllocMethod  string // 附加费用分摊方式：amount 按金额 / qty 按数量

	GoodsAmount  Money // 货款
	ShippingCost Money // 运费
	TariffCost   Money // 关税/税费
	OtherCost    Money // 其他费用
	TotalCost    Money // 合计（含分摊）

	Notes         string
	CreatedBy     *int64
	CreatedByName string
	ConfirmedAt   string
	CreatedAt     string
	UpdatedAt     string

	Items []PurchaseItem
}

// 附加费用分摊方式。
const (
	AllocByAmount = "amount" // 按货款金额比例分摊
	AllocByQty    = "qty"    // 按数量比例分摊
)

// AllocMethodOptions 分摊方式选项。
var AllocMethodOptions = []Option{
	{AllocByAmount, "按货款金额比例（推荐）"},
	{AllocByQty, "按入库数量比例"},
}

// AllocMethodLabel 分摊方式中文名。
func (p Purchase) AllocMethodLabel() string {
	if p.AllocMethod == AllocByQty {
		return "按数量"
	}
	return "按金额"
}

// StatusLabel 状态中文名。
func (p Purchase) StatusLabel() string {
	if v, ok := PurchaseStatusLabels[p.Status]; ok {
		return v
	}
	return p.Status
}

// StatusClass 状态样式。
func (p Purchase) StatusClass() string {
	switch p.Status {
	case PurchaseConfirmed:
		return "ok"
	case PurchaseVoid:
		return "muted"
	default:
		return "warn"
	}
}

// IsDraft 是否草稿（可编辑）。
func (p Purchase) IsDraft() bool { return p.Status == PurchaseDraft }

// IsConfirmed 是否已入库。
func (p Purchase) IsConfirmed() bool { return p.Status == PurchaseConfirmed }

// ExtraCost 附加费用合计。
func (p Purchase) ExtraCost() Money { return p.ShippingCost + p.TariffCost + p.OtherCost }

// TotalQty 入库总数量。
func (p Purchase) TotalQty() Qty {
	var total Qty
	for _, it := range p.Items {
		total += it.Qty
	}
	return total
}

// ItemCount 明细行数。
func (p Purchase) ItemCount() int { return len(p.Items) }

// PurchaseItem 采购明细行。
type PurchaseItem struct {
	ID             int64
	PurchaseID     int64
	ProductID      int64
	ProductName    string
	ProductSKU     string
	Qty            Qty
	UnitPrice      Money // 采购单价（不含分摊）
	Amount         Money // 货款 = Qty × UnitPrice
	ExtraAlloc     Money // 分摊到的附加费用
	LandedUnitCost Money // 到岸成本单价 = (Amount + ExtraAlloc) / Qty
	Note           string
}

// LandedAmount 该行到岸总成本。
func (i PurchaseItem) LandedAmount() Money { return i.Amount + i.ExtraAlloc }

// ---------------------------------------------------------------- 市集活动

// 市集状态。
const (
	MarketPlanned = "planned" // 计划中
	MarketOngoing = "ongoing" // 进行中
	MarketSettled = "settled" // 已结算（已扣库存、锁定利润）
)

// MarketStatusLabels 市集状态中文名。
var MarketStatusLabels = map[string]string{
	MarketPlanned: "计划中",
	MarketOngoing: "进行中",
	MarketSettled: "已结算",
}

// MarketStatusOptions 市集状态选项。
var MarketStatusOptions = []Option{
	{MarketPlanned, "计划中"},
	{MarketOngoing, "进行中"},
	{MarketSettled, "已结算"},
}

// MarketStatusFilterOptions 市集状态筛选项。
var MarketStatusFilterOptions = []Option{
	{"", "全部状态"},
	{MarketPlanned, "计划中"},
	{MarketOngoing, "进行中"},
	{MarketSettled, "已结算"},
}

// Market 市集活动。金额字段为结算/统计快照。
type Market struct {
	ID        int64
	Code      string
	Name      string
	Venue     string
	City      string
	Organizer string
	StartDate string
	EndDate   string
	Status    string
	Notes     string

	Revenue      Money // 销售额
	CogsSold     Money // 售出部分成本
	TastingCost  Money // 试饮成本
	LossCost     Money // 赠送 + 损耗成本
	ExpenseTotal Money // 各项活动费用
	NetProfit    Money // 净利润

	CreatedBy     *int64
	CreatedByName string
	SettledAt     string
	SettledByName string
	CreatedAt     string
	UpdatedAt     string

	Items    []MarketItem
	Expenses []MarketExpense
	Records  []MarketRecord
}

// Totals 本场损益。有逐笔记录时按记录精确计算，否则退回汇总数量。
func (m Market) Totals() MarketTotals {
	return ComputeTotals(m.Items, m.Expenses, m.Records)
}

// SaleCount 销售笔数（现场收银用）。
func (m Market) SaleCount() int {
	n := 0
	for _, r := range m.Records {
		if r.Kind == RecordSale {
			n++
		}
	}
	return n
}

// StatusLabel 状态中文名。
func (m Market) StatusLabel() string {
	if v, ok := MarketStatusLabels[m.Status]; ok {
		return v
	}
	return m.Status
}

// StatusClass 状态样式。
func (m Market) StatusClass() string {
	switch m.Status {
	case MarketSettled:
		return "ok"
	case MarketOngoing:
		return "live"
	default:
		return "warn"
	}
}

// IsSettled 是否已结算。
func (m Market) IsSettled() bool { return m.Status == MarketSettled }

// CanEditItems 是否可编辑明细（已结算需先撤销结算）。
func (m Market) CanEditItems() bool { return m.Status != MarketSettled }

// DateRange 日期区间文本。
func (m Market) DateRange() string {
	if m.EndDate == "" || m.EndDate == m.StartDate {
		return m.StartDate
	}
	return m.StartDate + " ~ " + m.EndDate
}

// Days 活动天数。
func (m Market) Days() int {
	start, err1 := time.Parse("2006-01-02", m.StartDate)
	end, err2 := time.Parse("2006-01-02", m.EndDate)
	if err1 != nil || err2 != nil || end.Before(start) {
		return 1
	}
	return int(end.Sub(start).Hours()/24) + 1
}

// GrossProfit 毛利 = 销售额 - 售出成本。
func (m Market) GrossProfit() Money { return m.Revenue - m.CogsSold }

// GrossMargin 毛利率（%）。
func (m Market) GrossMargin() float64 { return Ratio(m.GrossProfit(), m.Revenue) }

// TotalOutflow 总支出 = 售出成本 + 试饮成本 + 赠送损耗 + 活动费用。
func (m Market) TotalOutflow() Money {
	return m.CogsSold + m.TastingCost + m.LossCost + m.ExpenseTotal
}

// NetMargin 净利率（%）。
func (m Market) NetMargin() float64 { return Ratio(m.NetProfit, m.Revenue) }

// NetProfitPerDay 日均净利润。
func (m Market) NetProfitPerDay() Money {
	if d := m.Days(); d > 0 {
		return Money(int64(m.NetProfit) / int64(d))
	}
	return 0
}

// TastingQty 试饮总杯数。
func (m Market) TastingQty() Qty {
	var total Qty
	for _, it := range m.Items {
		total += it.TastingQty
	}
	return total
}

// SoldQty 售出总瓶数。
func (m Market) SoldQty() Qty {
	var total Qty
	for _, it := range m.Items {
		total += it.SoldQty
	}
	return total
}

// ConversionRate 试饮带动倍数 = 销售瓶数 / 试饮瓶数。
//
// 试饮按「瓶」记录，因此这个指标的含义是「每投入 1 瓶试饮，带动卖出多少瓶」。
func (m Market) ConversionRate() float64 {
	tasting := m.TastingQty()
	if tasting == 0 {
		return 0
	}
	return float64(m.SoldQty()) / float64(tasting)
}

// SalesMultiplier ConversionRate 的同义方法，便于模板表达。
func (m Market) SalesMultiplier() float64 { return m.ConversionRate() }

// GiftQty 赠送总瓶数。
func (m Market) GiftQty() Qty {
	var total Qty
	for _, it := range m.Items {
		total += it.GiftQty
	}
	return total
}

// LossQty 损耗总瓶数。
func (m Market) LossQty() Qty {
	var total Qty
	for _, it := range m.Items {
		total += it.LossQty
	}
	return total
}

// OutQty 总出库瓶数（试饮 + 销售 + 赠送 + 损耗）。
func (m Market) OutQty() Qty {
	return m.TastingQty() + m.SoldQty() + m.GiftQty() + m.LossQty()
}

// CarriedQty 带去的总瓶数。
func (m Market) CarriedQty() Qty {
	var total Qty
	for _, it := range m.Items {
		total += it.CarriedQty
	}
	return total
}

// CostPerTasting 每杯试饮成本。
func (m Market) CostPerTasting() Money { return DivByQty(m.TastingCost, m.TastingQty()) }

// CostEstimated 成本是否为预估值（未结算时按当前平均成本估算）。
func (m Market) CostEstimated() bool { return m.Status != MarketSettled }

// AvgOrderValue 单瓶均价。
func (m Market) AvgOrderValue() Money { return DivByQty(m.Revenue, m.SoldQty()) }

// MarketTotals 一场市集的损益汇总。
type MarketTotals struct {
	Revenue     Money // 销售额
	CogsSold    Money // 售出成本
	TastingCost Money // 试饮成本
	LossCost    Money // 赠送 + 损耗成本
	ExpenseCost Money // 活动费用
	NetProfit   Money // 净利润
}

// GrossProfit 毛利 = 销售额 - 售出成本。
func (t MarketTotals) GrossProfit() Money { return t.Revenue - t.CogsSold }

// GrossMargin 毛利率（%）。
func (t MarketTotals) GrossMargin() float64 { return Ratio(t.GrossProfit(), t.Revenue) }

// NetMargin 净利率（%）。
func (t MarketTotals) NetMargin() float64 { return Ratio(t.NetProfit, t.Revenue) }

// TotalCost 总成本支出。
func (t MarketTotals) TotalCost() Money {
	return t.CogsSold + t.TastingCost + t.LossCost + t.ExpenseCost
}

// 市集现场记录类型。
const (
	RecordSale    = "sale"
	RecordTasting = "tasting"
	RecordGift    = "gift"
	RecordLoss    = "loss"
)

// RecordKindLabels 记录类型中文名。
var RecordKindLabels = map[string]string{
	RecordSale:    "销售",
	RecordTasting: "试饮",
	RecordGift:    "赠送",
	RecordLoss:    "损耗",
}

// RecordKindOptions 记录类型选项。
var RecordKindOptions = []Option{
	{RecordSale, "销售"},
	{RecordTasting, "试饮"},
	{RecordGift, "赠送"},
	{RecordLoss, "损耗"},
}

// RecordKindLabel 记录类型中文名。
func RecordKindLabel(kind string) string {
	if v, ok := RecordKindLabels[kind]; ok {
		return v
	}
	return kind
}

// MarketRecord 市集现场的一笔记录：卖出一单、试饮一瓶、赠送或损耗一瓶。
//
// 这是市集业务的事实来源：销售额、成本都按它逐笔累计，
// market_items 上的汇总数量只是它的缓存（用于列表展示与结算过账）。
type MarketRecord struct {
	ID            int64
	MarketID      int64
	ProductID     int64
	ProductName   string
	ProductSKU    string
	Kind          string
	Qty           Qty
	UnitPrice     Money  // 成交单价（仅销售有意义）
	Discount      Money  // 该笔优惠
	UnitCost      *Money // 结算时的成本快照；未结算为 nil
	Channel       string // manual 手点 / scan 扫码 / migrated 历史迁移
	Barcode       string
	Note          string
	OccurredAt    string
	CreatedBy     *int64
	CreatedByName string
	CreatedAt     string

	// 关联查询字段：产品当前平均成本，未结算时作为成本估算
	AvgCost Money
}

// KindLabel 类型中文名。
func (r MarketRecord) KindLabel() string { return RecordKindLabel(r.Kind) }

// IsSale 是否为销售记录。
func (r MarketRecord) IsSale() bool { return r.Kind == RecordSale }

// Amount 该笔销售额（仅销售）。
func (r MarketRecord) Amount() Money {
	if r.Kind != RecordSale {
		return 0
	}
	return MulQty(r.Qty, r.UnitPrice) - r.Discount
}

// HasSnapshot 是否已锁定成本。
func (r MarketRecord) HasSnapshot() bool { return r.UnitCost != nil }

// CostBasis 成本计价基础：已结算用快照，否则用当前平均成本。
func (r MarketRecord) CostBasis() Money {
	if r.UnitCost != nil {
		return *r.UnitCost
	}
	return r.AvgCost
}

// Cost 该笔成本（销售计入售出成本，试饮/赠送/损耗计入对应支出）。
func (r MarketRecord) Cost() Money { return MulQty(r.Qty, r.CostBasis()) }

// FromScan 是否由扫码产生。
func (r MarketRecord) FromScan() bool { return r.Channel == "scan" }

// Clock 时间文本（HH:MM），列表展示用。
func (r MarketRecord) Clock() string {
	if len(r.OccurredAt) >= 16 {
		return strings.ReplaceAll(r.OccurredAt[11:16], "T", " ")
	}
	return r.OccurredAt
}

// ComputeTotals 依据逐笔记录、汇总明细与费用计算一场市集的损益。
//
// 有逐笔记录时按记录精确计算（能反映每笔不同的成交价）；
// 没有记录时退回按汇总数量估算，兼容历史数据。
func ComputeTotals(items []MarketItem, expenses []MarketExpense, records []MarketRecord) MarketTotals {
	var t MarketTotals
	if len(records) > 0 {
		for _, r := range records {
			switch r.Kind {
			case RecordSale:
				t.Revenue += r.Amount()
				t.CogsSold += r.Cost()
			case RecordTasting:
				t.TastingCost += r.Cost()
			case RecordGift, RecordLoss:
				t.LossCost += r.Cost()
			}
		}
	} else {
		for _, it := range items {
			t.Revenue += it.Revenue()
			t.CogsSold += it.Cogs()
			t.TastingCost += it.TastingCost()
			t.LossCost += it.LossCost()
		}
	}
	for _, e := range expenses {
		t.ExpenseCost += e.Amount
	}
	t.NetProfit = t.Revenue - t.CogsSold - t.TastingCost - t.LossCost - t.ExpenseCost
	return t
}

// MarketItem 市集单品行：一场市集中某个产品的试饮/销售/赠送/损耗汇总。
type MarketItem struct {
	ID          int64
	MarketID    int64
	ProductID   int64
	ProductName string
	ProductSKU  string
	Category    string

	CarriedQty  Qty // 带去的库存
	TastingQty  Qty // 试饮数量
	SoldQty     Qty // 销售数量
	GiftQty     Qty // 赠送数量
	LossQty     Qty // 损耗数量
	UnitPrice   Money
	DiscountAmt Money  // 优惠/折扣
	UnitCost    *Money // 结算时的成本快照
	Note        string
	SortOrder   int

	// 关联查询字段
	SalePrice Money
	AvgCost   Money
	StockQty  Qty
	Unit      string
}

// Revenue 该行销售额 = 销售数量 × 单价 - 优惠。
func (i MarketItem) Revenue() Money {
	return MulQty(i.SoldQty, i.UnitPrice) - i.DiscountAmt
}

// CostBasis 成本计价基础：已结算用快照，否则用当前平均成本。
func (i MarketItem) CostBasis() Money {
	if i.UnitCost != nil {
		return *i.UnitCost
	}
	return i.AvgCost
}

// HasSnapshot 是否已有成本快照。
func (i MarketItem) HasSnapshot() bool { return i.UnitCost != nil }

// Cogs 售出成本。
func (i MarketItem) Cogs() Money { return MulQty(i.SoldQty, i.CostBasis()) }

// TastingCost 试饮成本。
func (i MarketItem) TastingCost() Money { return MulQty(i.TastingQty, i.CostBasis()) }

// LossCost 赠送与损耗成本。
func (i MarketItem) LossCost() Money { return MulQty(i.GiftQty+i.LossQty, i.CostBasis()) }

// GrossProfit 该行毛利。
func (i MarketItem) GrossProfit() Money { return i.Revenue() - i.Cogs() }

// NetProfit 该行净贡献 = 销售额 - 售出成本 - 试饮成本 - 赠送损耗成本。
func (i MarketItem) NetProfit() Money {
	return i.Revenue() - i.Cogs() - i.TastingCost() - i.LossCost()
}

// OutQty 该行总出库量 = 试饮 + 销售 + 赠送 + 损耗。
func (i MarketItem) OutQty() Qty { return i.TastingQty + i.SoldQty + i.GiftQty + i.LossQty }

// RemainQty 带回未售出数量（需填写带去数量时才有意义）。
func (i MarketItem) RemainQty() Qty { return i.CarriedQty - i.OutQty() }

// Conversion 该行试饮带动倍数（销售瓶数 / 试饮瓶数）。
func (i MarketItem) Conversion() float64 {
	if i.TastingQty == 0 {
		return 0
	}
	return float64(i.SoldQty) / float64(i.TastingQty)
}

// EffectivePrice 实际成交均价。
func (i MarketItem) EffectivePrice() Money { return DivByQty(i.Revenue(), i.SoldQty) }

// Margin 该行净利率（%）。
func (i MarketItem) Margin() float64 { return Ratio(i.NetProfit(), i.Revenue()) }

// UnitOrDefault 计量单位，缺省为「瓶」。
func (i MarketItem) UnitOrDefault() string {
	if strings.TrimSpace(i.Unit) != "" {
		return i.Unit
	}
	return "瓶"
}

// Shortage 库存缺口（正数表示库存不足）。
func (i MarketItem) Shortage() Qty {
	if i.StockQty >= i.OutQty() {
		return 0
	}
	return i.OutQty() - i.StockQty
}

// 市集费用类别。
const (
	ExpenseBooth     = "booth"     // 摊位费
	ExpenseTravel    = "travel"    // 交通费
	ExpenseMeal      = "meal"      // 餐饮
	ExpensePackaging = "packaging" // 包装物料
	ExpenseLabor     = "labor"     // 人工
	ExpenseOther     = "other"     // 其他
)

// ExpenseCategoryLabels 费用类别中文名。
var ExpenseCategoryLabels = map[string]string{
	ExpenseBooth:     "摊位费",
	ExpenseTravel:    "交通费",
	ExpenseMeal:      "餐饮",
	ExpensePackaging: "包装物料",
	ExpenseLabor:     "人工",
	ExpenseOther:     "其他",
}

// ExpenseCategoryOptions 费用类别选项。
var ExpenseCategoryOptions = []Option{
	{ExpenseBooth, "摊位费"},
	{ExpenseTravel, "交通费"},
	{ExpenseMeal, "餐饮"},
	{ExpensePackaging, "包装物料"},
	{ExpenseLabor, "人工"},
	{ExpenseOther, "其他"},
}

// MarketExpense 市集活动费用。
type MarketExpense struct {
	ID       int64
	MarketID int64
	Category string
	Amount   Money
	Note     string
}

// CategoryLabel 费用类别中文名。
func (e MarketExpense) CategoryLabel() string { return ExpenseCategoryLabel(e.Category) }

// ExpenseCategoryLabel 费用类别中文名。
func ExpenseCategoryLabel(c string) string {
	if v, ok := ExpenseCategoryLabels[c]; ok {
		return v
	}
	return c
}

// ---------------------------------------------------------------- 设置与日志

// Settings 全局设置（单行表）。
type Settings struct {
	CompanyName     string
	Currency        string
	CurrencySymbol  string
	DefaultLowQty   Qty
	DefaultBoothFee Money
	AllowNegative   bool
	UpdatedAt       string

	// 自动备份策略：有数据变化时按 BackupActiveHours 备，
	// 一直没变化就拉长到 BackupIdleDays，只保留最近 BackupKeep 份。
	AutoBackup        bool
	BackupKeep        int
	BackupActiveHours int
	BackupIdleDays    int
}

// BackupActiveInterval 有数据变化时的备份间隔。
func (s Settings) BackupActiveInterval() time.Duration {
	h := s.BackupActiveHours
	if h <= 0 {
		h = 24
	}
	return time.Duration(h) * time.Hour
}

// BackupIdleInterval 没有数据变化时的备份间隔。
func (s Settings) BackupIdleInterval() time.Duration {
	d := s.BackupIdleDays
	if d <= 0 {
		d = 7
	}
	return time.Duration(d) * 24 * time.Hour
}

// BackupKeepCount 保留的备份份数。
func (s Settings) BackupKeepCount() int {
	if s.BackupKeep <= 0 {
		return 30
	}
	if s.BackupKeep > 365 {
		return 365
	}
	return s.BackupKeep
}

// ActivityLog 操作日志。
type ActivityLog struct {
	ID        int64
	UserID    *int64
	Username  string
	Action    string
	Entity    string
	EntityID  *int64
	Detail    string
	CreatedAt string
}

// ---------------------------------------------------------------- 报表结构

// MonthlyPoint 月度统计点。
type MonthlyPoint struct {
	Label      string
	Revenue    Money
	Cost       Money
	Profit     Money
	SoldQty    Qty
	TastingQty Qty
}

// ProductSalesRow 产品销售排行行。
type ProductSalesRow struct {
	ProductID   int64
	ProductName string
	ProductSKU  string
	SoldQty     Qty
	TastingQty  Qty
	Revenue     Money
	Cogs        Money
	Profit      Money
	Conversion  float64 // 试饮带动倍数 = 销售瓶数 / 试饮瓶数
}

// MarketSummaryRow 市集汇总报表行。
type MarketSummaryRow struct {
	ID           int64
	Code         string
	Name         string
	DateRange    string
	Status       string
	Days         int
	Revenue      Money
	CogsSold     Money
	TastingCost  Money
	LossCost     Money
	ExpenseTotal Money
	GrossProfit  Money
	NetProfit    Money
	NetMargin    float64
	SoldQty      Qty
	TastingQty   Qty
	Conversion   float64 // 试饮带动倍数 = 销售瓶数 / 试饮瓶数
}

// Totals 汇总行。
type MarketSummaryTotals struct {
	Count        int
	Revenue      Money
	CogsSold     Money
	TastingCost  Money
	LossCost     Money
	ExpenseTotal Money
	GrossProfit  Money
	NetProfit    Money
	NetMargin    float64
	SoldQty      Qty
	TastingQty   Qty
	Conversion   float64 // 试饮带动倍数 = 销售瓶数 / 试饮瓶数
}

// DashboardStats 首页关键指标。
type DashboardStats struct {
	StockValue    Money
	StockQty      Qty
	SKUCount      int
	LowStockCount int

	MonthRevenue Money
	MonthProfit  Money
	MonthSoldQty Qty
	MonthMarkets int

	YearRevenue Money
	YearProfit  Money
	YearMarkets int

	ActiveMarkets int
	SupplierCount int
	CustomerCount int
}

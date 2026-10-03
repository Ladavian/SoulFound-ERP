package web

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"icewine-erp/internal/model"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// ---------------------------------------------------------------- 权限

// 权限点。角色与权限的映射集中在 rolePerms，便于审计。
const (
	PermDashboardView   = "dashboard.view"
	PermProductView     = "product.view"
	PermProductManage   = "product.manage"
	PermPartnerView     = "partner.view"
	PermPartnerManage   = "partner.manage"
	PermPurchaseView    = "purchase.view"
	PermPurchaseManage  = "purchase.manage"
	PermPurchaseConfirm = "purchase.confirm"
	PermMarketView      = "market.view"
	PermMarketManage    = "market.manage"
	PermMarketSettle    = "market.settle"
	PermGroupView       = "grouporder.view"
	PermGroupManage     = "grouporder.manage"
	PermInventoryView   = "inventory.view"
	PermInventoryAdjust = "inventory.adjust"
	PermReportView      = "report.view"
	PermReportExport    = "report.export"
	PermUserManage      = "user.manage"
	PermSettingManage   = "setting.manage"
)

// rolePerms 角色 → 权限集合。"*" 表示全部权限。
var rolePerms = map[string][]string{
	model.RoleAdmin: {"*"},
	model.RoleManager: {
		PermDashboardView,
		PermProductView, PermProductManage,
		PermPartnerView, PermPartnerManage,
		PermPurchaseView, PermPurchaseManage, PermPurchaseConfirm,
		PermMarketView, PermMarketManage, PermMarketSettle,
		PermGroupView, PermGroupManage,
		PermInventoryView, PermInventoryAdjust,
		PermReportView, PermReportExport,
		PermSettingManage,
	},
	model.RoleStaff: {
		PermDashboardView,
		PermProductView,
		PermPartnerView, PermPartnerManage,
		PermPurchaseView, PermPurchaseManage,
		PermMarketView, PermMarketManage,
		PermGroupView, PermGroupManage,
		PermInventoryView,
	},
	model.RoleViewer: {
		PermDashboardView,
		PermProductView,
		PermPartnerView,
		PermPurchaseView,
		PermMarketView,
		PermGroupView,
		PermInventoryView,
	},
}

// PermissionItem 单个权限点的说明。
type PermissionItem struct {
	Key   string
	Label string
	Desc  string
}

// PermissionGroup 按模块分组的权限点，用户管理页据此渲染勾选界面。
type PermissionGroup struct {
	Label string
	Items []PermissionItem
}

// PermissionGroups 全部权限点，按功能模块分组。
//
// 分组的粒度就是日常说的"按功能分配"：想让某个账号只能记市集，
// 就只勾「市集活动」里的查看与记账，结算和其它模块一律不给。
var PermissionGroups = []PermissionGroup{
	{Label: "看板与报表", Items: []PermissionItem{
		{PermDashboardView, "查看首页看板", "库存、本月市集、待办等概览"},
		{PermReportView, "查看报表", "市集利润、产品销售、库存报表（含成本与利润）"},
		{PermReportExport, "导出报表", "导出 Excel 文件"},
	}},
	{Label: "产品档案", Items: []PermissionItem{
		{PermProductView, "查看产品", "产品列表与详情"},
		{PermProductManage, "维护产品", "新增、编辑、停用、删除产品，上传图片与条码"},
	}},
	{Label: "供应商与客户", Items: []PermissionItem{
		{PermPartnerView, "查看往来单位", "供应商与客户列表"},
		{PermPartnerManage, "维护往来单位", "新增、编辑、删除"},
	}},
	{Label: "采购入库", Items: []PermissionItem{
		{PermPurchaseView, "查看采购单", "采购列表与详情"},
		{PermPurchaseManage, "编辑采购单", "新建、修改、删除采购草稿"},
		{PermPurchaseConfirm, "确认入库", "确认后库存与成本才真正变动"},
	}},
	{Label: "市集活动", Items: []PermissionItem{
		{PermMarketView, "查看市集", "市集列表、明细与收银台"},
		{PermMarketManage, "市集记账", "收银台逐笔记账、扫码出库、改明细与费用"},
		{PermMarketSettle, "结算市集", "结算会扣减库存并锁定成本"},
	}},
	{Label: "线下团单", Items: []PermissionItem{
		{PermGroupView, "查看团单", "线下大团单的订单记录（不影响库存）"},
		{PermGroupManage, "维护团单", "新建、修改、变更状态、删除团单"},
	}},
	{Label: "库存", Items: []PermissionItem{
		{PermInventoryView, "查看库存", "库存数量与出入库流水"},
		{PermInventoryAdjust, "出入库登记", "期初建账、盘点、损耗、退货"},
	}},
	{Label: "系统管理", Items: []PermissionItem{
		{PermUserManage, "用户与权限", "新增用户、改账号名、分配权限"},
		{PermSettingManage, "系统设置", "公司名、币种、自动备份、重算库存"},
	}},
}

// AllPermissions 全部权限点（服务层据此校验合法性）。
func AllPermissions() []string {
	out := make([]string, 0, 24)
	for _, g := range PermissionGroups {
		for _, it := range g.Items {
			out = append(out, it.Key)
		}
	}
	return out
}

// PermissionLabel 权限中文名。
func PermissionLabel(key string) string {
	if key == "*" {
		return "全部权限"
	}
	if key == model.PermissionNone {
		return "无权限"
	}
	for _, g := range PermissionGroups {
		for _, it := range g.Items {
			if it.Key == key {
				return it.Label
			}
		}
	}
	return key
}

// RoleDefaults 角色默认权限（新建用户时预勾选，之后可逐项增删）。
func RoleDefaults(role string) []string {
	list := rolePerms[role]
	if len(list) == 1 && list[0] == "*" {
		return AllPermissions()
	}
	out := make([]string, len(list))
	copy(out, list)
	return out
}

// RoleDefaultsByRole 全部角色的默认权限，供前端按角色预勾选。
func RoleDefaultsByRole() map[string][]string {
	out := map[string][]string{}
	for _, r := range []string{model.RoleAdmin, model.RoleManager, model.RoleStaff, model.RoleViewer} {
		out[r] = RoleDefaults(r)
	}
	return out
}

// permSet 供模板调用：{{if .Perms.Has "market.settle"}}。
//
// 为什么不用"把函数放进数据 map"的写法？因为 html/template 不允许对
// map 中的函数值传参（会报 "Can is not a method but has arguments"），
// 而调用值类型上的方法传参是完全合法的。
type permSet struct {
	role  string
	perms []string // 解析后的权限点
}

// newPermSet 解析权限：单独指定过就用自己的，否则用角色默认。
func newPermSet(role string, custom []string) permSet {
	if len(custom) > 0 {
		return permSet{role: role, perms: custom}
	}
	return permSet{role: role, perms: rolePerms[role]}
}

// Has 判断当前用户是否具备某权限。
func (p permSet) Has(perm string) bool {
	for _, x := range p.perms {
		if x == "*" || x == perm {
			return true
		}
	}
	return false
}

// Role 当前角色值。
func (p permSet) Role() string { return p.role }

// List 解析后的权限点列表（供前端预勾选）。
func (p permSet) List() []string {
	out := make([]string, 0, len(p.perms))
	for _, x := range p.perms {
		if x == "*" {
			continue
		}
		out = append(out, x)
	}
	return out
}

// IsCustom 是否单独指定过权限。
func (p permSet) IsCustom() bool { return len(p.perms) > 0 }

// Count 已授权的权限点数量。
func (p permSet) Count() int {
	n := 0
	for _, key := range AllPermissions() {
		if p.Has(key) {
			n++
		}
	}
	return n
}

// permsForUser 解析某个用户的权限集合。
func permsForUser(u *model.User) permSet {
	if u == nil {
		return permSet{}
	}
	return newPermSet(u.Role, u.Permissions)
}

// permLabel 权限的中文说明（用户管理页展示）。
func roleDescription(role string) string {
	switch role {
	case model.RoleAdmin:
		return "全部权限：业务操作 + 用户管理 + 系统设置"
	case model.RoleManager:
		return "全部业务权限：进销存、市集结算、成本与利润报表、系统设置；不能管理用户"
	case model.RoleStaff:
		return "录单权限：采购与市集数据录入、产品与客户维护、查看库存；看不到成本与利润，不能结算"
	default:
		return "只读：可查看列表与库存，不能做任何修改，看不到成本与利润"
	}
}

// ---------------------------------------------------------------- 请求上下文

func userFrom(r *http.Request) *model.User {
	if r == nil {
		return nil
	}
	if v, ok := r.Context().Value(ctxUserKey).(*model.User); ok {
		return v
	}
	return nil
}

func settingsFrom(r *http.Request) *model.Settings {
	if v, ok := r.Context().Value(ctxSettingsKey).(*model.Settings); ok {
		return v
	}
	return &model.Settings{CurrencySymbol: "¥"}
}

func flashFrom(r *http.Request) []Flash {
	if v, ok := r.Context().Value(ctxFlashKey).([]Flash); ok {
		return v
	}
	return nil
}

// Flash 一次性提示消息。
type Flash struct {
	Level string // success / error / info
	Text  string
}

// canEdit 便捷判断：当前用户是否具备某权限。
func canEdit(r *http.Request, perm string) bool {
	return permsForUser(userFrom(r)).Has(perm)
}

// isHTMX 判断是否 HTMX 发起的请求。
func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

// trimAll 批量去空格。
func trimAll(values ...string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.TrimSpace(v)
	}
	return out
}

// logAction 记一条操作日志。
//
// 产品、供应商、客户这类直接走 store 的写操作没有包事务，
// 统一用这个方法补日志；失败只打日志，不影响用户操作。
func (s *Server) logAction(r *http.Request, action, entity string, entityID *int64, detail string) {
	if err := s.svc.Store.LogNow(r.Context(), userFrom(r), action, entity, entityID, detail); err != nil {
		log.Printf("写操作日志失败（%s %s）: %v", action, entity, err)
	}
}

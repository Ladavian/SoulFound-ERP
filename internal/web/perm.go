package web

import (
	"encoding/json"
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
		PermInventoryView,
	},
	model.RoleViewer: {
		PermDashboardView,
		PermProductView,
		PermPartnerView,
		PermPurchaseView,
		PermMarketView,
		PermInventoryView,
	},
}

// permSet 供模板调用：{{if .Perms.Has "market.settle"}}。
//
// 为什么不用"把函数放进数据 map"的写法？因为 html/template 不允许对
// map 中的函数值传参（会报 "Can is not a method but has arguments"），
// 而调用值类型上的方法传参是完全合法的。
type permSet struct{ role string }

// Has 判断当前用户是否具备某权限。
func (p permSet) Has(perm string) bool { return hasPerm(p.role, perm) }

// Role 当前角色值。
func (p permSet) Role() string { return p.role }

// hasPerm 判断角色是否具备权限。
func hasPerm(role, perm string) bool {
	if role == "" {
		return false
	}
	for _, p := range rolePerms[role] {
		if p == "*" || p == perm {
			return true
		}
	}
	return false
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
	u := userFrom(r)
	if u == nil {
		return false
	}
	return hasPerm(u.Role, perm)
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

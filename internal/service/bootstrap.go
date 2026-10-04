package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"icewine-erp/internal/model"
)

// Bootstrap 首次启动初始化：全局设置、默认管理员、可选演示数据。
func (s *Service) Bootstrap(ctx context.Context) error {
	if err := s.Store.EnsureSettings(ctx, model.Settings{
		CompanyName:    s.Cfg.AppName,
		Currency:       s.Cfg.Currency,
		CurrencySymbol: s.Cfg.CurrencySymbol,
		DefaultLowQty:  s.Cfg.DefaultLowQty,
		// 默认开启自动备份：有数据变化每天一份，一直没变化每周兜底一份
		AutoBackup:        true,
		BackupKeep:        30,
		BackupActiveHours: 24,
		BackupIdleDays:    7,
	}); err != nil {
		return fmt.Errorf("初始化设置失败: %w", err)
	}

	count, err := s.Store.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("检查用户失败: %w", err)
	}
	if count == 0 {
		hash, err := HashPassword(s.Cfg.AdminPassword)
		if err != nil {
			return err
		}
		if _, err := s.Store.CreateUser(ctx, &model.User{
			Username:     s.Cfg.AdminUsername,
			PasswordHash: hash,
			FullName:     s.Cfg.AdminName,
			Role:         model.RoleAdmin,
			IsActive:     true,
		}); err != nil {
			return fmt.Errorf("创建管理员失败: %w", err)
		}
		log.Printf("已创建管理员账号：%s / %s（请登录后立即修改密码）",
			s.Cfg.AdminUsername, s.Cfg.AdminPassword)
	}

	if s.Cfg.SeedDemo {
		existing, _, err := s.Store.CountProducts(ctx)
		if err != nil {
			return err
		}
		if existing == 0 {
			admin, err := s.Store.UserByUsername(ctx, s.Cfg.AdminUsername)
			if err != nil {
				return err
			}
			if err := s.seedDemo(ctx, admin); err != nil {
				return fmt.Errorf("写入演示数据失败: %w", err)
			}
			log.Printf("已写入演示数据（产品、采购入库、市集活动）")
		}
	}
	return nil
}

// ---------------------------------------------------------------- 用户管理

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

func validRole(role string) bool {
	switch role {
	case model.RoleAdmin, model.RoleManager, model.RoleStaff, model.RoleViewer:
		return true
	}
	return false
}

// 权限点清单由 web 层维护在 perm.go，这里只需要校验合法性，
// 因此通过注册进来的方式读取，避免 service 依赖 web。
var (
	knownPermissions  = map[string]bool{}
	PermUserManageKey = "user.manage"
)

// RegisterPermissions 由 web 层在启动时注册全部权限点，用于校验。
func RegisterPermissions(list []string) {
	for _, p := range list {
		knownPermissions[p] = true
	}
}

// NormalizePermissions 去重并校验权限点。
func NormalizePermissions(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p == model.PermissionNone {
			// 明确表示"不给任何权限"，其余勾选一律忽略
			return []string{model.PermissionNone}, nil
		}
		if len(knownPermissions) > 0 && !knownPermissions[p] && p != "*" {
			return nil, UserErrf("未知的权限项：%s", p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func containsPerm(list []string, perm string) bool {
	for _, p := range list {
		if p == perm || p == "*" {
			return true
		}
	}
	return false
}

// CreateUser 新建用户。
//
// permissions 非空时表示单独指定权限点；为空则沿用角色默认权限。
func (s *Service) CreateUser(ctx context.Context, username, password, fullName, role string, permissions []string, actor *model.User) (int64, error) {
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return 0, UserErrf("用户名只能是 3-32 位字母、数字、点、下划线或中划线")
	}
	if err := ValidatePassword(password); err != nil {
		return 0, err
	}
	if !validRole(role) {
		return 0, UserErrf("请选择有效的角色")
	}
	permissions, err := NormalizePermissions(permissions)
	if err != nil {
		return 0, err
	}
	if existing, err := s.Store.UserByUsername(ctx, username); err != nil {
		return 0, err
	} else if existing != nil {
		return 0, UserErrf("用户名「%s」已存在", username)
	}
	hash, err := HashPassword(password)
	if err != nil {
		return 0, err
	}
	var newID int64
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		id, err := s.Store.CreateUserTx(ctx, tx, &model.User{
			Username:     username,
			PasswordHash: hash,
			FullName:     strings.TrimSpace(fullName),
			Role:         role,
			IsActive:     true,
			Permissions:  permissions,
		})
		if err != nil {
			return err
		}
		newID = id
		return s.Store.Log(ctx, tx, actor, "新建用户", "user", &id, username+" / "+model.RoleLabel(role))
	})
	return newID, err
}

// UpdateUser 修改用户资料、角色与启用状态。
func (s *Service) UpdateUser(ctx context.Context, id int64, username, fullName, role string, isActive bool, permissions []string, actor *model.User) error {
	if !validRole(role) {
		return UserErrf("请选择有效的角色")
	}
	username = strings.TrimSpace(username)
	if !usernamePattern.MatchString(username) {
		return UserErrf("用户名只能是 3-32 位字母、数字、点、下划线或中划线")
	}
	permissions, err := NormalizePermissions(permissions)
	if err != nil {
		return err
	}
	target, err := s.Store.UserByID(ctx, id)
	if err != nil {
		return err
	}
	if target == nil {
		return UserErrf("用户不存在")
	}

	// 改账号名要保证唯一
	if username != target.Username {
		existing, err := s.Store.UserByUsername(ctx, username)
		if err != nil {
			return err
		}
		if existing != nil && existing.ID != id {
			return UserErrf("用户名「%s」已被占用", username)
		}
	}

	if actor != nil && actor.ID == id {
		if !isActive {
			return UserErrf("不能停用当前登录的账号")
		}
		if target.Role == model.RoleAdmin && role != model.RoleAdmin {
			return UserErrf("不能修改自己的管理员角色")
		}
		// 自锁保护：把自己改到无法再进用户管理，就再也改不回来了
		if len(permissions) > 0 && !containsPerm(permissions, PermUserManageKey) {
			return UserErrf("不能移除自己的「用户管理」权限，否则将无法再调整权限")
		}
	}
	// 防止把最后一个管理员降级或停用
	if target.Role == model.RoleAdmin && (role != model.RoleAdmin || !isActive) {
		admins, err := s.Store.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if admins <= 1 {
			return UserErrf("系统必须保留至少一个启用状态的管理员")
		}
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.UpdateUserTx(ctx, tx, &model.User{
			ID:          id,
			Username:    username,
			FullName:    strings.TrimSpace(fullName),
			Role:        role,
			IsActive:    isActive,
			Permissions: permissions,
		}); err != nil {
			return err
		}
		detail := target.Username
		if username != target.Username {
			detail = target.Username + " → " + username
		}
		return s.Store.Log(ctx, tx, actor, "修改用户", "user", &id, detail)
	})
}

// ResetPassword 管理员重置他人密码。
func (s *Service) ResetPassword(ctx context.Context, id int64, newPassword string, actor *model.User) error {
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	target, err := s.Store.UserByID(ctx, id)
	if err != nil {
		return err
	}
	if target == nil {
		return UserErrf("用户不存在")
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.UpdatePasswordTx(ctx, tx, id, hash); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, actor, "重置密码", "user", &id, target.Username)
	})
}

// DeleteUser 删除用户。
func (s *Service) DeleteUser(ctx context.Context, id int64, actor *model.User) error {
	if actor != nil && actor.ID == id {
		return UserErrf("不能删除当前登录的账号")
	}
	target, err := s.Store.UserByID(ctx, id)
	if err != nil {
		return err
	}
	if target == nil {
		return UserErrf("用户不存在")
	}
	if target.Role == model.RoleAdmin {
		admins, err := s.Store.CountActiveAdmins(ctx)
		if err != nil {
			return err
		}
		if admins <= 1 {
			return UserErrf("系统必须保留至少一个管理员")
		}
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.DeleteUserTx(ctx, tx, id); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, actor, "删除用户", "user", &id, target.Username)
	})
}

// ---------------------------------------------------------------- 演示数据

func (s *Service) seedDemo(ctx context.Context, admin *model.User) error {
	supplier1, err := s.Store.CreateSupplier(ctx, &model.Supplier{
		Name: "Peller Estates 皮勒酒庄", ContactName: "Linda", Phone: "+1 905-468-4673",
		Email: "sales@pellerestates.example", Country: "加拿大",
		Address: "290 John St E, Niagara-on-the-Lake, ON", IsActive: true,
	})
	if err != nil {
		return err
	}
	if _, err := s.Store.CreateSupplier(ctx, &model.Supplier{
		Name: "Inniskillin 云岭酒庄", ContactName: "Mark", Phone: "+1 905-468-2187",
		Email: "orders@inniskillin.example", Country: "加拿大",
		Address: "1499 Line 3, Niagara-on-the-Lake, ON", IsActive: true,
	}); err != nil {
		return err
	}

	type productSeed struct {
		sku, name, nameEn string
		vintage, volume   int
		abv               string
		price, cost       string
		lowStock          string
		brand, origin     string
		specs             string
	}
	seeds := []productSeed{
		{"ICE-VID-375", "云岭酒庄威代尔冰酒375ml", "Vidal Icewine", 2019, 375, "11.5", "398", "168", "6",
			"SoulFound", "加拿大 尼亚加拉",
			"葡萄品种: 维代尔\n甜度: 很甜\n等级: VQA\n适饮温度: 6-8℃\n包装: 单支装"},
		{"ICE-RIE-375", "云岭酒庄雷司令冰酒375ml", "Riesling Icewine", 2020, 375, "10.5", "458", "198", "6",
			"SoulFound", "加拿大 尼亚加拉",
			"葡萄品种: 雷司令\n甜度: 很甜\n等级: VQA\n适饮温度: 6-8℃\n包装: 单支装"},
		{"ICE-CAB-200", "皮勒酒庄品丽珠冰酒200ml", "Cabernet Franc Icewine", 2018, 200, "11.0", "328", "128", "6",
			"SoulFound", "加拿大 尼亚加拉",
			"葡萄品种: 品丽珠\n甜度: 甜\n等级: VQA\n包装: 单支装"},
		{"ICE-VID-200", "云岭酒庄威代尔冰酒200ml", "Vidal Icewine Half", 2021, 200, "11.5", "268", "108", "12",
			"SoulFound", "加拿大 尼亚加拉",
			"葡萄品种: 维代尔\n甜度: 很甜\n等级: VQA\n包装: 半瓶装"},
	}
	productIDs := make([]int64, 0, len(seeds))
	for _, sd := range seeds {
		id, err := s.Store.CreateProduct(ctx, &model.Product{
			SKU: sd.sku, Name: sd.name, NameEn: sd.nameEn, Category: "冰酒",
			Brand: sd.brand, Origin: sd.origin,
			Vintage: sd.vintage, VolumeML: sd.volume, ABV: parseABVSeed(sd.abv),
			Unit: "瓶", BottlesPerCase: 6,
			SalePrice: model.MustMoney(sd.price), CostPrice: model.MustMoney(sd.cost),
			Specs: sd.specs, LowStockQty: model.MustQty(sd.lowStock),
			SupplierID: &supplier1, IsActive: true,
			Notes: "尼亚加拉半岛 VQA 认证",
		})
		if err != nil {
			return err
		}
		productIDs = append(productIDs, id)
	}

	// 一批采购入库（含运费与关税，验证到岸成本分摊）
	purchaseDate := fmtDate(todayDate().AddDate(0, 0, -60))
	purchaseID, err := s.SavePurchase(ctx, PurchaseInput{
		SupplierID:   &supplier1,
		PurchaseDate: purchaseDate,
		AllocMethod:  model.AllocByQty,
		ShippingCost: model.MustMoney("1800"),
		TariffCost:   model.MustMoney("1200"),
		Notes:        "2025 秋季首批到货，海运 + 清关",
		Items: []PurchaseItemInput{
			{ProductID: productIDs[0], Qty: model.MustQty("72"), UnitPrice: model.MustMoney("168")},
			{ProductID: productIDs[1], Qty: model.MustQty("48"), UnitPrice: model.MustMoney("198")},
			{ProductID: productIDs[2], Qty: model.MustQty("36"), UnitPrice: model.MustMoney("128")},
			{ProductID: productIDs[3], Qty: model.MustQty("84"), UnitPrice: model.MustMoney("108")},
		},
	}, admin)
	if err != nil {
		return err
	}
	if err := s.ConfirmPurchase(ctx, purchaseID, admin); err != nil {
		return err
	}

	type demoLine struct {
		product int
		carried string
		tasting string
		sold    string
		gift    string
		loss    string
		price   string
		disc    string
	}
	type demoExpense struct {
		category string
		amount   string
		note     string
	}
	type marketSeed struct {
		name, venue, city, organizer string
		daysAgo                      int
		days                         int
		lines                        []demoLine
		expenses                     []demoExpense
		settle                       bool
	}

	markets := []marketSeed{
		{
			name: "圣劳伦斯市场周末市集", venue: "St. Lawrence Market", city: "多伦多",
			organizer: "Toronto Food Events", daysAgo: 45, days: 2,
			lines: []demoLine{
				{0, "24", "4", "12", "1", "0", "398", "0"},
				{1, "16", "3", "8", "0", "0", "458", "80"},
				{3, "32", "5", "16", "1", "1", "268", "0"},
			},
			expenses: []demoExpense{
				{model.ExpenseBooth, "800", "两天摊位费"},
				{model.ExpenseTravel, "260", "油费与停车"},
				{model.ExpensePackaging, "170", "纸袋与冰袋"},
			},
			settle: true,
		},
		{
			name: "尼亚加拉冰酒节", venue: "Niagara Icewine Village", city: "尼亚加拉湖滨小镇",
			organizer: "Niagara Wine Festival", daysAgo: 25, days: 3,
			lines: []demoLine{
				{0, "32", "5", "16", "1", "0", "398", "200"},
				{1, "24", "4", "12", "0", "0", "458", "0"},
				{2, "18", "3", "9", "0", "0", "328", "0"},
				{3, "40", "6", "20", "1", "0", "268", "120"},
			},
			expenses: []demoExpense{
				{model.ExpenseBooth, "1800", "三天展位"},
				{model.ExpenseTravel, "720", "住宿与交通"},
				{model.ExpenseMeal, "480", "团队餐费"},
				{model.ExpensePackaging, "270", "礼盒与包装"},
			},
			settle: true,
		},
		{
			name: "万锦亚洲美食节", venue: "Markham Fairgrounds", city: "万锦",
			organizer: "Markham Asian Food Fest", daysAgo: 8, days: 1,
			lines: []demoLine{
				{0, "16", "3", "9", "0", "0", "398", "0"},
				{3, "24", "4", "13", "1", "0", "268", "70"},
			},
			expenses: []demoExpense{
				{model.ExpenseBooth, "600", "单日摊位"},
				{model.ExpenseTravel, "160", "交通"},
			},
			settle: true,
		},
		{
			name: "圣诞市集（筹备中）", venue: "Distillery District", city: "多伦多",
			organizer: "Toronto Christmas Market", daysAgo: -12, days: 4,
			lines: []demoLine{
				{0, "0", "0", "0", "0", "0", "398", "0"},
				{2, "0", "0", "0", "0", "0", "328", "0"},
			},
			expenses: []demoExpense{
				{model.ExpenseBooth, "2400", "四天展位预付"},
			},
			settle: false,
		},
	}

	for _, ms := range markets {
		start := todayDate().AddDate(0, 0, -ms.daysAgo)
		end := start.AddDate(0, 0, ms.days-1)
		marketID, err := s.SaveMarket(ctx, MarketInput{
			Name: ms.name, Venue: ms.venue, City: ms.city, Organizer: ms.organizer,
			StartDate: fmtDate(start), EndDate: fmtDate(end),
		}, admin)
		if err != nil {
			return err
		}
		for _, ln := range ms.lines {
			if _, err := s.AddMarketProduct(ctx, marketID, productIDs[ln.product], admin); err != nil {
				return err
			}
		}
		m, err := s.Store.MarketByID(ctx, marketID)
		if err != nil {
			return err
		}
		for i, ln := range ms.lines {
			if i >= len(m.Items) {
				break
			}
			item := m.Items[i]
			// 数量改成"逐笔记录"之后，明细行只负责带去数量与售价，
			// 试饮/销售/赠送/损耗都通过现场记录生成（下方 AddMarketRecord）。
			up := MarketItemUpdate{
				CarriedQty: model.MustQty(ln.carried),
				UnitPrice:  model.MustMoney(ln.price),
			}
			if err := s.UpdateMarketItemRow(ctx, marketID, item.ID, up, admin); err != nil {
				return err
			}
			demoRecords := []struct {
				kind  string
				qty   string
				price string
				disc  string
				note  string
			}{
				{model.RecordSale, ln.sold, ln.price, ln.disc, "演示：现场销售"},
				{model.RecordTasting, ln.tasting, "0", "0", "演示：试饮"},
				{model.RecordGift, ln.gift, "0", "0", "演示：赠送"},
				{model.RecordLoss, ln.loss, "0", "0", "演示：损耗"},
			}
			for _, dr := range demoRecords {
				if model.MustQty(dr.qty) == 0 {
					continue
				}
				if _, err := s.AddMarketRecord(ctx, marketID, MarketRecordInput{
					ProductID: item.ProductID,
					Kind:      dr.kind,
					Qty:       model.MustQty(dr.qty),
					UnitPrice: model.MustMoney(dr.price),
					Discount:  model.MustMoney(dr.disc),
					Channel:   "manual",
					Note:      dr.note,
				}, admin); err != nil {
					return err
				}
			}
		}
		expenses := make([]model.MarketExpense, 0, len(ms.expenses))
		for _, e := range ms.expenses {
			expenses = append(expenses, model.MarketExpense{
				Category: e.category, Amount: model.MustMoney(e.amount), Note: e.note,
			})
		}
		if err := s.SaveMarketExpenses(ctx, marketID, expenses, admin); err != nil {
			return err
		}
		if ms.settle {
			if err := s.SettleMarket(ctx, marketID, admin); err != nil {
				return err
			}
		} else {
			if err := s.SetMarketStatus(ctx, marketID, model.MarketOngoing, admin); err != nil {
				return err
			}
		}
	}

	// 一笔期初/盘点示例
	if err := s.AdjustStock(ctx, AdjustInput{
		ProductID:  productIDs[2],
		Qty:        model.MustQty("6"),
		UnitCost:   model.MustMoney("135"),
		OccurredOn: fmtDate(todayDate().AddDate(0, 0, -50)),
		Reason:     model.ReasonOpening,
		Note:       "开业前自留样品转库存",
	}, admin); err != nil {
		return err
	}
	return nil
}

// parseABVSeed 演示数据里的酒精度（"11.5" → 1150）。
func parseABVSeed(raw string) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0
	}
	return int(f*100 + 0.5)
}

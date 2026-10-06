package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"icewine-erp/internal/model"
)

// EcOrderRow 平台导出表格里的一行（子订单）。
//
// 字段名与平台导出列一一对应，具体列名在 web 层做宽松匹配，
// 这里只负责"一行订单明细"本身。
type EcOrderRow struct {
	SubOrderNo   string // 子订单编号
	OrderNo      string // 主订单编号
	Title        string // 商品标题
	EcProductID  string // 商品ID（平台商品ID，绑定 ERP 产品靠它）
	EcSKU        string // 商品属性（规格）
	MerchantCode string // 商家编码
	Qty          string // 购买数量
	UnitPrice    string // 商品价格
	PayableAmt   string // 买家应付货款
	PaidAmt      string // 买家实付金额
	RefundStatus string // 退款状态
	RefundAmt    string // 退款金额
	ItemStatus   string // 订单状态
	CreatedAt    string // 订单创建时间
	PaidAt       string // 订单付款时间
	ShippedAt    string // 发货时间
	LogisticsNo  string // 物流单号
	LogisticsCo  string // 物流公司
	SellerNote   string // 商家备注
	BuyerNote    string // 主订单买家留言
}

// ImportEcOrders 导入平台订单。
//
// 行为说明：
//   - 按「主订单编号」和「子订单编号」去重，同一份文件重复导入不会产生重复数据；
//   - 已经手工绑定过产品的明细不会被自动匹配覆盖（尊重人工判断）；
//   - 未匹配上的商品会汇总回来，让用户在界面上绑一次，
//     绑定会把该电商商品ID下所有历史未匹配的行一起对上。
func (s *Service) ImportEcOrders(ctx context.Context, platform string, rows []EcOrderRow, user *model.User) (model.EcImportSummary, error) {
	summary := model.EcImportSummary{Platform: platform}
	if platform == "" {
		platform = model.EcTaobao
	}

	// 按主订单分组：一张订单一行主表记录，多条明细挂上去
	type orderGroup struct {
		orderNo string
		rows    []EcOrderRow
	}
	var (
		groups []*orderGroup
		index  = map[string]*orderGroup{}
	)
	for i, r := range rows {
		line := i + 2 // 表格里的实际行号（含表头）
		orderNo := strings.TrimSpace(r.OrderNo)
		subNo := strings.TrimSpace(r.SubOrderNo)
		if orderNo == "" && subNo == "" {
			summary.Skipped++
			continue
		}
		if orderNo == "" {
			orderNo = subNo // 少数导出只有子订单号
		}
		summary.Items++
		g, ok := index[orderNo]
		if !ok {
			g = &orderGroup{orderNo: orderNo}
			index[orderNo] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, r)
		_ = line
	}

	// 先做商品匹配，必须在事务外：
	// 连接池上限是 1，事务占着唯一连接，事务里再走连接池就会报
	// "transaction has already been committed or rolled back"（或直接卡死）。
	// 同一个商品ID / 编码往往重复出现，这里顺便缓存，避免逐行查库。
	type matchKey struct{ kind, val string }
	matchCache := map[matchKey]*int64{}
	resolve := func(ecID, merchantCode string) *int64 {
		if ecID != "" {
			k := matchKey{"ec", ecID}
			if v, ok := matchCache[k]; ok {
				return v
			}
			var id *int64
			if p, err := s.Store.ProductByEcLink(ctx, platform, ecID); err == nil && p != nil {
				v := p.ID
				id = &v
			}
			matchCache[k] = id
			if id != nil {
				return id
			}
		}
		if merchantCode != "" {
			k := matchKey{"sku", merchantCode}
			if v, ok := matchCache[k]; ok {
				return v
			}
			var id *int64
			if p, err := s.Store.ProductBySKU(ctx, merchantCode); err == nil && p != nil {
				v := p.ID
				id = &v
			}
			matchCache[k] = id
			return id
		}
		return nil
	}
	for _, g := range groups {
		for _, r := range g.rows {
			resolve(strings.TrimSpace(r.EcProductID), strings.TrimSpace(r.MerchantCode))
		}
	}

	err := s.Store.Tx(ctx, func(tx *sql.Tx) error {
		for _, g := range groups {
			order := &model.EcOrder{
				Platform:  platform,
				OrderNo:   g.orderNo,
				CreatedAt: normalizeEcTime(g.rows[0].CreatedAt),
				PaidAt:    normalizeEcTime(g.rows[0].PaidAt),
				ShippedAt: normalizeEcTime(g.rows[0].ShippedAt),
				BuyerNote: strings.TrimSpace(g.rows[0].BuyerNote),
			}
			// 订单状态取第一条；退款状态按整单是否全退判定
			order.Status = strings.TrimSpace(g.rows[0].ItemStatus)
			refunded := 0
			closed := 0
			var notes []string
			for _, r := range g.rows {
				if strings.Contains(r.RefundStatus, "退款成功") {
					refunded++
				}
				if strings.Contains(r.ItemStatus, "关闭") {
					closed++
				}
				if n := strings.TrimSpace(r.SellerNote); n != "" && !contains(notes, n) {
					notes = append(notes, n)
				}
			}
			switch {
			case refunded == len(g.rows):
				order.RefundStatus = "退款成功"
			case closed == len(g.rows):
				order.RefundStatus = "全部关闭"
			default:
				order.RefundStatus = strings.TrimSpace(g.rows[0].RefundStatus)
			}
			order.SellerNote = strings.Join(notes, "；")

			orderID, created, err := s.Store.UpsertEcOrder(ctx, tx, order)
			if err != nil {
				summary.Errors = appendErr(summary.Errors, "%s：写入订单失败 %v", g.orderNo, err)
				continue
			}
			if created {
				summary.Orders++
			} else {
				summary.OrdersUpd++
			}

			for _, r := range g.rows {
				item := &model.EcOrderItem{
					OrderID:      orderID,
					Platform:     platform,
					SubOrderNo:   strings.TrimSpace(r.SubOrderNo),
					Title:        strings.TrimSpace(r.Title),
					EcProductID:  strings.TrimSpace(r.EcProductID),
					EcSKU:        strings.TrimSpace(r.EcSKU),
					MerchantCode: strings.TrimSpace(r.MerchantCode),
					RefundStatus: strings.TrimSpace(r.RefundStatus),
					ItemStatus:   strings.TrimSpace(r.ItemStatus),
					LogisticsNo:  strings.TrimSpace(r.LogisticsNo),
					LogisticsCo:  strings.TrimSpace(r.LogisticsCo),
				}
				if v, err := model.ParseQty(r.Qty); err == nil {
					item.Qty = v
				} else {
					summary.Errors = appendErr(summary.Errors, "%s：数量无法识别（%s）", item.SubOrderNo, r.Qty)
				}
				item.UnitPrice = parseMoneyLoose(r.UnitPrice)
				item.PayableAmount = parseMoneyLoose(r.PayableAmt)
				item.PaidAmount = parseMoneyLoose(r.PaidAmt)
				item.RefundAmount = parseMoneyLoose(r.RefundAmt)

				// 用预解析好的匹配结果（电商商品ID 优先，其次商家编码当 SKU）
				item.ProductID = resolve(item.EcProductID, item.MerchantCode)

				if _, _, err := s.Store.UpsertEcItem(ctx, tx, item); err != nil {
					summary.Errors = appendErr(summary.Errors, "%s：写入明细失败 %v", item.SubOrderNo, err)
					continue
				}
				if item.ProductID != nil {
					summary.Matched++
				} else {
					summary.Unmatched++
				}
			}
		}
		return nil
	})
	if err != nil {
		return summary, err
	}

	// 未匹配的电商商品汇总（读库，保证跨文件累计）
	if list, err := s.Store.EcUnmatched(ctx, platform, 100); err == nil {
		summary.UnmatchedEc = list
		summary.Unmatched = 0
		for _, u := range list {
			summary.Unmatched += u.Orders
		}
	}
	if err := s.Store.LogNow(ctx, user, "导入电商订单", "ec_order", nil,
		fmt.Sprintf("%s：%d 单 / %d 行，未匹配商品 %d 种",
			model.EcPlatformLabel(platform), summary.Orders+summary.OrdersUpd,
			summary.Items, len(summary.UnmatchedEc))); err != nil {
		summary.Errors = appendErr(summary.Errors, "写日志失败：%v", err)
	}
	return summary, nil
}

// BindEcLink 把一个平台商品ID 绑到 ERP 产品上。
//
// 一个产品可以绑多个平台商品ID（淘宝多个链接、以及抖音京东等），
// 所以这里是"新增一条绑定关系"，不是覆盖字段。
// 绑定后会把该商品ID 下所有历史未匹配的订单明细一起补齐。
func (s *Service) BindEcLink(ctx context.Context, platform, ecProductID, ecSKUId, title string, productID int64, user *model.User) (int, error) {
	platform = strings.TrimSpace(platform)
	ecProductID = strings.TrimSpace(ecProductID)
	if platform == "" {
		platform = model.EcTaobao
	}
	if ecProductID == "" {
		return 0, UserErrf("缺少平台商品ID")
	}
	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return 0, err
	}
	if product == nil {
		return 0, UserErrf("产品不存在")
	}
	// 同一个平台商品ID 只能属于一个产品
	if existing, err := s.Store.EcLinkByEcID(ctx, platform, ecProductID); err != nil {
		return 0, err
	} else if existing != nil && existing.ProductID != productID {
		return 0, UserErrf("%s的商品ID「%s」已经绑给「%s」了，请先解绑",
			model.EcPlatformLabel(platform), ecProductID, existing.ProductName)
	}

	var fixed int
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		conflict, err := s.Store.AddEcLink(ctx, tx, model.ProductEcLink{
			ProductID: productID, Platform: platform,
			EcProductID: ecProductID, EcSKUId: ecSKUId, Title: title,
		})
		if err != nil {
			return err
		}
		if conflict != nil {
			return UserErrf("%s的商品ID「%s」已经绑给「%s」了",
				model.EcPlatformLabel(platform), ecProductID, conflict.ProductName)
		}
		n, err := s.Store.BindEcOrderItems(ctx, tx, platform, ecProductID, productID)
		if err != nil {
			return err
		}
		fixed = n
		return s.Store.Log(ctx, tx, user, "绑定平台商品", "product", &productID,
			fmt.Sprintf("%s %s → %s（补齐 %d 行订单明细）",
				model.EcPlatformLabel(platform), ecProductID, product.Name, n))
	})
	if err != nil {
		return 0, err
	}
	return fixed, nil
}

// UnbindEcLink 解除一条平台商品绑定。
func (s *Service) UnbindEcLink(ctx context.Context, linkID int64, user *model.User) error {
	link, err := s.Store.EcLinkByID(ctx, linkID)
	if err != nil {
		return err
	}
	if link == nil {
		return UserErrf("绑定不存在")
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.DeleteEcLink(ctx, tx, linkID); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "解绑平台商品", "product", &link.ProductID,
			fmt.Sprintf("%s %s（原属 %s）", link.PlatformLabel(), link.EcProductID, link.ProductName))
	})
}

// BindEcItem 单独把一条订单明细绑到产品（临时性绑定，不写回产品档案）。
func (s *Service) BindEcItem(ctx context.Context, itemID, productID int64, user *model.User) error {
	if productID <= 0 {
		return UserErrf("请选择产品")
	}
	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return err
	}
	if product == nil {
		return UserErrf("产品不存在")
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.BindEcItem(ctx, tx, itemID, productID); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "绑定订单明细", "ec_order", &itemID, product.Name)
	})
}

// ---------------------------------------------------------------- 工具

// parseMoneyLoose 平台金额：允许带 ¥、千分位、空值。
func parseMoneyLoose(raw string) model.Money {
	v := strings.TrimSpace(raw)
	v = strings.ReplaceAll(v, ",", "")
	v = strings.ReplaceAll(v, "￥", "")
	v = strings.ReplaceAll(v, "¥", "")
	if v == "" || v == "-" || v == "无退款申请" {
		return 0
	}
	m, err := model.ParseMoney(v)
	if err != nil {
		return 0
	}
	return m
}

// normalizeEcTime 平台时间格式不统一（秒可能只写一位），统一成标准格式，
// 这样 SQLite 的 date() 才能正常取日期做筛选。
func normalizeEcTime(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	datePart, timePart, _ := strings.Cut(v, " ")
	datePart = strings.ReplaceAll(datePart, "/", "-")
	parts := strings.Split(datePart, "-")
	if len(parts) != 3 {
		return v
	}
	y, m, d := parts[0], parts[1], parts[2]
	if len(m) == 1 {
		m = "0" + m
	}
	if len(d) == 1 {
		d = "0" + d
	}
	out := y + "-" + m + "-" + d
	if timePart == "" {
		return out
	}
	tp := strings.Split(strings.TrimSpace(timePart), ":")
	for len(tp) < 3 {
		tp = append(tp, "00")
	}
	for i := range tp {
		if len(tp[i]) == 1 {
			tp[i] = "0" + tp[i]
		}
	}
	return out + " " + strings.Join(tp[:3], ":")
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func appendErr(list []string, format string, args ...any) []string {
	if len(list) >= 50 {
		return list
	}
	return append(list, fmt.Sprintf(format, args...))
}

// SaveLinks 覆盖保存某个产品的平台商品绑定。
//
// 表单里一次提交产品的全部绑定行，这里按行重建：
// 先校验有没有把别的产品的商品ID 抢过来，再整体替换。
func (s *Service) SaveLinks(ctx context.Context, productID int64, links []model.ProductEcLink, user *model.User) error {
	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return err
	}
	if product == nil {
		return UserErrf("产品不存在")
	}
	seen := map[string]bool{}
	for i := range links {
		links[i].ProductID = productID
		if links[i].Platform == "" {
			links[i].Platform = model.EcTaobao
		}
		links[i].EcProductID = strings.TrimSpace(links[i].EcProductID)
		if links[i].EcProductID == "" {
			return UserErrf("第 %d 行还没填平台商品ID", i+1)
		}
		key := links[i].Platform + "|" + links[i].EcProductID
		if seen[key] {
			return UserErrf("第 %d 行的商品ID 重复了", i+1)
		}
		seen[key] = true
		// 同一个平台商品ID 只能属于一个产品
		if existing, err := s.Store.EcLinkByEcID(ctx, links[i].Platform, links[i].EcProductID); err != nil {
			return err
		} else if existing != nil && existing.ProductID != productID {
			return UserErrf("%s的商品ID「%s」已经绑给「%s」了",
				model.EcPlatformLabel(links[i].Platform), links[i].EcProductID, existing.ProductName)
		}
	}

	// 旧绑定在事务外查好（事务里不能走连接池）
	old, err := s.Store.ProductLinks(ctx, productID)
	if err != nil {
		return err
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		// 先删掉本产品原有的绑定（DeleteEcLink 会顺带松绑对应订单明细）
		for _, l := range old {
			if err := s.Store.DeleteEcLink(ctx, tx, l.ID); err != nil {
				return err
			}
		}
		fixed := 0
		for _, l := range links {
			if conflict, err := s.Store.AddEcLink(ctx, tx, l); err != nil {
				return err
			} else if conflict != nil {
				return UserErrf("%s的商品ID「%s」已经绑给「%s」了",
					model.EcPlatformLabel(l.Platform), l.EcProductID, conflict.ProductName)
			}
			n, err := s.Store.BindEcOrderItems(ctx, tx, l.Platform, l.EcProductID, productID)
			if err != nil {
				return err
			}
			fixed += n
		}
		return s.Store.Log(ctx, tx, user, "设置电商绑定", "product", &productID,
			fmt.Sprintf("%s：%d 条绑定，补齐 %d 行订单明细", product.Name, len(links), fixed))
	})
}

// ---------------------------------------------------------------- 虚拟组套

// SaveBundles 保存产品的组套组成。
//
// 校验两条：
//   - 不能把自己组进自己；
//   - 组套不能嵌套（组成产品自己也是组套时拒绝），
//     否则库存与成本会递归算不清。
func (s *Service) SaveBundles(ctx context.Context, productID int64, bundles []model.ProductBundle, user *model.User) error {
	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return err
	}
	if product == nil {
		return UserErrf("产品不存在")
	}
	// 日志明细在事务外拼好：事务里不能查库
	var parts []string
	for _, b := range bundles {
		if comp, err := s.Store.ProductByID(ctx, b.ComponentID); err == nil && comp != nil {
			parts = append(parts, fmt.Sprintf("%s×%s", comp.Name, b.Qty))
		}
	}
	bundleDetail := "清空组套"
	if len(parts) > 0 {
		bundleDetail = strings.Join(parts, " + ")
	}

	for _, b := range bundles {
		if b.ComponentID == productID {
			return UserErrf("组成产品不能是它自己")
		}
		if b.Qty <= 0 {
			return UserErrf("组成用量必须大于 0")
		}
		comp, err := s.Store.ProductByID(ctx, b.ComponentID)
		if err != nil {
			return err
		}
		if comp == nil {
			return UserErrf("组成产品不存在")
		}
		// 嵌套组套会让库存与成本递归，先不支持
		if nested, err := s.Store.BundlesOf(ctx, comp.ID); err == nil && len(nested) > 0 {
			return UserErrf("「%s」自己也是组套，组套不能嵌套", comp.Name)
		}
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.ReplaceBundles(ctx, tx, productID, bundles); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "设置虚拟组套", "product", &productID,
			product.Name+" = "+bundleDetail)
	})
}

// BundleStock 组套的可售数量 = 各组成「库存 ÷ 用量」的最小值。
//
// 组套本身没有实体库存，能卖多少取决于组成产品。
func (s *Service) BundleStock(ctx context.Context, productID int64) (model.Qty, error) {
	bundles, err := s.Store.BundlesOf(ctx, productID)
	if err != nil || len(bundles) == 0 {
		return 0, err
	}
	var min model.Qty
	for i, b := range bundles {
		comp, err := s.Store.ProductByID(ctx, b.ComponentID)
		if err != nil {
			return 0, err
		}
		if comp == nil || b.Qty <= 0 {
			return 0, nil
		}
		canMake := model.DivQty(comp.StockQty, b.Qty)
		if i == 0 || canMake < min {
			min = canMake
		}
	}
	if min < 0 {
		min = 0
	}
	return min, nil
}

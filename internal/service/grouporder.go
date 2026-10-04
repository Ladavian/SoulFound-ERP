package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// GroupItemInput 团单明细行输入。
type GroupItemInput struct {
	ProductID   *int64
	ProductName string
	SKU         string
	Qty         model.Qty
	Unit        string
	UnitPrice   model.Money
	UnitCost    model.Money
	Note        string
}

// GroupOrderInput 团单输入。
type GroupOrderInput struct {
	CustomerID   *int64
	CustomerName string
	Contact      string
	Phone        string
	OrderDate    string
	ShipDate     string
	Warehouse    string
	Discount     model.Money
	ExtraFee     model.Money
	Note         string
	Items        []GroupItemInput
}

// Validate 校验团单输入。
func (in GroupOrderInput) Validate() error {
	if strings.TrimSpace(in.CustomerName) == "" && in.CustomerID == nil {
		return UserErrf("请填写客户名称，或从客户档案里选一个")
	}
	if in.Discount < 0 || in.ExtraFee < 0 {
		return UserErrf("优惠与其它费用不能为负数")
	}
	if len(in.Items) == 0 {
		return UserErrf("请至少填写一行产品明细")
	}
	for i, it := range in.Items {
		name := strings.TrimSpace(it.ProductName)
		if name == "" {
			return UserErrf("第 %d 行缺少产品名称", i+1)
		}
		if it.Qty <= 0 {
			return UserErrf("第 %d 行的数量必须大于 0", i+1)
		}
		if it.UnitPrice < 0 {
			return UserErrf("第 %d 行的单价不能为负数", i+1)
		}
		if it.UnitCost < 0 {
			return UserErrf("第 %d 行的成本单价不能为负数", i+1)
		}
	}
	return nil
}

// SaveGroupOrder 新建或修改团单。
//
// 团单只做记录留存：**不写库存流水、不动库存**，
// 因为货是从大仓发的，不在本系统管理的仓库里。
func (s *Service) SaveGroupOrder(ctx context.Context, id int64, in GroupOrderInput, user *model.User) (int64, error) {
	if err := in.Validate(); err != nil {
		return 0, err
	}
	var userID *int64
	if user != nil {
		uid := user.ID
		userID = &uid
	}

	order := &model.GroupOrder{
		CustomerID:   in.CustomerID,
		CustomerName: strings.TrimSpace(in.CustomerName),
		Contact:      strings.TrimSpace(in.Contact),
		Phone:        strings.TrimSpace(in.Phone),
		OrderDate:    strings.TrimSpace(in.OrderDate),
		ShipDate:     strings.TrimSpace(in.ShipDate),
		Warehouse:    strings.TrimSpace(in.Warehouse),
		Discount:     in.Discount,
		ExtraFee:     in.ExtraFee,
		Note:         strings.TrimSpace(in.Note),
	}
	if order.OrderDate == "" {
		order.OrderDate = store.Today()
	}
	// 从客户档案里选的时候，顺手把名称补齐，方便列表与搜索
	if order.CustomerName == "" && in.CustomerID != nil {
		if c, err := s.Store.CustomerByID(ctx, *in.CustomerID); err == nil && c != nil {
			order.CustomerName = c.Name
			if order.Phone == "" {
				order.Phone = c.Phone
			}
		}
	}

	items := make([]model.GroupOrderItem, 0, len(in.Items))
	for i, it := range in.Items {
		// 没填成本时用产品当前平均成本兜底（没有平均成本就用参考成本），
		// 这样毛利能直接算出来；系统里没有的产品就只能是 0，界面上会提示。
		cost := it.UnitCost
		if cost == 0 && it.ProductID != nil {
			if prod, err := s.Store.ProductByID(ctx, *it.ProductID); err == nil && prod != nil {
				cost = prod.CostPriceOrAvg()
			}
		}
		_ = i
		items = append(items, model.GroupOrderItem{
			ProductID:   it.ProductID,
			ProductName: strings.TrimSpace(it.ProductName),
			SKU:         strings.TrimSpace(it.SKU),
			Qty:         it.Qty,
			Unit:        strings.TrimSpace(it.Unit),
			UnitPrice:   it.UnitPrice,
			UnitCost:    cost,
			Note:        strings.TrimSpace(it.Note),
		})
	}

	// 修改前先读一次原单：必须在事务**外**读。
	// 连接池上限是 1，事务已经占用了唯一连接，
	// 在事务闭包里再走一次连接池会永远等不到连接，直到请求超时。
	var current *model.GroupOrder
	if id > 0 {
		var err error
		current, err = s.Store.GroupOrderByID(ctx, id)
		if err != nil {
			return 0, err
		}
		if current == nil {
			return 0, UserErrf("团单不存在")
		}
		if current.Status == model.GroupCancelled {
			return 0, UserErrf("已取消的团单不能再修改")
		}
	}

	var newID int64
	err := s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if id > 0 {
			order.ID = id
			if err := s.Store.UpdateGroupOrder(ctx, tx, order); err != nil {
				return err
			}
			if err := s.Store.ReplaceGroupItems(ctx, tx, id, items); err != nil {
				return err
			}
			newID = id
			return s.Store.Log(ctx, tx, user, "修改团单", "grouporder", &id, current.Code)
		}

		code, err := s.Store.NextCode(ctx, tx, "GO", "GT", time.Now())
		if err != nil {
			return err
		}
		order.Code = code
		order.Status = model.GroupDraft
		order.CreatedBy = userID
		nid, err := s.Store.InsertGroupOrder(ctx, tx, order)
		if err != nil {
			return err
		}
		if err := s.Store.ReplaceGroupItems(ctx, tx, nid, items); err != nil {
			return err
		}
		newID = nid
		return s.Store.Log(ctx, tx, user, "新建团单", "grouporder", &nid,
			fmt.Sprintf("%s %s %s", code, order.CustomerName, order.Total()))
	})
	if err != nil {
		return 0, err
	}
	return newID, nil
}

// SetGroupOrderStatus 变更团单状态。
func (s *Service) SetGroupOrderStatus(ctx context.Context, id int64, status string, user *model.User) error {
	if _, ok := model.GroupStatusLabels[status]; !ok {
		return UserErrf("状态不正确")
	}
	order, err := s.Store.GroupOrderByID(ctx, id)
	if err != nil {
		return err
	}
	if order == nil {
		return UserErrf("团单不存在")
	}
	if order.Status == status {
		return UserErrf("状态没有变化")
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.SetGroupOrderStatus(ctx, tx, id, status); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "团单状态", "grouporder", &id,
			order.StatusLabel()+" → "+model.GroupStatusLabel(status))
	})
}

// DeleteGroupOrder 删除团单。
func (s *Service) DeleteGroupOrder(ctx context.Context, id int64, user *model.User) error {
	order, err := s.Store.GroupOrderByID(ctx, id)
	if err != nil {
		return err
	}
	if order == nil {
		return UserErrf("团单不存在")
	}
	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.Store.DeleteGroupOrder(ctx, tx, id); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "删除团单", "grouporder", &id, order.Code)
	})
}

// GroupOrderSummaryBetween 统计一段时间内的团单。
func (s *Service) GroupOrderSummaryBetween(ctx context.Context, from, to string) (model.GroupOrderSummary, error) {
	out := model.GroupOrderSummary{ByStatus: map[string]int{}}
	orders, err := s.Store.ListGroupOrders(ctx, store.GroupOrderFilter{From: from, To: to, Sort: "oldest"})
	if err != nil {
		return out, err
	}
	for _, o := range orders {
		// 取消的不计入金额，但计入状态分布
		out.ByStatus[o.Status]++
		if o.Status == model.GroupCancelled {
			continue
		}
		out.Count++
		out.Qty += o.TotalQty()
		out.Amount += o.Total()
		out.Cost += o.TotalCost()
		out.Profit += o.Profit()
	}
	return out, nil
}

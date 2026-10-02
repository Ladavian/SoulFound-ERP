package service

import (
	"context"
	"database/sql"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// PostRequest 一次库存过账请求。
type PostRequest struct {
	ProductID  int64
	Qty        model.Qty   // 正数入库、负数出库
	UnitCost   model.Money // 入库成本；撤销出库时的原始成本
	ForceCost  bool        // 出库时强制按 UnitCost 计价（撤销、重算用）
	OccurredOn string
	Reason     string
	RefType    string
	RefID      *int64
	RefCode    string
	Note       string
	UserID     *int64
}

// Poster 在单个事务内批量过账。
//
// 内部缓存产品结存，保证同一产品在一个事务内的多笔变动能累积计算正确
// （例如一场市集里同一产品的销售、试饮、赠送、损耗），同时避免重复读库。
type Poster struct {
	svc      *Service
	ctx      context.Context
	tx       *sql.Tx
	allowNeg bool
	products map[int64]*model.Product
}

// NewPoster 创建过账器。allowNegative 控制是否允许负库存。
func (s *Service) NewPoster(ctx context.Context, tx *sql.Tx, allowNegative bool) *Poster {
	return &Poster{
		svc:      s,
		ctx:      ctx,
		tx:       tx,
		allowNeg: allowNegative,
		products: make(map[int64]*model.Product),
	}
}

// Product 取产品（带事务内缓存）。
func (p *Poster) Product(id int64) (*model.Product, error) {
	if prod, ok := p.products[id]; ok {
		return prod, nil
	}
	prod, err := p.svc.Store.ProductByIDTx(p.ctx, p.tx, id)
	if err != nil {
		return nil, err
	}
	if prod == nil {
		return nil, UserErrf("产品不存在（ID %d），可能已被删除", id)
	}
	p.products[id] = prod
	return prod, nil
}

// CostOf 产品当前平均成本（结算快照用）。
func (p *Poster) CostOf(productID int64) (model.Money, error) {
	prod, err := p.Product(productID)
	if err != nil {
		return 0, err
	}
	return prod.AvgCost, nil
}

// Post 过账一笔库存变动，并同步产品库存与平均成本。
func (p *Poster) Post(req PostRequest) (*model.StockMovement, error) {
	if req.Qty == 0 {
		return nil, nil
	}
	prod, err := p.Product(req.ProductID)
	if err != nil {
		return nil, err
	}

	cost := req.UnitCost
	if req.Qty < 0 {
		if !p.allowNeg && -req.Qty > prod.StockQty {
			return nil, UserErrf("「%s」库存不足：当前库存 %s %s，本次需要出库 %s %s",
				prod.Name, prod.StockQty, prod.Unit, -req.Qty, prod.Unit)
		}
		if !req.ForceCost {
			cost = prod.AvgCost
		}
	}

	newQty, newAvg, newValue := model.ApplyMovement(prod.StockQty, prod.AvgCost, req.Qty, cost)

	occurred := req.OccurredOn
	if occurred == "" {
		occurred = store.Today()
	}
	mv := &model.StockMovement{
		ProductID:    req.ProductID,
		OccurredOn:   occurred,
		Qty:          req.Qty,
		UnitCost:     cost,
		TotalCost:    model.MulQty(req.Qty, cost),
		QtyAfter:     newQty,
		AvgCostAfter: newAvg,
		ValueAfter:   newValue,
		Reason:       req.Reason,
		RefType:      req.RefType,
		RefID:        req.RefID,
		RefCode:      req.RefCode,
		Note:         req.Note,
		CreatedBy:    req.UserID,
	}
	if _, err := p.svc.Store.InsertMovement(p.ctx, p.tx, mv); err != nil {
		return nil, err
	}
	if err := p.svc.Store.UpdateProductStock(p.ctx, p.tx, prod.ID, newQty, newAvg, newValue); err != nil {
		return nil, err
	}
	prod.StockQty, prod.AvgCost, prod.StockValue = newQty, newAvg, newValue
	return mv, nil
}

// PostMany 依次过账多笔变动。
func (p *Poster) PostMany(reqs []PostRequest) error {
	for _, req := range reqs {
		if _, err := p.Post(req); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- 重算

// RebuildProductStock 依据流水重放，重建某产品的库存与加权平均成本。
// 用于撤销单据后修正受影响产品。
func (s *Service) RebuildProductStock(ctx context.Context, tx *sql.Tx, productID int64) error {
	moves, err := s.Store.ProductMovementsAsc(ctx, tx, productID)
	if err != nil {
		return err
	}
	var (
		qty model.Qty
		avg model.Money
	)
	for _, m := range moves {
		newQty, newAvg, newValue := model.ApplyMovement(qty, avg, m.Qty, m.UnitCost)
		if err := s.Store.UpdateMovementSnapshot(ctx, tx, m.ID, newQty, newAvg, newValue); err != nil {
			return err
		}
		qty, avg = newQty, newAvg
	}
	return s.Store.UpdateProductStock(ctx, tx, productID, qty, avg, model.MulQty(qty, avg))
}

// RebuildAffected 重建一组产品的库存。
func (s *Service) RebuildAffected(ctx context.Context, tx *sql.Tx, productIDs map[int64]bool) error {
	for id := range productIDs {
		if err := s.RebuildProductStock(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// RebuildAllStock 重算全部产品库存（维护功能）。
func (s *Service) RebuildAllStock(ctx context.Context) (int, error) {
	products, err := s.Store.ListProducts(ctx, store.ProductFilter{IncludeInactive: true})
	if err != nil {
		return 0, err
	}
	err = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		for _, p := range products {
			if err := s.RebuildProductStock(ctx, tx, p.ID); err != nil {
				return err
			}
		}
		return nil
	})
	return len(products), err
}

// ---------------------------------------------------------------- 手工出入库

// AdjustInput 手工出入库 / 盘点调整输入。
type AdjustInput struct {
	ProductID  int64
	Qty        model.Qty
	UnitCost   model.Money
	OccurredOn string
	Reason     string
	Note       string
}

var manualReasons = map[string]bool{
	model.ReasonOpening:    true,
	model.ReasonAdjustIn:   true,
	model.ReasonAdjustOut:  true,
	model.ReasonReturnIn:   true,
	model.ReasonMarketGift: true,
	model.ReasonMarketLoss: true,
}

// AdjustStock 手工登记一笔出入库（期初建账、盘点、损耗、退货等）。
func (s *Service) AdjustStock(ctx context.Context, in AdjustInput, user *model.User) error {
	if in.ProductID <= 0 {
		return UserErrf("请选择产品")
	}
	if in.Qty == 0 {
		return UserErrf("数量不能为 0")
	}
	if in.Reason == "" {
		in.Reason = model.ReasonAdjustIn
	}
	if !manualReasons[in.Reason] {
		return UserErrf("不支持的单据类型")
	}
	if in.Qty > 0 && in.Reason == model.ReasonReturnIn && in.UnitCost == 0 {
		// 退货入库未填成本时按当前平均成本计价
		prod, err := s.Store.ProductByID(ctx, in.ProductID)
		if err != nil {
			return err
		}
		if prod != nil {
			in.UnitCost = prod.AvgCost
		}
	}
	if in.Qty < 0 && in.Reason == model.ReasonAdjustIn {
		return UserErrf("「盘点调增」只允许正数数量")
	}
	if in.Qty > 0 && in.Reason == model.ReasonAdjustOut {
		return UserErrf("「盘点调减」只允许负数数量")
	}

	cfg, err := s.Store.Settings(ctx)
	if err != nil {
		return err
	}

	var userID *int64
	if user != nil {
		id := user.ID
		userID = &id
	}

	return s.Store.Tx(ctx, func(tx *sql.Tx) error {
		poster := s.NewPoster(ctx, tx, cfg.AllowNegative)
		if _, err := poster.Post(PostRequest{
			ProductID:  in.ProductID,
			Qty:        in.Qty,
			UnitCost:   in.UnitCost,
			OccurredOn: in.OccurredOn,
			Reason:     in.Reason,
			RefType:    "manual",
			RefCode:    "手工登记",
			Note:       in.Note,
			UserID:     userID,
		}); err != nil {
			return err
		}
		return s.Store.Log(ctx, tx, user, "库存调整", "product", &in.ProductID,
			model.ReasonLabel(in.Reason)+" "+in.Qty.String())
	})
}

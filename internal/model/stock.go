package model

// ApplyMovement 计算一次库存变动后的结存（移动加权平均法）。
//
// 规则：
//   - 入库（qty > 0）：新库存价值 = 旧价值 + 入库数量 × 入库成本，再据此重算平均成本。
//   - 出库（qty < 0）：按传入的 unitCost 扣减价值。正常出库传当前平均成本，
//     撤销/重算时传原始成本以保证价值守恒。
//   - 结存价值始终等于 数量 × 平均成本，避免浮点残差累积。
//
// 返回新的数量、平均成本与库存价值。
func ApplyMovement(oldQty Qty, oldAvg Money, qty Qty, unitCost Money) (Qty, Money, Money) {
	oldValue := MulQty(oldQty, oldAvg)
	newQty := oldQty + qty
	newValue := oldValue + MulQty(qty, unitCost)
	if newQty == 0 {
		return 0, oldAvg, 0
	}
	newAvg := DivByQty(newValue, newQty)
	return newQty, newAvg, MulQty(newQty, newAvg)
}

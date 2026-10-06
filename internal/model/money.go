// Package model 定义领域模型与金额/数量等基础类型。
//
// 设计要点：金额与数量一律使用整数最小单位存储与运算，彻底避开浮点误差。
//
//	Money = 1/10000 货币单位   （C$35.00 -> 350000）
//	Qty   = 1/1000 件          （12 瓶    -> 12000）
//
// 移动加权平均成本需要 4 位小数精度，因此 Money 比“分”多两位。
package model

import (
	"fmt"
	"math"
	"strings"
)

// Money 金额，单位 1/10000 货币单位。
type Money int64

// Qty 数量，单位 1/1000 件。
type Qty int64

const (
	// MoneyDecimals Money 的小数位数。
	MoneyDecimals = 4
	// QtyDecimals Qty 的小数位数。
	QtyDecimals = 3

	moneyScale = 10000
	qtyScale   = 1000
)

var pow10 = [8]int64{1, 10, 100, 1000, 10000, 100000, 1000000, 10000000}

// ---------------------------------------------------------------- 解析

func parseScaled(raw string, decimals int) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	neg := false
	seenDigit := false
	var intPart strings.Builder
	var fracPart strings.Builder
	inFrac := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			seenDigit = true
			if inFrac {
				fracPart.WriteRune(r)
			} else {
				intPart.WriteRune(r)
			}
		case r == '.' || r == '。':
			inFrac = true
		case r == '-' && !seenDigit:
			neg = true
		case r == '+' && !seenDigit:
			// 忽略
		default:
			// 货币符号、千分位、空格等一律忽略
		}
	}
	ip := intPart.String()
	fp := fracPart.String()
	if !seenDigit {
		if strings.TrimSpace(strings.Trim(s, "-+()")) == "" {
			return 0, nil
		}
		return 0, fmt.Errorf("无法解析数字：%q", raw)
	}
	iv, err := parseInt64(ip)
	if err != nil {
		return 0, fmt.Errorf("数字过大：%q", raw)
	}
	value := iv * pow10[decimals]
	// 小数部分：截断 + 四舍五入
	if len(fp) > 0 {
		keep := fp
		var roundUp bool
		if len(keep) > decimals {
			roundUp = keep[decimals] >= '5'
			keep = keep[:decimals]
		}
		fv, err := parseInt64(keep)
		if err != nil {
			return 0, fmt.Errorf("无法解析数字：%q", raw)
		}
		fv *= pow10[decimals-len(keep)]
		value += fv
		if roundUp {
			value++
		}
	}
	if neg {
		value = -value
	}
	return value, nil
}

func parseInt64(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	var v int64
	for _, r := range s {
		d := int64(r - '0')
		if v > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("overflow")
		}
		v = v*10 + d
	}
	return v, nil
}

// ParseMoney 解析用户输入的金额，容错处理货币符号与千分位。
func ParseMoney(raw string) (Money, error) {
	v, err := parseScaled(raw, MoneyDecimals)
	return Money(v), err
}

// ParseQty 解析用户输入的数量。
func ParseQty(raw string) (Qty, error) {
	v, err := parseScaled(raw, QtyDecimals)
	return Qty(v), err
}

// MustMoney 解析金额，失败返回 0（仅用于常量与演示数据）。
func MustMoney(raw string) Money {
	v, err := ParseMoney(raw)
	if err != nil {
		return 0
	}
	return v
}

// MustQty 解析数量，失败返回 0。
func MustQty(raw string) Qty {
	v, err := ParseQty(raw)
	if err != nil {
		return 0
	}
	return v
}

// MoneyFromCents 由“分”构造金额。
func MoneyFromCents(cents int64) Money { return Money(cents * 100) }

// MoneyFromFloat 由浮点构造金额（四舍五入），仅用于图表与统计。
func MoneyFromFloat(f float64) Money { return Money(math.Round(f * moneyScale)) }

// QtyFromInt 由整数件数构造数量。
func QtyFromInt(n int64) Qty { return Qty(n * qtyScale) }

// ---------------------------------------------------------------- 转换

// Float 返回浮点值，仅用于图表绘制。
func (m Money) Float() float64 { return float64(m) / moneyScale }

// Float 返回浮点值，仅用于图表绘制。
func (q Qty) Float() float64 { return float64(q) / qtyScale }

// Int 返回四舍五入后的整数件数。
func (q Qty) Int() int64 { return int64(math.Round(float64(q) / qtyScale)) }

// Cents 返回分为单位的整数。
func (m Money) Cents() int64 { return int64(math.Round(float64(m) / 100)) }

// IsZero 是否为零。
func (m Money) IsZero() bool { return m == 0 }

// IsNeg 是否为负。
func (m Money) IsNeg() bool { return m < 0 }

// IsPos 是否大于 0。
func (m Money) IsPos() bool { return m > 0 }

// ---------------------------------------------------------------- 运算

func divRound(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	neg := (a < 0) != (b < 0)
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	r := (a + b/2) / b
	if neg {
		return -r
	}
	return r
}

// MulQty 金额 × 数量，得金额。
// DivQty 两个数量相除（保留 1/1000 精度），用于"库存 ÷ 用量"这类计算。
func DivQty(a, b Qty) Qty {
	if b == 0 {
		return 0
	}
	return Qty(int64(a) * qtyScale / int64(b))
}

// DivQtyInt 数量除以整数。
func DivQtyInt(a Qty, n int64) Qty {
	if n == 0 {
		return 0
	}
	return Qty(int64(a) / n)
}

func MulQty(q Qty, m Money) Money {
	return Money(divRound(int64(q)*int64(m), qtyScale))
}

// DivByQty 金额 ÷ 数量，得单价金额。
func DivByQty(m Money, q Qty) Money {
	if q == 0 {
		return 0
	}
	return Money(divRound(int64(m)*qtyScale, int64(q)))
}

// MulInt 金额 × 整数。
func (m Money) MulInt(n int64) Money { return Money(int64(m) * n) }

// MulRatio 金额 × 比例。
func (m Money) MulRatio(r float64) Money { return Money(math.Round(float64(m) * r)) }

// DivRatio 金额 ÷ 比例（用于反算）。
func (m Money) DivRatio(r float64) Money {
	if r == 0 {
		return 0
	}
	return Money(math.Round(float64(m) / r))
}

// Ratio 计算 a/b 的百分比。
func Ratio(a, b Money) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b) * 100
}

// Round2 四舍五入到分。
func (m Money) Round2() Money { return Money(divRound(int64(m), 100) * 100) }

// Add 金额相加。
func (m Money) Add(o Money) Money { return m + o }

// Sub 金额相减。
func (m Money) Sub(o Money) Money { return m - o }

// Neg 取负。
func (m Money) Neg() Money { return -m }

// AvgCost 移动加权平均：入库后的新平均成本。
func AvgCost(oldQty Qty, oldAvg Money, inQty Qty, inCost Money) Money {
	total := oldQty + inQty
	if total <= 0 {
		return inCost
	}
	numerator := int64(oldQty)*int64(oldAvg) + int64(inQty)*int64(inCost)
	return Money(divRound(numerator, int64(total)))
}

// SumMoney 金额求和。
func SumMoney(items ...Money) Money {
	var total Money
	for _, it := range items {
		total += it
	}
	return total
}

// SumQty 数量求和。
func SumQty(items ...Qty) Qty {
	var total Qty
	for _, it := range items {
		total += it
	}
	return total
}

func groupThousands(s string) string {
	n := len(s)
	if n <= 3 {
		return s
	}
	var b strings.Builder
	pre := n % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if n > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < n; i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < n {
			b.WriteByte(',')
		}
	}
	return b.String()
}

func formatScaled(v int64, decimals int) string {
	neg := v < 0
	if neg {
		v = -v
	}
	scale := pow10[decimals]
	ip := v / scale
	fp := v % scale
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	b.WriteString(groupThousands(fmt.Sprintf("%d", ip)))
	if decimals > 0 {
		b.WriteByte('.')
		b.WriteString(fmt.Sprintf("%0*d", decimals, fp))
	}
	return b.String()
}

func trimScaled(v int64, decimals int) string {
	s := formatScaled(v, decimals)
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimRight(s, ".")
}

// String 金额文本，固定 2 位小数。
//
// Money 的内部精度是 1/10000，展示到「分」需要先换算成 1/100 单位。
func (m Money) String() string { return formatScaled(divRound(int64(m), 100), 2) }

// String4 金额文本，至少 2 位、最多 4 位小数（用于单位成本）。
func (m Money) String4() string {
	s := trimScaled(int64(m), MoneyDecimals)
	if idx := strings.IndexByte(s, '.'); idx < 0 {
		return s + ".00"
	} else if len(s)-idx == 2 {
		return s + "0"
	}
	return s
}

// Plain 金额文本，无千分位，固定 2 位小数（用于 CSV）。
func (m Money) Plain() string {
	return fmt.Sprintf("%.2f", m.Float())
}

// Plain4 金额文本，无千分位，最多 4 位小数。
func (m Money) Plain4() string {
	return fmt.Sprintf("%.4f", m.Float())
}

// String 数量文本，去掉多余的 0。
func (q Qty) String() string { return trimScaled(int64(q), QtyDecimals) }

// Plain 数量文本，无千分位。
func (q Qty) Plain() string {
	s := trimScaled(int64(q), QtyDecimals)
	return strings.ReplaceAll(s, ",", "")
}

// PctOf 按万分比取值：rate 300 表示 3%。
func PctOf(base Money, rate int) Money {
	if base == 0 || rate == 0 {
		return 0
	}
	return Money(divRound(int64(base)*int64(rate), 10000))
}

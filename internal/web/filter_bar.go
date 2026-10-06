package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"icewine-erp/internal/model"
)

// FilterChip 一个可点选的筛选项（平台 / 账期）。
type FilterChip struct {
	Value  string
	Label  string
	Active bool
	Href   string // 点它之后跳到的地址（已带上切换后的参数）
	Count  int    // 这个选项下有多少条数据，0 表示可以先不看
}

// FilterBar 筛选条要渲染的数据。
type FilterBar struct {
	Title    string
	Chips    []FilterChip
	AllHref  string
	NoneHref string
	Hint     string
	Multi    bool
}

// toggleQuery 生成"点一下切换某个值"的地址。
//
// 多选维度（账期、总对账单的平台）用重复参数：period=202608&period=202609；
// 单选维度（平台账单页的平台）直接替换。
// 用普通链接实现，不依赖 JS，地址栏可收藏可分享。
func toggleQuery(r *http.Request, key, value string, multi bool) string {
	q := r.URL.Query()
	if multi {
		cur := q[key]
		out := make([]string, 0, len(cur)+1)
		found := false
		for _, v := range cur {
			if v == value {
				found = true
				continue
			}
			out = append(out, v)
		}
		if !found {
			out = append(out, value)
		}
		q.Del(key)
		for _, v := range out {
			q.Add(key, v)
		}
	} else {
		if q.Get(key) == value {
			q.Del(key)
		} else {
			q.Set(key, value)
		}
	}
	// 换筛选时回到第一页
	q.Del("page")
	return r.URL.Path + "?" + q.Encode()
}

// setQuery 生成"把某个参数设成指定值"的地址（用于全选 / 清空）。
func setQuery(r *http.Request, key string, values []string, drop ...string) string {
	q := r.URL.Query()
	q.Del(key)
	for _, v := range values {
		q.Add(key, v)
	}
	for _, d := range drop {
		q.Del(d)
	}
	q.Del("page")
	return r.URL.Path + "?" + q.Encode()
}

// selectedPeriods 读取选中的账期（多选，去重后按倒序）。
//
// 没选就返回 all 里最新的若干期，避免一进来什么都没有。
func selectedPeriods(r *http.Request, all []string, defaultN int) []string {
	raw := r.URL.Query()["period"]
	seen := map[string]bool{}
	var out []string
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		n := defaultN
		if n <= 0 || n > len(all) {
			n = len(all)
		}
		out = append(out, all[:n]...)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// selectedValues 读取多选参数（如总对账单的平台）。
func selectedValues(r *http.Request, key string, all []string) []string {
	raw := r.URL.Query()[key]
	seen := map[string]bool{}
	var out []string
	for _, v := range raw {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return all
	}
	return out
}

// periodChips 账期多选 chips。
func periodChips(r *http.Request, all []string, selected []string, counts map[string]int) FilterBar {
	sel := map[string]bool{}
	for _, p := range selected {
		sel[p] = true
	}
	bar := FilterBar{
		Title: "账期",
		Multi: true,
		Hint:  "可多选：小平台两三个月结一次时，把几个月一起勾上",
	}
	for _, p := range all {
		bar.Chips = append(bar.Chips, FilterChip{
			Value:  p,
			Label:  periodLabelForUI(p),
			Active: sel[p],
			Href:   toggleQuery(r, "period", p, true),
			Count:  counts[p],
		})
	}
	bar.AllHref = setQuery(r, "period", all)
	bar.NoneHref = setQuery(r, "period", nil)
	return bar
}

// valueChips 通用多选 chips（如总对账单的平台）。
func valueChips(r *http.Request, key, title string, all []model.Option, selected []string) FilterBar {
	sel := map[string]bool{}
	for _, v := range selected {
		sel[v] = true
	}
	bar := FilterBar{Title: title, Multi: true}
	var vals []string
	for _, o := range all {
		bar.Chips = append(bar.Chips, FilterChip{
			Value: o.Value, Label: o.Label, Active: sel[o.Value],
			Href: toggleQuery(r, key, o.Value, true),
		})
		vals = append(vals, o.Value)
	}
	bar.AllHref = setQuery(r, key, vals)
	bar.NoneHref = setQuery(r, key, nil)
	return bar
}

// periodLabelForUI 202608 → 2026-08。
func periodLabelForUI(p string) string {
	if len(p) == 6 {
		return p[:4] + "-" + p[4:]
	}
	return p
}

// periodLabelMulti 多个账期的展示文案。
func periodLabelMulti(periods []string) string {
	switch len(periods) {
	case 0:
		return ""
	case 1:
		return periodLabelForUI(periods[0])
	}
	// 多个时给个区间感：2026-06 ~ 2026-09（共 4 期）
	sorted := append([]string{}, periods...)
	sort.Strings(sorted)
	return periodLabelForUI(sorted[0]) + " ~ " + periodLabelForUI(sorted[len(sorted)-1]) +
		"（共 " + strconv.Itoa(len(sorted)) + " 期）"
}

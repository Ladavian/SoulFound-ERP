package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
)

// formReader 表单读取器：自动收集字段错误，避免每个处理函数重复判断。
type formReader struct {
	r    *http.Request
	errs []string
}

func newFormReader(r *http.Request) *formReader {
	_ = r.ParseForm()
	return &formReader{r: r}
}

// value 读取单个字段。
//
// 注意：必须走 r.Form 而不是 r.PostForm —— PostForm 只包含请求体，
// 对 GET 请求的查询参数（?q=...&status=...）会一律返回空字符串，
// 那样所有列表页的筛选、搜索、分页都会失效。r.Form 同时包含
// 请求体与 URL 查询串（请求体优先），正是我们要的语义。
func (f *formReader) value(key string) string {
	if f.r.Form == nil {
		_ = f.r.ParseForm()
	}
	if vs := f.r.Form[key]; len(vs) > 0 {
		return vs[0]
	}
	if vs := f.r.PostForm[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func (f *formReader) Str(key string) string {
	return strings.TrimSpace(f.value(key))
}

func (f *formReader) Raw(key string) string { return f.value(key) }

func (f *formReader) List(key string) []string {
	if f.r.Form == nil {
		_ = f.r.ParseForm()
	}
	if vs := f.r.Form[key]; len(vs) > 0 {
		return vs
	}
	return f.r.PostForm[key]
}

func (f *formReader) Bool(key string) bool {
	v := strings.ToLower(f.Str(key))
	return v == "1" || v == "true" || v == "on" || v == "yes"
}

func (f *formReader) Int(key, label string) int {
	raw := f.Str(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		f.errs = append(f.errs, label+"必须是整数")
		return 0
	}
	return n
}

func (f *formReader) ID(key, label string) int64 {
	raw := f.Str(key)
	if raw == "" || raw == "0" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		f.errs = append(f.errs, label+"不正确")
		return 0
	}
	return n
}

func (f *formReader) OptionalID(key string) *int64 {
	raw := f.Str(key)
	if raw == "" || raw == "0" {
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

func (f *formReader) Money(key, label string) model.Money {
	raw := f.Str(key)
	if raw == "" {
		return 0
	}
	v, err := model.ParseMoney(raw)
	if err != nil {
		f.errs = append(f.errs, label+"格式不正确")
		return 0
	}
	return v
}

func (f *formReader) Qty(key, label string) model.Qty {
	raw := f.Str(key)
	if raw == "" {
		return 0
	}
	v, err := model.ParseQty(raw)
	if err != nil {
		f.errs = append(f.errs, label+"格式不正确")
		return 0
	}
	return v
}

// Required 校验必填。
func (f *formReader) Required(key, label string) string {
	raw := f.Str(key)
	if raw == "" {
		f.errs = append(f.errs, label+"不能为空")
	}
	return raw
}

// Err 汇总错误。
func (f *formReader) Err() error {
	if len(f.errs) == 0 {
		return nil
	}
	// 最多展示 3 条，避免提示过长
	errs := f.errs
	if len(errs) > 3 {
		errs = errs[:3]
	}
	return service.UserErrf("%s", strings.Join(errs, "；"))
}

// AddError 手工追加错误。
func (f *formReader) AddError(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}

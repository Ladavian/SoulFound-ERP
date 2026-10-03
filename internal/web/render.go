package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"icewine-erp/internal/assets"
	"icewine-erp/internal/model"
)

// Renderer 负责模板解析与渲染。生产模式下模板随二进制打包，开发模式读取磁盘。
type Renderer struct {
	dir   string
	dev   bool
	mu    sync.RWMutex
	cache map[string]*template.Template
	sym   atomic.Value // string，货币符号
}

// NewRenderer 创建渲染器。
func NewRenderer(dir string, dev bool, symbol string) *Renderer {
	r := &Renderer{dir: dir, dev: dev, cache: map[string]*template.Template{}}
	r.sym.Store(symbol)
	return r
}

// SetSymbol 更新货币符号（保存设置后调用）。
func (r *Renderer) SetSymbol(symbol string) {
	if strings.TrimSpace(symbol) == "" {
		symbol = "¥"
	}
	r.sym.Store(symbol)
}

func (r *Renderer) symbol() string {
	if v, ok := r.sym.Load().(string); ok {
		return v
	}
	return "¥"
}

// withSymbol 拼接货币符号，负数把负号放到符号前面。
func withSymbol(sym, amount string, negative bool) string {
	if negative {
		return "-" + sym + strings.TrimPrefix(amount, "-")
	}
	return sym + amount
}

// StaticFS 返回静态资源文件系统。
func StaticFS() (fs.FS, error) {
	return fs.Sub(assets.FS, "static")
}

func (r *Renderer) funcs() template.FuncMap {
	sym := r.symbol()
	return template.FuncMap{
		// 金额与数量。负数把负号放在货币符号前：-¥123.45，而不是 ¥-123.45
		"money":  func(v model.Money) string { return withSymbol(sym, v.String(), v.IsNeg()) },
		"money0": func(v model.Money) string { return withSymbol(sym, v.Round2().String(), v.IsNeg()) },
		"money4": func(v model.Money) string { return withSymbol(sym, v.String4(), v.IsNeg()) },
		"moneyS": func(v model.Money) string { return withSymbol(sym, v.String(), v.IsNeg()) },
		"pct":    func(v float64) string { return fmt.Sprintf("%.1f%%", v) },
		"pct0":   func(v float64) string { return fmt.Sprintf("%.0f%%", v) },
		"num":    func(v float64) string { return fmt.Sprintf("%.2f", v) },
		"neg":    func(v model.Money) bool { return v.IsNeg() },
		"ratio":  func(a, b model.Money) float64 { return model.Ratio(a, b) },

		// 数量输入框的值
		"qtyInput": func(v model.Qty) string {
			s := v.Plain()
			if s == "" {
				return "0"
			}
			return s
		},
		"moneyInput":  func(v model.Money) string { return v.Plain4() },
		"moneyInput2": func(v model.Money) string { return v.Plain() },

		// 日期
		"date": func(v string) string {
			if len(v) >= 10 {
				return v[:10]
			}
			return v
		},
		"datetime": func(v string) string {
			if len(v) >= 16 {
				return strings.ReplaceAll(v[:16], "T", " ")
			}
			return v
		},
		"today": func() string { return time.Now().Format("2006-01-02") },
		"daysFromNow": func(days int) string {
			return time.Now().AddDate(0, 0, days).Format("2006-01-02")
		},

		// 字符串与逻辑
		"default": func(fallback, v string) string {
			if strings.TrimSpace(v) == "" {
				return fallback
			}
			return v
		},
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
		"contains":  strings.Contains,
		"hasPrefix": strings.HasPrefix,
		"truncate": func(n int, s string) string {
			runes := []rune(s)
			if len(runes) <= n {
				return s
			}
			return string(runes[:n]) + "…"
		},
		"join": strings.Join,
		"add":  func(a, b int) int { return a + b },
		"sub":  func(a, b int) int { return a - b },
		"mul":  func(a, b float64) float64 { return a * b },
		"div": func(a, b float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"fixed":       func(n int, v float64) string { return fmt.Sprintf("%.*f", n, v) },
		"groupStatus": func(v string) string { return model.GroupStatusLabel(v) },
		"reasonLabel": func(v string) string { return model.ReasonLabel(v) },
		"deref": func(v *int64) int64 {
			if v == nil {
				return 0
			}
			return *v
		},
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"even": func(i int) bool { return i%2 == 0 },
		"dict": func(values ...any) map[string]any {
			out := map[string]any{}
			for i := 0; i+1 < len(values); i += 2 {
				if key, ok := values[i].(string); ok {
					out[key] = values[i+1]
				}
			}
			return out
		},
		"json": func(v any) template.JS {
			return template.JS(jsonEncode(v))
		},
		// 图表用的浮点数组
		"floats": func(values []model.Money) []float64 {
			out := make([]float64, len(values))
			for i, v := range values {
				out[i] = v.Float()
			}
			return out
		},
	}
}

// parse 为指定页面构建模板集合（布局 + 公共片段 + 页面）。
func (r *Renderer) parse(page string) (*template.Template, error) {
	pageFile := path.Join("templates/pages", page+".html")
	patterns := []string{
		"templates/layouts/*.html",
		"templates/partials/*.html",
		pageFile,
	}

	var (
		tpl *template.Template
		err error
	)
	// 根模板必须用一个不会被 {{define}} 占用的名字：若这里叫 "layout"，
	// 会与 base.html 里的 {{define "layout"}} 冲突，执行时报
	// "layout is an incomplete template"。
	if r.dev {
		tpl, err = template.New("root").Funcs(r.funcs()).ParseFS(os.DirFS(r.dir), patterns...)
	} else {
		tpl, err = template.New("root").Funcs(r.funcs()).ParseFS(assets.FS, patterns...)
	}
	if err != nil {
		return nil, fmt.Errorf("解析模板 %s 失败: %w", page, err)
	}
	return tpl, nil
}

func (r *Renderer) lookup(page string) (*template.Template, error) {
	if r.dev {
		return r.parse(page)
	}
	r.mu.RLock()
	tpl, ok := r.cache[page]
	r.mu.RUnlock()
	if ok {
		return tpl, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if tpl, ok := r.cache[page]; ok {
		return tpl, nil
	}
	tpl, err := r.parse(page)
	if err != nil {
		return nil, err
	}
	r.cache[page] = tpl
	return tpl, nil
}

// Render 渲染完整页面（执行 layout）。
//
// 先渲染到内存缓冲再写出：模板执行到一半出错时，不会把半截 HTML
// 连同 200 状态码发给浏览器（那样错误会被掩盖，很难排查）。
func (r *Renderer) Render(w http.ResponseWriter, page string, data map[string]any) error {
	tpl, err := r.lookup(page)
	if err != nil {
		return fmt.Errorf("加载模板 %s 失败: %w", page, err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return fmt.Errorf("渲染页面 %s 失败: %w", page, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = w.Write(buf.Bytes())
	return err
}

// RenderPartial 渲染页面文件中的某个命名片段（HTMX 局部刷新用）。
func (r *Renderer) RenderPartial(w http.ResponseWriter, page, name string, data map[string]any) error {
	tpl, err := r.lookup(page)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("渲染片段 %s/%s 失败: %w", page, name, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = w.Write(buf.Bytes())
	return err
}

// RenderError 渲染错误页。
func (r *Renderer) RenderError(w http.ResponseWriter, status int, title, detail string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	data["Title"] = title
	data["Status"] = status
	data["Detail"] = detail
	tpl, err := r.lookup("error")
	if err != nil {
		http.Error(w, title+": "+detail, status)
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	return tpl.ExecuteTemplate(w, "layout", data)
}

// ---------------------------------------------------------------- 页面数据

// newPage 构造页面数据的公共部分。
func (s *Server) newPage(r *http.Request, title, nav string) map[string]any {
	user := userFrom(r)
	role := ""
	if user != nil {
		role = user.Role
	}
	return map[string]any{
		"Title":    title,
		"Nav":      nav,
		"User":     user,
		"Cfg":      s.cfg,
		"Settings": settingsFrom(r),
		"Flash":    flashFrom(r),
		"Perms":    permsForUser(user),
		"IsAdmin":  role == model.RoleAdmin,
		"Today":    time.Now().Format("2006-01-02"),
	}
}

func jsonEncode(v any) string {
	// 延迟到运行时：避免在模板函数里直接依赖 encoding/json 的 panic 风险
	b, err := jsonMarshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

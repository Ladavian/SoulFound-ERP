package web

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"icewine-erp/internal/assets"
)

// pageFiles 列出所有页面模板（相对 templates/pages，去掉 .html）。
func pageFiles(t *testing.T) []string {
	t.Helper()
	var pages []string
	err := fs.WalkDir(assets.FS, "templates/pages", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".html") {
			return nil
		}
		rel := strings.TrimPrefix(p, "templates/pages/")
		pages = append(pages, strings.TrimSuffix(rel, ".html"))
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	return pages
}

// TestTemplatesParse 确保每个页面模板都能解析，并且提供了 content 或 layout 块。
func TestTemplatesParse(t *testing.T) {
	renderer := NewRenderer("internal/assets", false, "C$")
	pages := pageFiles(t)
	if len(pages) == 0 {
		t.Fatal("没有找到任何页面模板")
	}
	for _, page := range pages {
		tpl, err := renderer.parse(page)
		if err != nil {
			t.Errorf("页面 %s 解析失败: %v", page, err)
			continue
		}
		if tpl.Lookup("content") == nil && tpl.Lookup("layout") == nil {
			t.Errorf("页面 %s 既没有定义 content 也没有定义 layout", page)
		}
		if page != "login" && tpl.Lookup("layout") == nil && tpl.Lookup("content") != nil {
			// 依赖 base.html 提供的 layout
			if tpl.Lookup("base") == nil && tpl.Lookup("layout") == nil {
				t.Errorf("页面 %s 找不到 layout 定义", page)
			}
		}
	}
}

// TestNavPagesExist 保证所有路由用到的页面模板都存在。
func TestNavPagesExist(t *testing.T) {
	expected := []string{
		"login", "dashboard", "error", "profile", "settings", "logs", "more",
		"products/list", "products/form", "products/detail", "products/labels",
		"partners/list", "partners/form",
		"purchases/list", "purchases/form", "purchases/detail",
		"inventory/index", "inventory/movements", "inventory/adjust",
		"markets/list", "markets/form", "markets/detail", "markets/pos",
		"reports/markets", "reports/products", "reports/inventory",
		"users/list", "users/form", "users/password",
		"grouporders/list", "grouporders/form", "grouporders/detail",
	}
	found := map[string]bool{}
	for _, p := range pageFiles(t) {
		found[path.Clean(p)] = true
	}
	for _, want := range expected {
		if !found[want] {
			t.Errorf("缺少页面模板: templates/pages/%s.html", want)
		}
	}
}

// TestMobileConventions 把「手机端适配约定」固化成检查。
//
// 背景：这些约定以前是靠人一个个页面去改，漏了就只在手机上表现为
// "排版错乱/要横向滑动"。这里改成对全部模板做静态检查，
// 以后新加表格或筛选栏时漏了适配，CI 会直接失败。
func TestMobileConventions(t *testing.T) {
	root := "templates"
	if _, err := os.Stat(root); err != nil {
		root = "../assets/templates"
	}

	var (
		tableFiles  int
		filterFiles int
	)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		rel := strings.TrimPrefix(path, root+string(filepath.Separator))

		// 1) 数据表格必须支持窄屏转卡片
		for _, m := range regexp.MustCompile(`<table class="([^"]*)"`).FindAllStringSubmatch(src, -1) {
			classes := m[1]
			if strings.Contains(classes, "entry-table") {
				continue // 录入表有自己的手机布局
			}
			if !strings.Contains(classes, "data") {
				continue
			}
			tableFiles++
			if !strings.Contains(classes, "data--stack") {
				line := strings.Count(src[:strings.Index(src, m[0])], "\n") + 1
				t.Errorf("%s:%d 表格缺少 data--stack，手机上要横向滑动才能看全", rel, line)
			}
		}

		// 2) 筛选栏在手机上必须可折叠
		if strings.Contains(src, `class="filters"`) {
			filterFiles++
			for _, need := range []string{"filters__toggle", "filters__submit", `class="filters__more"`} {
				if !strings.Contains(src, need) {
					t.Errorf("%s 的筛选栏缺少 %s，手机上会挤成一团", rel, need)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	if tableFiles == 0 || filterFiles == 0 {
		t.Fatalf("检查范围异常：表格 %d 个、筛选栏 %d 个", tableFiles, filterFiles)
	}
	t.Logf("已检查 %d 张数据表格、%d 个筛选栏", tableFiles, filterFiles)
}

// alpineScopeCheck 静态分析模板里的 Alpine 作用域问题。
//
// 用户遇到过"活动费用的添加按钮点了没反应"：状态 x-data="{extra:0}" 写在
// <table> 上，而按钮在表格外面的 card__foot 里，extra 不在作用域，
// 点击等于空操作。这类问题路由体检查不出来，只能看 DOM 嵌套关系。
//
// 做法：把模板当 HTML 粗解析（维护一个标签栈），对每个带
// @click/@input/@change 的元素，往上找祖先里有没有定义它用到的变量。
var jsNoise = map[string]bool{
	"true": true, "false": true, "null": true, "undefined": true, "this": true,
	"if": true, "else": true, "return": true, "function": true, "var": true,
	"let": true, "const": true, "new": true, "typeof": true, "in": true, "of": true,
	"document": true, "window": true, "console": true, "Math": true, "Number": true,
	"String": true, "Boolean": true, "Array": true, "Object": true, "JSON": true,
	"parseInt": true, "parseFloat": true, "isNaN": true, "alert": true, "Date": true,
}

var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
	"img": true, "input": true, "link": true, "meta": true, "param": true,
	"source": true, "track": true, "wbr": true,
	// 内联 SVG 里的自闭合元素
	"path": true, "circle": true, "rect": true, "line": true, "polyline": true,
	"polygon": true, "ellipse": true, "use": true, "stop": true,
}

// alpineExprIdents 取出表达式里用到的根标识符（跳过属性名与关键字）。
//
// 先剥掉字符串字面量与 Go 模板片段：前者是数据不是变量，
// 后者由服务端渲染展开（例如 {{range}} 展开出的参数），静态看不到。
func alpineExprIdents(expr string) []string {
	expr = regexp.MustCompile(`\{\{[^}]*\}\}`).ReplaceAllString(expr, " ")
	expr = regexp.MustCompile(`'(?:[^'\\]|\\.)*'`).ReplaceAllString(expr, " ")
	expr = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`).ReplaceAllString(expr, " ")
	out := []string{}
	for _, m := range regexp.MustCompile(`(\.?)([A-Za-z_$][A-Za-z0-9_$]*)`).FindAllStringSubmatch(expr, -1) {
		if m[1] == "." {
			continue // 属性访问，如 row.productId 里的 productId
		}
		name := m[2]
		if jsNoise[name] || strings.HasPrefix(name, "$") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// alpineDataKeys 解析 x-data="{ a: 1, more: false, ... }" 里的变量名。
func alpineDataKeys(expr string) []string {
	out := []string{}
	for _, m := range regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*:`).FindAllStringSubmatch(expr, -1) {
		out = append(out, m[1])
	}
	// x-data="someComponent({...})" 里的方法名由 JS 提供，这里不做静态校验
	return out
}

func TestAlpineClickHandlersInScope(t *testing.T) {
	root := "templates"
	if _, err := os.Stat(root); err != nil {
		root = "../assets/templates"
	}

	// 元素标签，支持属性里带引号与 > 的情况
	tagRe := regexp.MustCompile(`<(\/?)([a-zA-Z][a-zA-Z0-9]*)((?:"[^"]*"|'[^']*'|[^>"'])*)>`)
	attrRe := regexp.MustCompile(`(@click|@input|@change|@submit)\s*=\s*"([^"]*)"`)
	dataRe := regexp.MustCompile(`x-data\s*=\s*"([^"]*)"`)

	checkedFiles, checkedHandlers := 0, 0

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)
		rel := strings.TrimPrefix(path, root+string(filepath.Separator))
		if !strings.Contains(src, "@click") && !strings.Contains(src, "@input") &&
			!strings.Contains(src, "@change") {
			return nil
		}
		checkedFiles++

		type scope struct {
			keys []string
			tag  string
		}
		stack := []scope{}
		// componentScope 记录由 x-data="component({...})" 声明的整块作用域：
		// 这种作用域里的方法由 JS 提供，不做静态校验，直接放行。
		componentDepth := []int{}

		for _, m := range tagRe.FindAllStringSubmatchIndex(src, -1) {
			closing := src[m[2]:m[3]] == "/"
			tag := strings.ToLower(src[m[4]:m[5]])
			attrs := src[m[6]:m[7]]
			line := strings.Count(src[:m[0]], "\n") + 1

			if closing {
				if len(stack) > 0 {
					stack = stack[:len(stack)-1]
				}
				if len(componentDepth) > 0 && componentDepth[len(componentDepth)-1] > len(stack) {
					componentDepth = componentDepth[:len(componentDepth)-1]
				}
				continue
			}

			// 收集本元素提供的作用域
			if dm := dataRe.FindStringSubmatch(attrs); dm != nil {
				// 组件式 x-data（如 permEditor({...})）：方法由 JS 提供，
				// 静态看不到，整块跳过校验。
				isComponent := strings.Contains(dm[1], "(")
				keys := alpineDataKeys(dm[1])
				if isComponent {
					keys = nil
					componentDepth = append(componentDepth, len(stack))
				}
				stack = append(stack, scope{keys: keys, tag: tag})
			} else if !voidTags[tag] && !strings.HasSuffix(attrs, "/") {
				stack = append(stack, scope{tag: tag})
			} else if voidTags[tag] || strings.HasSuffix(attrs, "/") {
				// 自闭合元素：处理完属性就结束，不压栈
			}

			// 检查事件处理里用到的标识符是否在祖先作用域里定义过
			for _, hm := range attrRe.FindAllStringSubmatch(attrs, -1) {
				checkedHandlers++
				inComponent := len(componentDepth) > 0 &&
					componentDepth[len(componentDepth)-1] < len(stack)
				if inComponent {
					continue
				}
				for _, ident := range alpineExprIdents(hm[2]) {
					found := false
					for _, sc := range stack {
						for _, k := range sc.keys {
							if k == ident {
								found = true
								break
							}
						}
						if found {
							break
						}
					}
					if !found {
						t.Errorf("%s:%d 的 %s 用到了 %q，但它的祖先里没有定义（点击会没反应）",
							rel, line, hm[1], ident)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	if checkedFiles == 0 || checkedHandlers == 0 {
		t.Fatalf("检查范围异常：文件 %d、事件处理 %d", checkedFiles, checkedHandlers)
	}
	t.Logf("已检查 %d 个模板里的 %d 处 Alpine 事件处理", checkedFiles, checkedHandlers)
}

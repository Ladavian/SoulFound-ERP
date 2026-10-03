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

// TestNoNestedFormsAndMultipart 静态检查表单的两个致命写法。
//
// 用户反馈"插入产品图片无法保存"，根因是图片上传表单被嵌套在
// 产品表单里面：HTML 不允许表单嵌套，浏览器会直接忽略内层 <form>，
// 于是点"上传图片"实际提交的是外层表单（且外层没有 multipart，文件根本没发出去）。
// 这类问题肉眼很难发现，所以固定成检查：
//  1. 不允许 form 嵌套
//  2. 带 <input type="file"> 的表单必须有 enctype="multipart/form-data"
func TestNoNestedFormsAndMultipart(t *testing.T) {
	root := "templates"
	if _, err := os.Stat(root); err != nil {
		root = "../assets/templates"
	}

	tagRe := regexp.MustCompile(`(?is)<(/?)([a-z][a-z0-9]*)((?:"[^"]*"|'[^']*'|[^>"'])*)>`)
	voidTags := map[string]bool{
		"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
		"img": true, "input": true, "link": true, "meta": true, "param": true,
		"source": true, "track": true, "wbr": true, "path": true, "circle": true,
		"rect": true, "line": true, "polyline": true, "polygon": true, "ellipse": true,
		"use": true, "stop": true,
	}
	fileRe := regexp.MustCompile(`(?i)type\s*=\s*"file"`)
	enctypeRe := regexp.MustCompile(`(?i)enctype\s*=\s*"multipart/form-data"`)

	checked := 0
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

		type frame struct {
			tag   string
			line  int
			attrs string
		}
		stack := []frame{}
		lineOf := func(idx int) int { return strings.Count(src[:idx], "\n") + 1 }

		for _, m := range tagRe.FindAllStringSubmatchIndex(src, -1) {
			closing := src[m[2]:m[3]] == "/"
			name := strings.ToLower(src[m[4]:m[5]])
			attrs := src[m[6]:m[7]]
			line := lineOf(m[0])

			if closing {
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i].tag == name {
						stack = stack[:i]
						break
					}
				}
				continue
			}
			if voidTags[name] || strings.HasSuffix(strings.TrimSpace(attrs), "/") {
				continue
			}

			if name == "form" {
				checked++
				// 1) 不允许嵌套
				for _, f := range stack {
					if f.tag == "form" {
						t.Errorf("%s:%d 出现嵌套 <form>（外层在第 %d 行）："+
							"浏览器会忽略内层 form，里面按钮提交的是外层表单",
							rel, line, f.line)
						break
					}
				}
				// 2) 表单内有文件输入就必须是 multipart
				end := strings.Index(strings.ToLower(src[m[0]:]), "</form>")
				body := ""
				if end >= 0 {
					body = src[m[0] : m[0]+end]
				}
				if fileRe.MatchString(body) && !enctypeRe.MatchString(attrs) {
					t.Errorf("%s:%d 表单里有文件上传，但缺少 enctype=\"multipart/form-data\"，"+
						"文件不会被提交", rel, line)
				}
			}
			stack = append(stack, frame{tag: name, line: line, attrs: attrs})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	if checked == 0 {
		t.Fatal("没有检查到任何表单，检查范围异常")
	}
	t.Logf("已检查 %d 个表单的嵌套与 multipart 设置", checked)
}

// TestAlpineTemplatesHaveSingleRoot Alpine 的 x-for / x-if 模板只能有一个根元素。
//
// 用户反馈"新建团单填完信息却提示请至少填写一行产品明细"：
// 明细的隐藏提交字段写成了 7 个并列的 <input> 放在同一个 <template x-for> 里，
// 而 Alpine 只克隆第一个根元素，于是只有 product_id 被提交，
// product_name 等全部丢失，服务端看不到明细就报错。
// 这个坑不看文档很难知道，所以固定成检查。
func TestAlpineTemplatesHaveSingleRoot(t *testing.T) {
	root := "templates"
	if _, err := os.Stat(root); err != nil {
		root = "../assets/templates"
	}

	templateRe := regexp.MustCompile(`(?is)<template\b([^>]*)>(.*?)</template>`)
	directiveRe := regexp.MustCompile(`(?i)x-(for|if)\s*=`)
	tagRe := regexp.MustCompile(`(?is)<(/?)([a-z][a-z0-9]*)((?:"[^"]*"|'[^']*'|[^>"'])*)>`)
	voidTags := map[string]bool{
		"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true,
		"img": true, "input": true, "link": true, "meta": true, "param": true,
		"source": true, "track": true, "wbr": true, "path": true, "circle": true,
		"rect": true, "line": true, "polyline": true, "polygon": true, "ellipse": true,
		"use": true, "stop": true,
	}

	checked := 0
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

		for _, m := range templateRe.FindAllStringSubmatchIndex(src, -1) {
			attrs := src[m[2]:m[3]]
			if !directiveRe.MatchString(attrs) {
				continue
			}
			line := strings.Count(src[:m[0]], "\n") + 1
			body := src[m[4]:m[5]]
			checked++

			// 数模板里的顶层元素个数
			depth, roots := 0, 0
			for _, t := range tagRe.FindAllStringSubmatch(body, -1) {
				closing, name, a := t[1] == "/", strings.ToLower(t[2]), t[3]
				if voidTags[name] || strings.HasSuffix(strings.TrimSpace(a), "/") {
					if depth == 0 {
						roots++
					}
					continue
				}
				if closing {
					depth--
				} else {
					if depth == 0 {
						roots++
					}
					depth++
				}
			}
			if roots != 1 {
				t.Errorf("%s:%d 的 Alpine 模板有 %d 个根元素："+
					"Alpine 只会克隆第一个，其余内容不会渲染、表单字段也不会提交",
					rel, line, roots)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	if checked == 0 {
		t.Fatal("没有检查到任何 Alpine 模板，检查范围异常")
	}
	t.Logf("已检查 %d 个 Alpine 模板的根元素数量", checked)
}

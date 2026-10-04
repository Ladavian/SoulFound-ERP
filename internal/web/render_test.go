package web

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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

// TestTableLayoutContract 表格首列的排版契约（PC 与手机都要成立）。
//
// 用户反馈市集列表的"市集"列被挤成一列竖排的字——列多的表格
// 会把没有宽度下限的首列压到只剩几个字符宽，中文只能逐字换行。
// 修法是给首列一个最小宽度，并在手机卡片模式下复位。复位规则
// 必须与桌面规则**同等具体**，否则优先级不够根本不会生效。
func TestTableLayoutContract(t *testing.T) {
	raw, err := os.ReadFile("../assets/static/css/app.css")
	if err != nil {
		t.Fatalf("读取样式失败: %v", err)
	}
	src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(raw), "")

	// 解析 CSS：需要识别媒体查询的嵌套，简单正则做不到
	type cssRule struct {
		selector string
		body     string
		medias   []string
	}
	var rules []cssRule
	var mediaStack []string
	var pending *cssRule
	var buf strings.Builder

	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '{':
			head := strings.TrimSpace(buf.String())
			buf.Reset()
			// @media / @container / @supports 这类"里面还能放规则"的块都要记下来，
			// 否则容器查询里的规则会被当成顶层规则
			if strings.HasPrefix(head, "@") &&
				!strings.HasPrefix(head, "@font-face") &&
				!strings.HasPrefix(head, "@import") {
				mediaStack = append(mediaStack, head)
				continue
			}
			pending = &cssRule{selector: head, medias: append([]string{}, mediaStack...)}
		case '}':
			if pending != nil {
				pending.body = buf.String()
				rules = append(rules, *pending)
				pending = nil
			} else if len(mediaStack) > 0 {
				mediaStack = mediaStack[:len(mediaStack)-1]
			}
			buf.Reset()
		default:
			buf.WriteByte(src[i])
		}
	}

	// 「窄容器」既包括 max-width 媒体查询（手机视口），
	// 也包括 @container（侧栏面板这类窄容器）
	isMobile := func(r cssRule) bool {
		for _, m := range r.medias {
			if strings.Contains(m, "max-width") {
				return true
			}
		}
		return false
	}
	// specificity 计算类/属性/伪类个数与元素个数（比较用，够精确）
	specificity := func(sel string) (int, int) {
		classes := len(regexp.MustCompile(`\.[a-zA-Z_-][\w-]*`).FindAllString(sel, -1)) +
			len(regexp.MustCompile(`\[[^\]]*\]`).FindAllString(sel, -1)) +
			len(regexp.MustCompile(`:[a-z-]+`).FindAllString(sel, -1))
		elems := len(regexp.MustCompile(`(^|[\s>+~])[a-zA-Z][\w-]*`).FindAllString(sel, -1))
		return classes, elems
	}

	const target = "table.data td.row-cell--main"
	var desktop, mobile []cssRule
	for _, r := range rules {
		if strings.Join(strings.Fields(r.selector), " ") != target {
			continue
		}
		if isMobile(r) {
			mobile = append(mobile, r)
		} else {
			desktop = append(desktop, r)
		}
	}
	if len(desktop) == 0 || !strings.Contains(desktop[0].body, "min-width") {
		t.Fatal("首列缺少最小宽度：列多的表格会把首列挤成逐字竖排")
	}
	if len(mobile) == 0 || !strings.Contains(mobile[len(mobile)-1].body, "min-width: 0") {
		t.Fatal("窄容器（卡片模式）下没有复位首列最小宽度")
	}
	dc, de := specificity(desktop[0].selector)
	mc, me := specificity(mobile[len(mobile)-1].selector)
	if mc < dc || (mc == dc && me < de) {
		t.Errorf("手机复位规则优先级不足（%d 类 %d 元素 vs %d 类 %d 元素），不会生效",
			mc, me, dc, de)
	}

	// 宽表格必须能横向滚动，而不是把内容压扁
	if !regexp.MustCompile(`\.table-wrap\s*\{[^}]*overflow-x`).MatchString(src) {
		t.Error(".table-wrap 需要 overflow-x，列多时应当横向滚动")
	}
	t.Logf("已解析 %d 条 CSS 规则", len(rules))
}

// TestCSSIntegrity 样式表结构完整性。
//
// 背景：修手机端样式时把一条规则的选择器整行替换掉了，结果多出一个 "}"，
// 媒体查询提前闭合，本该只在手机端生效的规则（例如
// .data--stack .mobile-hide { display: none }）跑到了全局，
// 桌面端表格表体少了 4 列、整行左移，数值显示在错误的表头下面。
// 这种错误浏览器不会报错、页面看起来也"有内容"，只能靠结构检查。
func TestCSSIntegrity(t *testing.T) {
	raw, err := os.ReadFile("../assets/static/css/app.css")
	if err != nil {
		t.Fatalf("读取样式失败: %v", err)
	}
	src := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(raw), "")

	type block struct {
		head   string
		atRule bool
		line   int
		decls  []string // 该块内出现的声明片段（用于报错定位）
	}
	var stack []block
	var buf strings.Builder
	lineOf := func(pos int) int { return strings.Count(src[:pos], "\n") + 1 }

	for i := 0; i < len(src); i++ {
		switch src[i] {
		case '{':
			head := strings.TrimSpace(buf.String())
			buf.Reset()
			if head == "" {
				t.Errorf("样式表第 %d 行出现空的规则头（多余的 { ）", lineOf(i))
			}
			stack = append(stack, block{
				head:   head,
				atRule: strings.HasPrefix(head, "@"),
				line:   lineOf(i),
			})
		case '}':
			if len(stack) == 0 {
				t.Errorf("样式表第 %d 行出现多余的 }", lineOf(i))
				buf.Reset()
				continue
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			body := strings.TrimSpace(buf.String())
			// 关闭一个 @ 块（媒体查询等）时，若还残留声明，
			// 说明这些声明写在了规则外面——通常是漏写选择器。
			if top.atRule && body != "" {
				t.Errorf("样式表第 %d 行的 @ 块（%s，起始第 %d 行）闭合前残留了规则外声明："+
					"多半是选择器被误删，会导致后面的规则跑到媒体查询外面去\n    残留内容: %s",
					lineOf(i), top.head, top.line, firstLine(body))
			}
			buf.Reset()
		default:
			buf.WriteByte(src[i])
		}
	}
	if len(stack) != 0 {
		t.Errorf("样式表有 %d 个未闭合的块，第一个是第 %d 行的 %q",
			len(stack), stack[0].line, stack[0].head)
	}
	if tail := strings.TrimSpace(buf.String()); tail != "" {
		t.Errorf("样式表结尾有规则外内容: %s", firstLine(tail))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 90 {
		s = s[:90] + "…"
	}
	return strings.TrimSpace(s)
}

// TestStackTablesInsideWrap 所有 data--stack 表格都必须包在 .table-wrap 里。
//
// 表格改成"按容器宽度自适应"之后，卡片模式由
// @container tablewrap (max-width: 720px) 驱动——前提是表格真的在
// .table-wrap 这个查询容器里面。漏包一层，那张表在手机上就永远变不成卡片，
// 也没有任何报错，只会一直挤着。所以固定成检查。
func TestStackTablesInsideWrap(t *testing.T) {
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
			attrs string
			line  int
		}
		stack := []frame{}
		for _, m := range tagRe.FindAllStringSubmatchIndex(src, -1) {
			closing := src[m[2]:m[3]] == "/"
			name := strings.ToLower(src[m[4]:m[5]])
			attrs := src[m[6]:m[7]]
			line := strings.Count(src[:m[0]], "\n") + 1

			if closing {
				for i := len(stack) - 1; i >= 0; i-- {
					if stack[i].tag == name {
						stack = stack[:i]
						break
					}
				}
				continue
			}
			if name == "table" && strings.Contains(attrs, "data--stack") {
				checked++
				wrapped := false
				for _, f := range stack {
					if f.tag == "div" && strings.Contains(f.attrs, "table-wrap") {
						wrapped = true
						break
					}
				}
				if !wrapped {
					t.Errorf("%s:%d 的 data--stack 表格没有包在 .table-wrap 里："+
						"容器查询不会生效，手机上这张表不会变成卡片",
						rel, line)
				}
			}
			if voidTags[name] || strings.HasSuffix(strings.TrimSpace(attrs), "/") {
				continue
			}
			stack = append(stack, frame{tag: name, attrs: attrs, line: line})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	if checked < 15 {
		t.Fatalf("只检查到 %d 张 data--stack 表格，范围异常", checked)
	}
	t.Logf("已检查 %d 张自适应表格的容器包裹", checked)
}

// TestStackTableCellsHaveLabels 卡片模式下每个字段都要有标签。
//
// 手机上表格会变成卡片，字段名来自 td 的 data-label。
// 漏一个 data-label，卡片上就只剩一个孤零零的数字，看不出是什么。
// 表头/表尾在卡片模式下会隐藏，所以只检查表体。
func TestStackTableCellsHaveLabels(t *testing.T) {
	root := "templates"
	if _, err := os.Stat(root); err != nil {
		root = "../assets/templates"
	}
	tableRe := regexp.MustCompile(`(?s)<table class="([^"]*data--stack[^"]*)"(.*?)</table>`)
	tbodyRe := regexp.MustCompile(`(?s)<tbody>(.*?)</tbody>`)
	tdRe := regexp.MustCompile(`<td([^>]*)>`)

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
		if !strings.Contains(src, "data--stack") {
			return nil
		}
		rel := strings.TrimPrefix(path, root+string(filepath.Separator))
		for _, tm := range tableRe.FindAllStringSubmatchIndex(src, -1) {
			body := src[tm[4]:tm[5]]
			tb := tbodyRe.FindStringSubmatchIndex(body)
			if tb == nil {
				continue
			}
			for _, td := range tdRe.FindAllStringSubmatchIndex(body[tb[2]:tb[3]], -1) {
				attrs := body[tb[2]:tb[3]][td[2]:td[3]]
				checked++
				if strings.Contains(attrs, "data-label") {
					continue
				}
				// 首列（卡片标题）与操作列不需要标签
				if strings.Contains(attrs, "row-cell--main") ||
					strings.Contains(attrs, `class="right`) ||
					strings.Contains(attrs, "actions-col") {
					continue
				}
				line := strings.Count(src[:tm[4]+tb[2]+td[0]], "\n") + 1
				t.Errorf("%s:%d 的单元格没有 data-label：卡片模式下会只剩一个没有说明的值",
					rel, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	if checked < 60 {
		t.Fatalf("只检查到 %d 个单元格，范围异常", checked)
	}
	t.Logf("已检查 %d 个卡片字段的标签", checked)
}

// TestStackCardGridHasNoHoles 手机卡片的网格不能留空位。
//
// 卡片是两列网格，每个字段占一格。字段数是奇数、又没有整行字段时，
// 最后一行就会空一格：用户看到的是"第 5 格塞了一堆数据、第 6 格空着"。
// 修法有两种：拆成偶数个字段，或让末尾字段占整行（.span-2）。
// 这里按实际排版顺序模拟一遍网格填充，任何中间或末尾的空位都算问题。
func TestStackCardGridHasNoHoles(t *testing.T) {
	root := "templates"
	if _, err := os.Stat(root); err != nil {
		root = "../assets/templates"
	}
	tableRe := regexp.MustCompile(`(?s)<table class="([^"]*data--stack[^"]*)"(.*?)</table>`)
	tbodyRe := regexp.MustCompile(`(?s)<tbody>(.*?)</tbody>`)
	trRe := regexp.MustCompile(`(?s)<tr>(.*?)</tr>`)
	tdRe := regexp.MustCompile(`<td([^>]*)>`)

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
		if !strings.Contains(src, "data--stack") {
			return nil
		}
		rel := strings.TrimPrefix(path, root+string(filepath.Separator))
		for _, tm := range tableRe.FindAllStringSubmatchIndex(src, -1) {
			body := src[tm[4]:tm[5]]
			tb := tbodyRe.FindStringSubmatchIndex(body)
			if tb == nil {
				continue
			}
			tbody := body[tb[2]:tb[3]]
			tr := trRe.FindStringSubmatchIndex(tbody)
			if tr == nil {
				continue
			}
			row := tbody[tr[2]:tr[3]]
			checked++

			// 按视觉顺序排列：标题(order -2) → 操作区(order -1) → 其余按原顺序
			type cell struct {
				order int
				full  bool
			}
			var cells []cell
			for _, td := range tdRe.FindAllStringSubmatchIndex(row, -1) {
				attrs := row[td[2]:td[3]]
				full := strings.Contains(attrs, "span-2") ||
					strings.Contains(attrs, "row-cell--main") ||
					strings.Contains(attrs, `class="right`) ||
					strings.Contains(attrs, "actions-col")
				order := 0
				if strings.Contains(attrs, "row-cell--main") {
					order = -2
				} else if strings.Contains(attrs, `class="right`) || strings.Contains(attrs, "actions-col") {
					order = -1
				}
				cells = append(cells, cell{order: order, full: full})
			}
			sort.SliceStable(cells, func(i, j int) bool { return cells[i].order < cells[j].order })

			pos := 0 // 0 = 行首，1 = 已占左列
			holes := 0
			for _, c := range cells {
				if c.full {
					if pos != 0 {
						holes++ // 整行字段前的空位
					}
					pos = 0
					continue
				}
				pos = 1 - pos
			}
			if pos != 0 {
				holes++ // 末尾空一格
			}
			if holes > 0 {
				line := strings.Count(src[:tm[4]+tb[2]+tr[0]], "\n") + 1
				t.Errorf("%s:%d 的卡片网格有 %d 处空位："+
					"字段数是奇数时，末尾字段应加 .span-2 占整行（或把字段拆成偶数个）",
					rel, line, holes)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历模板失败: %v", err)
	}
	if checked < 15 {
		t.Fatalf("只检查到 %d 张卡片，范围异常", checked)
	}
	t.Logf("已检查 %d 张卡片的网格排布", checked)
}

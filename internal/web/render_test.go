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

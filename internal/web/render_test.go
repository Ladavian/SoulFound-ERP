package web

import (
	"io/fs"
	"path"
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

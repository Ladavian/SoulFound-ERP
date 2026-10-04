package web

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"icewine-erp/internal/config"
	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
)

// testApp 启动一套完整环境（临时数据库 + 演示数据 + HTTP 处理器）。
func testApp(t *testing.T) (http.Handler, *service.Service, *config.Config) {
	t.Helper()
	srv, svc, cfg := newTestServer(t)
	return srv.Handler(), svc, cfg
}

// newTestServer 与 testApp 相同，但返回 *Server，
// 便于测试直接访问路由表（例如体检按钮目标是否真的注册过）。
func newTestServer(t *testing.T) (*Server, *service.Service, *config.Config) {
	t.Helper()

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.sqlite3"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	cfg := &config.Config{
		AppName:        "SoulFound ERP 测试",
		Version:        "test",
		Addr:           ":0",
		Dev:            false, // 使用 embed 的模板
		DataDir:        dir,
		DBPath:         filepath.Join(dir, "test.sqlite3"),
		WebDir:         "internal/assets",
		BackupDir:      filepath.Join(dir, "backups"),
		SecretKey:      []byte("test-secret-key-for-session-signing"),
		SessionCookie:  "erp_test_session",
		SessionTTL:     time.Hour,
		CookieSecure:   false,
		Currency:       "CNY",
		CurrencySymbol: "¥",
		DefaultLowQty:  model.QtyFromInt(6),
		AdminUsername:  "admin",
		AdminPassword:  "admin123",
		AdminName:      "测试管理员",
		SeedDemo:       true,
		Location:       time.Local,
	}

	svc := service.New(st, cfg)
	if err := svc.Bootstrap(ctx); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	srv, err := New(cfg, svc)
	if err != nil {
		t.Fatalf("初始化 HTTP 服务失败: %v", err)
	}
	return srv, svc, cfg
}

func doLogin(t *testing.T, h http.Handler, cfg *config.Config, username, password string) *http.Cookie {
	t.Helper()
	form := url.Values{"username": {username}, "password": {password}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	res := rec.Result()
	for _, c := range res.Cookies() {
		if c.Name == cfg.SessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatalf("登录失败：status=%d body=%s", res.StatusCode, firstChars(rec.Body.String(), 400))
	return nil
}

func get(t *testing.T, h http.Handler, path string, cookies ...*http.Cookie) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func post(t *testing.T, h http.Handler, path string, form url.Values, cookies ...*http.Cookie) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func postHTMX(t *testing.T, h http.Handler, path string, form url.Values, cookies ...*http.Cookie) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// TestAllPagesRender 以管理员身份访问每一个页面，确保模板能真正渲染出内容。
func TestAllPagesRender(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()

	products, _ := svc.Store.ListProducts(ctx, store.ProductFilter{})
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{})
	purchases, _ := svc.Store.ListPurchases(ctx, store.PurchaseFilter{})
	suppliers, _ := svc.Store.ListSuppliers(ctx, "", false)
	users, _ := svc.Store.ListUsers(ctx)
	if len(products) == 0 || len(markets) == 0 || len(purchases) == 0 {
		t.Fatal("演示数据未写入，测试前提不成立")
	}

	// 每条断言都包含 base.html 渲染的页面标题（由 handler 决定，最稳定），
	// 再配合一到两个内容标记验证数据真的渲染出来了。
	type pageCase struct {
		path    string
		markers []string
	}
	cases := []pageCase{
		{"/", []string{"经营看板", "库存总值", "快捷操作"}},
		{"/products", []string{"产品档案", products[0].SKU}},
		{"/products/new", []string{"产品档案", "产品编码", "建议售价"}},
		{"/products/" + strconv.FormatInt(products[0].ID, 10), []string{products[0].Name, "库存流水"}},
		{"/products/" + strconv.FormatInt(products[0].ID, 10) + "/edit", []string{"产品档案", products[0].SKU}},
		{"/suppliers", []string{"供应商管理", suppliers[0].Name}},
		{"/suppliers/new", []string{"供应商管理", "名称"}},
		{"/customers", []string{"客户管理"}},
		{"/purchases", []string{"采购入库", purchases[0].Code}},
		{"/purchases/new", []string{"采购入库", "附加费用"}},
		{"/purchases/" + strconv.FormatInt(purchases[0].ID, 10), []string{purchases[0].Code, "到岸"}},
		{"/inventory", []string{"库存查询", products[0].SKU}},
		{"/inventory/movements", []string{"库存流水", "结存"}},
		{"/inventory/adjust", []string{"出入库登记", "期初建账"}},
		{"/markets", []string{"市集活动", markets[0].Name}},
		{"/markets/new", []string{"市集活动", "市集名称"}},
		{"/markets/" + strconv.FormatInt(markets[0].ID, 10), []string{markets[0].Name, "本场损益"}},
		{"/markets/" + strconv.FormatInt(markets[0].ID, 10) + "/edit", []string{"市集活动", "结束日期"}},
		{"/reports/markets", []string{"市集利润报表", "净利润"}},
		{"/reports/products", []string{"产品销售报表", "销售额"}},
		{"/reports/inventory", []string{"库存报表"}},
		{"/users", []string{"用户管理", "admin"}},
		{"/users/new", []string{"用户管理", "用户名"}},
		{"/users/" + strconv.FormatInt(users[0].ID, 10) + "/edit", []string{"用户管理"}},
		{"/users/" + strconv.FormatInt(users[0].ID, 10) + "/password", []string{"重置密码"}},
		{"/settings", []string{"系统设置"}},
		{"/logs", []string{"操作日志"}},
		{"/profile", []string{"我的账号"}},
		{"/more", []string{"全部功能", "市集活动", "系统设置"}},
		{"/healthz", []string{"ok"}},
	}

	for _, c := range cases {
		code, body := get(t, h, c.path, cookie)
		if code != http.StatusOK {
			t.Errorf("GET %s 返回 %d，期望 200；响应片段：%s", c.path, code, firstChars(body, 500))
			continue
		}
		for _, marker := range c.markers {
			if !strings.Contains(body, marker) {
				t.Errorf("GET %s 的页面里没找到 %q", c.path, marker)
			}
		}
		if strings.Contains(body, "服务器内部错误") || strings.Contains(body, "无权访问") {
			t.Errorf("GET %s 渲染成了错误页：%s", c.path, firstChars(body, 300))
		}
	}
}

// TestNotFoundAndUnauthorized 检查兜底行为。
func TestNotFoundAndUnauthorized(t *testing.T) {
	h, _, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 已登录时未知路径应给出 404 页面
	code, body := get(t, h, "/no-such-page", cookie)
	if code != http.StatusNotFound {
		t.Errorf("已登录访问未知路径应返回 404，实际 %d", code)
	}
	if !strings.Contains(body, "页面不存在") {
		t.Error("404 页面应提示页面不存在")
	}

	// 未登录访问首页应跳转到登录页
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/login") {
		t.Errorf("未登录访问首页应 303 到 /login，实际 %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// 未登录访问未知路径也先去登录
	req = httptest.NewRequest(http.MethodGet, "/no-such-page", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("未登录访问未知路径应 303 到 /login，实际 %d", rec.Code)
	}

	// 登录页本身应该能打开
	code, body = get(t, h, "/login")
	if code != http.StatusOK || !strings.Contains(body, "登录") {
		t.Errorf("登录页异常：%d", code)
	}

	// 错误密码
	form := url.Values{"username": {"admin"}, "password": {"wrong-password"}}
	code, body = post(t, h, "/login", form)
	if code != http.StatusOK || !strings.Contains(body, "用户名或密码错误") {
		t.Errorf("错误密码应提示错误，实际 %d", code)
	}
	_ = cfg
}

// TestStaffCannotSeeCostsOrSettle 验证角色权限真的生效。
func TestStaffCannotSeeCostsOrSettle(t *testing.T) {
	h, svc, cfg := testApp(t)
	admin := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()

	if _, err := svc.CreateUser(ctx, "staff1", "staff123", "店员小王", model.RoleStaff, nil, mustUser(t, svc, "admin")); err != nil {
		t.Fatalf("创建店员失败: %v", err)
	}
	staff := doLogin(t, h, cfg, "staff1", "staff123")

	// 店员能看市集，但看不到成本与利润
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{})
	marketPath := "/markets/" + strconv.FormatInt(markets[0].ID, 10)
	code, body := get(t, h, marketPath, staff)
	if code != http.StatusOK {
		t.Fatalf("店员应能查看市集，实际 %d", code)
	}
	if !strings.Contains(body, "你的账号看不到成本与利润") {
		t.Error("店员页面应提示看不到成本利润")
	}
	if strings.Contains(body, "本场损益") {
		t.Error("店员不应看到损益明细")
	}

	// 店员访问利润报表应被拒绝
	code, _ = get(t, h, "/reports/markets", staff)
	if code != http.StatusForbidden {
		t.Errorf("店员访问利润报表应 403，实际 %d", code)
	}

	// 店员不能结算市集
	code, _ = post(t, h, marketPath+"/settle", url.Values{}, staff)
	if code != http.StatusForbidden {
		t.Errorf("店员结算应 403，实际 %d", code)
	}

	// 管理员可以访问利润报表
	if code, _ = get(t, h, "/reports/markets", admin); code != http.StatusOK {
		t.Errorf("管理员访问利润报表应 200，实际 %d", code)
	}

	// 派生成本与利润指标对店员必须隐藏。
	// 注意：采购单价、售价、销售额是店员自己录入/日常要用的，允许可见；
	// 这里检查的是系统派生出来的成本与利润口径。
	forbiddenWords := []string{"单位成本", "平均成本", "结存均价", "结存金额",
		"库存成本", "库存总值", "毛利", "净利润", "净利率", "成本单价"}
	staffPages := []string{"/", "/markets", "/products", "/products/1",
		"/inventory", "/inventory/movements", "/group-orders", "/purchases"}
	for _, path := range staffPages {
		_, body := get(t, h, path, staff)
		for _, forbidden := range forbiddenWords {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s 对店员不应出现「%s」", path, forbidden)
			}
		}
	}
	// 管理员应该能看到这些列
	_, body = get(t, h, "/inventory/movements", admin)
	if !strings.Contains(body, "单位成本") {
		t.Error("管理员应能看到「单位成本」列")
	}
	_, body = get(t, h, "/inventory", admin)
	if !strings.Contains(body, "平均成本") {
		t.Error("管理员应能看到「平均成本」列")
	}
}

// TestFullBusinessFlow 走一遍完整业务：建产品 → 采购入库 → 建市集 → 录入 → 结算。
func TestFullBusinessFlow(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()

	// 1. 新建产品
	code, _ := post(t, h, "/products/new", url.Values{
		"sku":              {"E2E-001"},
		"name":             {"端到端测试冰酒"},
		"category":         {"冰酒"},
		"vintage":          {"2022"},
		"volume_ml":        {"375"},
		"unit":             {"瓶"},
		"bottles_per_case": {"6"},
		"sale_price":       {"120.00"},
		"low_stock_qty":    {"4"},
		"is_active":        {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建产品应 303，实际 %d", code)
	}
	product, err := svc.Store.ProductBySKU(ctx, "E2E-001")
	if err != nil || product == nil {
		t.Fatalf("产品未创建成功: %v", err)
	}

	// 2. 采购入库：20 瓶 @ 50，运费 100（按数量分摊 => 到岸单价 55）
	code, _ = post(t, h, "/purchases/new", url.Values{
		"purchase_date": {"2025-06-01"},
		"alloc_method":  {"qty"},
		"shipping_cost": {"100"},
		"product_id":    {strconv.FormatInt(product.ID, 10)},
		"qty":           {"20"},
		"unit_price":    {"50"},
		"note":          {""},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建采购单应 303，实际 %d", code)
	}
	purchases, _ := svc.Store.ListPurchases(ctx, store.PurchaseFilter{Keyword: "E2E"})
	if len(purchases) == 0 {
		purchases, _ = svc.Store.ListPurchases(ctx, store.PurchaseFilter{Status: model.PurchaseDraft})
	}
	if len(purchases) == 0 {
		t.Fatal("采购单未创建")
	}
	purchaseID := purchases[0].ID

	code, _ = post(t, h, "/purchases/"+strconv.FormatInt(purchaseID, 10)+"/confirm", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("确认入库应 303，实际 %d", code)
	}
	product, _ = svc.Store.ProductByID(ctx, product.ID)
	if product.StockQty != model.MustQty("20") {
		t.Fatalf("入库后库存应为 20，实际 %s", product.StockQty)
	}
	if product.AvgCost != model.MustMoney("55") {
		t.Fatalf("到岸成本应为 55（50 + 100/20），实际 %s", product.AvgCost)
	}

	// 3. 新建市集
	code, _ = post(t, h, "/markets/new", url.Values{
		"name":       {"端到端测试市集"},
		"venue":      {"测试场地"},
		"city":       {"多伦多"},
		"start_date": {"2025-06-10"},
		"end_date":   {"2025-06-11"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建市集应 303，实际 %d", code)
	}
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "端到端"})
	if len(markets) == 0 {
		t.Fatal("市集未创建")
	}
	marketID := markets[0].ID

	// 4. 添加产品（HTMX 局部刷新）
	code, body := postHTMX(t, h, "/markets/"+strconv.FormatInt(marketID, 10)+"/items",
		url.Values{"product_id": {strconv.FormatInt(product.ID, 10)}}, cookie)
	if code != http.StatusOK {
		t.Fatalf("添加产品应 200，实际 %d", code)
	}
	if !strings.Contains(body, "端到端测试冰酒") {
		t.Error("添加产品后面板里应出现该产品")
	}

	market, err := svc.Store.MarketByID(ctx, marketID)
	if err != nil || market == nil {
		t.Fatalf("读取市集失败: %v", err)
	}
	if len(market.Items) != 1 {
		t.Fatalf("市集应有 1 行明细，实际 %d", len(market.Items))
	}
	itemID := market.Items[0].ID

	// 5. 录入试饮 2 瓶、销售 5 瓶，优惠 10 元
	code, body = postHTMX(t, h, "/markets/"+strconv.FormatInt(marketID, 10)+"/items/"+strconv.FormatInt(itemID, 10),
		url.Values{
			"carried_qty":  {"12"},
			"tasting_qty":  {"2"},
			"sold_qty":     {"5"},
			"gift_qty":     {"0"},
			"loss_qty":     {"0"},
			"unit_price":   {"120"},
			"discount_amt": {"10"},
		}, cookie)
	if code != http.StatusOK {
		t.Fatalf("更新明细应 200，实际 %d", code)
	}
	if !strings.Contains(body, "本场损益") {
		t.Error("局部刷新应返回损益面板")
	}

	// 6. 录入费用
	code, _ = postHTMX(t, h, "/markets/"+strconv.FormatInt(marketID, 10)+"/expenses",
		url.Values{"category": {"booth"}, "amount": {"80"}, "note": {"摊位费"}}, cookie)
	if code != http.StatusOK {
		t.Fatalf("保存费用应 200，实际 %d", code)
	}

	// 7. 数量超过带去数量时应被拒绝（保留输入并提示）
	code, body = postHTMX(t, h, "/markets/"+strconv.FormatInt(marketID, 10)+"/items/"+strconv.FormatInt(itemID, 10),
		url.Values{
			"carried_qty": {"3"}, "tasting_qty": {"2"}, "sold_qty": {"5"},
			"gift_qty": {"0"}, "loss_qty": {"0"}, "unit_price": {"120"}, "discount_amt": {"0"},
		}, cookie)
	if code != http.StatusOK || !strings.Contains(body, "超过了带去的数量") {
		t.Errorf("超量应提示错误，实际 %d", code)
	}

	// 8. 结算
	code, _ = post(t, h, "/markets/"+strconv.FormatInt(marketID, 10)+"/settle", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("结算应 303，实际 %d", code)
	}

	// 校验：库存 20 - 2 - 5 = 13；成本口径 = 5×120-10 - 7×55 - 80 = 590 - 385 - 80 = 125
	product, _ = svc.Store.ProductByID(ctx, product.ID)
	if product.StockQty != model.MustQty("13") {
		t.Errorf("结算后库存应为 13，实际 %s", product.StockQty)
	}
	market, _ = svc.Store.MarketByID(ctx, marketID)
	if market == nil || !market.IsSettled() {
		t.Fatal("市集应为已结算")
	}
	wantRevenue := model.MustMoney("590")
	wantProfit := model.MustMoney("125")
	if market.Revenue != wantRevenue {
		t.Errorf("销售额应为 %s，实际 %s", wantRevenue, market.Revenue)
	}
	if market.NetProfit != wantProfit {
		t.Errorf("净利润应为 %s，实际 %s", wantProfit, market.NetProfit)
	}

	// 9. 撤销结算后库存回滚
	code, _ = post(t, h, "/markets/"+strconv.FormatInt(marketID, 10)+"/unsettle", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("撤销结算应 303，实际 %d", code)
	}
	product, _ = svc.Store.ProductByID(ctx, product.ID)
	if product.StockQty != model.MustQty("20") {
		t.Errorf("撤销结算后库存应回到 20，实际 %s", product.StockQty)
	}
}

// TestExportsProduceValidFiles 校验导出的 Excel / CSV 真的是合法文件。
func TestExportsProduceValidFiles(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{})

	cases := []struct {
		path     string
		wantType string
	}{
		{"/export/markets.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"/export/products.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"/export/inventory.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"/export/market.xlsx?id=" + strconv.FormatInt(markets[0].ID, 10), "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"/export/movements.csv", "text/csv; charset=utf-8"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s 返回 %d", c.path, rec.Code)
			continue
		}
		if ct := rec.Header().Get("Content-Type"); ct != c.wantType {
			t.Errorf("GET %s Content-Type = %q，期望 %q", c.path, ct, c.wantType)
		}
		body := rec.Body.Bytes()
		if len(body) < 200 {
			t.Errorf("GET %s 输出过小: %d 字节", c.path, len(body))
		}
		if strings.HasPrefix(c.wantType, "application/vnd") {
			// xlsx 本质是 zip，必须以 PK 开头，并且能被 zip 读取
			if len(body) < 2 || body[0] != 'P' || body[1] != 'K' {
				t.Errorf("GET %s 不是合法的 xlsx（缺少 zip 头）", c.path)
			}
			if !zipContains(body, "xl/workbook.xml") {
				t.Errorf("GET %s 缺少 xl/workbook.xml", c.path)
			}
		}
	}
}

// TestXMLParsable 保证导出的 xlsx 内部 XML 结构能被标准库解析。
func TestXMLParsable(t *testing.T) {
	h, _, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	req := httptest.NewRequest(http.MethodGet, "/export/products.xlsx", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("导出失败: %d", rec.Code)
	}
	data, err := readZipEntry(rec.Body.Bytes(), "xl/workbook.xml")
	if err != nil {
		t.Fatalf("读取 workbook.xml 失败: %v", err)
	}
	var doc struct {
		XMLName xml.Name
		Sheets  []struct {
			Name string `xml:"name,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("workbook.xml 不是合法 XML: %v\n%s", err, firstChars(string(data), 300))
	}
	if len(doc.Sheets) == 0 {
		t.Error("导出的工作簿里没有工作表")
	}
}

// TestBackupCreatesFile 验证在线备份可用。
func TestBackupCreatesFile(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	code, _ := post(t, h, "/settings/backup", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("备份应 303，实际 %d", code)
	}
	path, err := svc.Store.Backup(context.Background(), cfg.BackupDir)
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if !strings.HasSuffix(path, ".sqlite3") {
		t.Errorf("备份文件名异常: %s", path)
	}
}

// TestRebuildStockIsIdempotent 重算库存不应改变正确数据。
func TestRebuildStockIsIdempotent(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()

	before, _ := svc.Store.ListProducts(ctx, store.ProductFilter{IncludeInactive: true})
	code, _ := post(t, h, "/settings/rebuild", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("重算应 303，实际 %d", code)
	}
	after, _ := svc.Store.ListProducts(ctx, store.ProductFilter{IncludeInactive: true})
	if len(before) != len(after) {
		t.Fatal("重算前后产品数量不一致")
	}
	for i := range before {
		if before[i].StockQty != after[i].StockQty || before[i].AvgCost != after[i].AvgCost {
			t.Errorf("%s 重算后数据变化：qty %d->%d，avg %d->%d（原始最小单位）", before[i].SKU,
				int64(before[i].StockQty), int64(after[i].StockQty),
				int64(before[i].AvgCost), int64(after[i].AvgCost))
		}
	}
}

// TestPasswordChange 修改密码后旧密码应失效。
func TestPasswordChange(t *testing.T) {
	h, _, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	code, _ := post(t, h, "/profile/password", url.Values{
		"old_password":     {"admin123"},
		"new_password":     {"newpass123"},
		"confirm_password": {"newpass123"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("改密码应 303，实际 %d", code)
	}

	// 旧密码应登录失败
	code, body := post(t, h, "/login", url.Values{"username": {"admin"}, "password": {"admin123"}})
	if code != http.StatusOK || !strings.Contains(body, "用户名或密码错误") {
		t.Error("旧密码应已失效")
	}
	// 新密码应登录成功
	doLogin(t, h, cfg, "admin", "newpass123")

	// 两次新密码不一致应报错
	cookie = doLogin(t, h, cfg, "admin", "newpass123")
	code, body = post(t, h, "/profile/password", url.Values{
		"old_password":     {"newpass123"},
		"new_password":     {"aaaaaa"},
		"confirm_password": {"bbbbbb"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("应 303 回跳，实际 %d", code)
	}
	_ = body
}

// ---------------------------------------------------------------- 小工具

func zipContains(data []byte, name string) bool {
	_, err := readZipEntry(data, name)
	return err == nil
}

func readZipEntry(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, io.EOF
}

// ---------------------------------------------------------------- 回归测试

// TestUserManagementFlows 覆盖用户管理的全部写操作。
//
// 这些操作曾经在事务里误用连接池（连接池只有 1 条连接），会永久阻塞；
// 本测试同时起到「不会挂起」的守护作用（-timeout 会把挂起变成失败）。
func TestUserManagementFlows(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()

	// 新建
	code, _ := post(t, h, "/users/new", url.Values{
		"username":  {"xiaoli"},
		"password":  {"initpass1"},
		"full_name": {"小李"},
		"role":      {model.RoleStaff},
		"is_active": {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建用户应 303，实际 %d", code)
	}
	u, err := svc.Store.UserByUsername(ctx, "xiaoli")
	if err != nil || u == nil {
		t.Fatalf("用户未创建: %v", err)
	}
	if u.Role != model.RoleStaff {
		t.Errorf("角色应为 staff，实际 %s", u.Role)
	}

	// 改账号名 + 角色 + 状态（账号名以前是只读的，现在允许修改）
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/edit", url.Values{
		"username":  {"xiaoli2"},
		"full_name": {"小李（升职）"},
		"role":      {model.RoleManager},
		"is_active": {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("修改用户应 303，实际 %d", code)
	}
	u, _ = svc.Store.UserByID(ctx, u.ID)
	if u.Role != model.RoleManager || u.FullName != "小李（升职）" || u.Username != "xiaoli2" {
		t.Errorf("用户未被正确更新: %+v", u)
	}
	// 旧账号名应已释放，新账号名可以登录
	if old, _ := svc.Store.UserByUsername(ctx, "xiaoli"); old != nil {
		t.Error("改名后旧账号名不应还能查到")
	}
	if renamed, _ := svc.Store.UserByUsername(ctx, "xiaoli2"); renamed == nil {
		t.Error("改名后应能用新账号名查到")
	}

	// 账号名不能与别人重复
	code, _ = post(t, h, "/users/new", url.Values{
		"username": {"other"}, "password": {"initpass1"}, "role": {model.RoleStaff},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建第二个用户应 303，实际 %d", code)
	}
	other, _ := svc.Store.UserByUsername(ctx, "other")
	code, _ = post(t, h, "/users/"+strconv.FormatInt(other.ID, 10)+"/edit", url.Values{
		"username": {"xiaoli2"}, "full_name": {"重名"}, "role": {model.RoleStaff}, "is_active": {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("重名提交应 303（带错误提示），实际 %d", code)
	}
	if again, _ := svc.Store.UserByID(ctx, other.ID); again.Username != "other" {
		t.Errorf("账号名冲突时不应被改写，实际 %q", again.Username)
	}

	// 按功能分配权限：只给「查看市集」，不给任何产品权限
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/edit", url.Values{
		"username":    {"xiaoli2"},
		"full_name":   {"小李（升职）"},
		"role":        {model.RoleStaff},
		"is_active":   {"1"},
		"permissions": {PermMarketView, PermMarketManage},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("保存权限应 303，实际 %d", code)
	}
	u, _ = svc.Store.UserByID(ctx, u.ID)
	if len(u.Permissions) != 2 {
		t.Fatalf("应保存 2 个权限点，实际 %v", u.Permissions)
	}

	// 自定义权限必须真的生效：能进市集，进不了产品
	staffCookie := doLogin(t, h, cfg, "xiaoli2", "initpass1")
	if code, _ := get(t, h, "/markets", staffCookie); code != http.StatusOK {
		t.Errorf("有 market.view 应能访问市集，实际 %d", code)
	}
	if code, _ := get(t, h, "/products", staffCookie); code != http.StatusForbidden {
		t.Errorf("没有 product.view 应被拒绝，实际 %d", code)
	}
	if code, _ := get(t, h, "/reports/markets", staffCookie); code != http.StatusForbidden {
		t.Errorf("没有 report.view 应被拒绝，实际 %d", code)
	}
	// 结算权限没给，收银台的结算入口也不该出现
	if _, body := get(t, h, "/markets", staffCookie); strings.Contains(body, "结算") &&
		strings.Contains(body, "撤销结算") {
		t.Error("未授予 market.settle 时不应出现结算操作")
	}

	// 非法权限点应被拒绝
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/edit", url.Values{
		"username": {"xiaoli2"}, "role": {model.RoleStaff}, "is_active": {"1"},
		"permissions": {"not.a.real.permission"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("非法权限应 303（带错误提示），实际 %d", code)
	}
	if after, _ := svc.Store.UserByID(ctx, u.ID); len(after.Permissions) != 2 {
		t.Errorf("非法权限不应写入，实际 %v", after.Permissions)
	}

	// 全不选：存成显式标记，而不是回退到角色默认
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/edit", url.Values{
		"username": {"xiaoli2"}, "role": {model.RoleStaff}, "is_active": {"1"},
		"permissions": {model.PermissionNone},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("全不选应 303，实际 %d", code)
	}
	u, _ = svc.Store.UserByID(ctx, u.ID)
	if len(u.Permissions) != 1 || u.Permissions[0] != model.PermissionNone {
		t.Fatalf("全不选应存成 none 标记，实际 %v", u.Permissions)
	}
	staffCookie = doLogin(t, h, cfg, "xiaoli2", "initpass1")
	if code, _ := get(t, h, "/markets", staffCookie); code != http.StatusForbidden {
		t.Errorf("无任何权限时应被拒绝，实际 %d", code)
	}

	// 管理员不能移除自己的用户管理权限，否则会把自己锁在外面
	adminUser := mustUser(t, svc, "admin")
	code, _ = post(t, h, "/users/"+strconv.FormatInt(adminUser.ID, 10)+"/edit", url.Values{
		"username": {"admin"}, "role": {model.RoleAdmin}, "is_active": {"1"},
		"permissions": {PermMarketView},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("应 303，实际 %d", code)
	}
	if after, _ := svc.Store.UserByID(ctx, adminUser.ID); len(after.Permissions) != 0 {
		t.Errorf("不应允许移除自己的用户管理权限，实际 %v", after.Permissions)
	}

	// 重置密码后应能用新密码登录
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/password", url.Values{
		"password":         {"newpass99"},
		"confirm_password": {"newpass99"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("重置密码应 303，实际 %d", code)
	}
	// 该账号已被改名成 xiaoli2，这里必须用新账号名登录
	doLogin(t, h, cfg, "xiaoli2", "newpass99")

	// 不能删除自己
	code, _ = post(t, h, "/users/1/delete", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("删除应 303 回跳，实际 %d", code)
	}
	if admin, _ := svc.Store.UserByID(ctx, 1); admin == nil {
		t.Error("不能删除当前登录账号")
	}

	// 删除其他用户
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/delete", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("删除用户应 303，实际 %d", code)
	}
	if gone, _ := svc.Store.UserByID(ctx, u.ID); gone != nil {
		t.Error("用户应已删除")
	}
}

// TestListFiltersWork 保证 GET 查询参数真的被用于筛选。
//
// 曾经用 PostFormValue 读取表单，导致 GET 请求的查询串被完全忽略——
// 表面上页面正常，实际上所有搜索、筛选、分页都失效。
func TestListFiltersWork(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()

	products, _ := svc.Store.ListProducts(ctx, store.ProductFilter{})
	if len(products) < 2 {
		t.Fatal("演示数据不足")
	}
	target := products[0]

	// 产品关键词筛选
	_, body := get(t, h, "/products?q="+url.QueryEscape(target.SKU), cookie)
	if !strings.Contains(body, target.SKU) {
		t.Errorf("按 SKU 搜索应命中 %s", target.SKU)
	}
	for _, p := range products[1:] {
		if strings.Contains(body, p.SKU) {
			t.Errorf("按 SKU 搜索不应出现 %s", p.SKU)
		}
	}

	// 市集状态筛选：只看计划中的市集
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{})
	var openName, openStatus string
	settledCount := 0
	for _, m := range markets {
		if m.Status != model.MarketSettled && openName == "" {
			openName, openStatus = m.Name, m.Status
		}
		if m.Status == model.MarketSettled {
			settledCount++
		}
	}
	if openName == "" || settledCount == 0 {
		t.Fatalf("演示数据应同时包含未结算与已结算市集（open=%q settled=%d）", openName, settledCount)
	}
	_, body = get(t, h, "/markets?status="+openStatus, cookie)
	if !strings.Contains(body, openName) {
		t.Errorf("筛选 %s 应包含 %s", model.MarketStatusLabels[openStatus], openName)
	}
	_, body = get(t, h, "/markets?status="+model.MarketSettled, cookie)
	if strings.Contains(body, openName) {
		t.Errorf("筛选「已结算」不应包含未结算的 %s", openName)
	}

	// 流水方向筛选。
	// 注意不能用「市集销售」这类文案断言——筛选下拉框本身就含这些字，
	// 所以改用单据号前缀：入库流水来自采购单（PO-），出库流水来自市集（MK-）。
	_, body = get(t, h, "/inventory/movements?direction=in", cookie)
	if !strings.Contains(body, "PO-") {
		t.Error("筛选「仅入库」应出现采购入库流水（PO- 单号）")
	}
	if strings.Contains(body, "MK-") {
		t.Error("筛选「仅入库」不应出现市集流水（MK- 单号）")
	}
	_, body = get(t, h, "/inventory/movements?direction=out", cookie)
	if !strings.Contains(body, "MK-") {
		t.Error("筛选「仅出库」应出现市集流水（MK- 单号）")
	}
	if strings.Contains(body, "PO-") {
		t.Error("筛选「仅出库」不应出现采购入库流水（PO- 单号）")
	}

	// 采购单状态筛选：演示数据只有已入库单据，草稿应为空
	_, body = get(t, h, "/purchases?status="+model.PurchaseConfirmed, cookie)
	if !strings.Contains(body, "PO-") {
		t.Error("筛选「已入库」应出现已入库单据")
	}
	_, body = get(t, h, "/purchases?status="+model.PurchaseDraft, cookie)
	if strings.Contains(body, "PO-") {
		t.Error("筛选「草稿」不应出现已入库单据")
	}

	// 分页链接应保留筛选条件
	_, body = get(t, h, "/inventory/movements?direction=in&page=1", cookie)
	if !strings.Contains(body, "direction=in") {
		t.Error("分页链接应保留筛选条件")
	}
}

// TestStaticAssetsAndServiceWorker 校验前端资源的缓存策略与 Service Worker 可注册性。
//
// 背景：/static/sw.js 的作用域最多只能覆盖 /static/，注册时申请 scope "/"
// 会被浏览器拒绝（离线缓存与 PWA 安装全部失效）。因此必须从根路径 /sw.js 提供，
// 并带上 Service-Worker-Allowed 头。
func TestStaticAssetsAndServiceWorker(t *testing.T) {
	h, _, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 根路径的 Service Worker
	req := httptest.NewRequest(http.MethodGet, "/sw.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /sw.js 返回 %d，期望 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "serviceWorker") && !strings.Contains(body, "addEventListener") {
		t.Error("/sw.js 内容不像 Service Worker 脚本")
	}
	if strings.Contains(body, "__ERP_VERSION__") {
		t.Error("/sw.js 里的版本占位符没有被替换")
	}
	if got := rec.Header().Get("Service-Worker-Allowed"); got != "/" {
		t.Errorf("缺少 Service-Worker-Allowed: /（实际 %q）", got)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("/sw.js 必须是 no-cache，实际 %q", cc)
	}
	if !strings.Contains(body, cfg.Version) {
		t.Errorf("/sw.js 里应包含构建版本 %q", cfg.Version)
	}

	// 带版本号的资源可以长缓存
	req = httptest.NewRequest(http.MethodGet, "/static/css/app.css?v="+cfg.Version, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("带版本号的资源应可长缓存，实际 %q", cc)
	}

	// 页面引用的 css/js 必须带版本号，否则升级后浏览器会继续用旧文件
	_, page := get(t, h, "/", cookie)
	for _, asset := range []string{"/static/css/app.css", "/static/js/app.js"} {
		if !strings.Contains(page, asset+"?v=") {
			t.Errorf("页面里的 %s 没有带版本号，改版后会取到旧缓存", asset)
		}
	}

	// 移动端底部导航的「更多」必须是真实链接，不能依赖 JS
	if !strings.Contains(page, `href="/more"`) {
		t.Error("底部导航的「更多」应当是 /more 链接")
	}
}

// TestMarketPOSFlow 收银台逐笔记账的完整链路。
//
// 现场流程：点「销售 +1」记一单 → 顶部销售额与单数实时变化 →
// 记错了点「撤销」→ 数量同步回退。这条链路是市集现场的核心，
// 任何一处算错都会直接反映到净利润上。
func TestMarketPOSFlow(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 准备一个产品并入库 20 瓶，成本 50
	code, _ := post(t, h, "/products/new", url.Values{
		"sku": {"POS-001"}, "name": {"收银台测试冰酒"}, "volume_ml": {"375"},
		"sale_price": {"398"}, "unit": {"瓶"}, "bottles_per_case": {"12"},
		"is_active": {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建产品应 303，实际 %d", code)
	}
	product, err := svc.Store.ProductBySKU(ctx, "POS-001")
	if err != nil || product == nil {
		t.Fatalf("产品未创建: %v", err)
	}
	if err := svc.AdjustStock(ctx, service.AdjustInput{
		ProductID: product.ID, Qty: model.MustQty("20"),
		UnitCost: model.MustMoney("50"), Reason: model.ReasonOpening,
		Note: "期初", OccurredOn: "2025-06-01",
	}, mustUser(t, svc, "admin")); err != nil {
		t.Fatalf("入库失败: %v", err)
	}

	// 建市集并上架
	code, _ = post(t, h, "/markets/new", url.Values{
		"name": {"收银台测试市集"}, "start_date": {"2025-06-10"}, "end_date": {"2025-06-10"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建市集应 303，实际 %d", code)
	}
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "收银台测试"})
	marketID := markets[0].ID
	mkt := strconv.FormatInt(marketID, 10)

	if _, body := postHTMX(t, h, "/markets/"+mkt+"/items",
		url.Values{"product_id": {strconv.FormatInt(product.ID, 10)}}, cookie); !strings.Contains(body, "收银台测试冰酒") {
		t.Fatal("上架后收银台/明细页应显示产品")
	}
	// 填带去数量 20、售价 398
	items, _ := svc.Store.MarketItems(ctx, marketID)
	if _, body := postHTMX(t, h, "/markets/"+mkt+"/items/"+strconv.FormatInt(items[0].ID, 10),
		url.Values{"carried_qty": {"20"}, "unit_price": {"398"}}, cookie); !strings.Contains(body, "398") {
		t.Fatal("保存带去数量与售价失败")
	}

	// 收银台页面能打开
	code, body := get(t, h, "/markets/"+mkt+"/pos", cookie)
	if code != http.StatusOK || !strings.Contains(body, "收银台") {
		t.Fatalf("收银台应可访问，实际 %d", code)
	}

	pid := strconv.FormatInt(product.ID, 10)

	// 记两单销售
	for i := 0; i < 2; i++ {
		code, body = postHTMX(t, h, "/markets/"+mkt+"/records",
			url.Values{"product_id": {pid}, "kind": {"sale"}, "qty": {"1"}}, cookie)
		if code != http.StatusOK {
			t.Fatalf("记账应 200，实际 %d", code)
		}
	}
	if !strings.Contains(body, "¥796.00") {
		t.Errorf("两单销售后销售额应为 ¥796.00，面板内容未体现")
	}

	// 记一次试饮
	code, body = postHTMX(t, h, "/markets/"+mkt+"/records",
		url.Values{"product_id": {pid}, "kind": {"tasting"}, "qty": {"1"}}, cookie)
	if code != http.StatusOK {
		t.Fatalf("试饮记账应 200，实际 %d", code)
	}

	records, err := svc.Store.MarketRecords(ctx, marketID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("应有 3 笔记录，实际 %d", len(records))
	}

	// 汇总数量应自动累加
	item, _ := svc.Store.MarketItemByID(ctx, items[0].ID)
	if item.SoldQty != model.MustQty("2") {
		t.Errorf("已售数量应为 2，实际 %s", item.SoldQty)
	}
	if item.TastingQty != model.MustQty("1") {
		t.Errorf("试饮数量应为 1，实际 %s", item.TastingQty)
	}

	// 损益：销售额 796，售出成本 100，试饮成本 50
	m, _ := svc.Store.MarketByID(ctx, marketID)
	totals := m.Totals()
	if totals.Revenue != model.MustMoney("796") {
		t.Errorf("销售额应为 796，实际 %s", totals.Revenue)
	}
	if totals.CogsSold != model.MustMoney("100") {
		t.Errorf("售出成本应为 100，实际 %s", totals.CogsSold)
	}
	if totals.TastingCost != model.MustMoney("50") {
		t.Errorf("试饮成本应为 50，实际 %s", totals.TastingCost)
	}
	if totals.NetProfit != model.MustMoney("646") {
		t.Errorf("净利润应为 646（796-100-50），实际 %s", totals.NetProfit)
	}

	// 撤销一笔销售：销售额与数量都要跟着退回来
	saleID := int64(0)
	for _, r := range records {
		if r.IsSale() {
			saleID = r.ID
			break
		}
	}
	code, body = postHTMX(t, h, "/markets/"+mkt+"/records/"+strconv.FormatInt(saleID, 10)+"/delete",
		url.Values{}, cookie)
	if code != http.StatusOK {
		t.Fatalf("撤销应 200，实际 %d", code)
	}
	if !strings.Contains(body, "¥398.00") {
		t.Errorf("撤销一单后销售额应为 ¥398.00")
	}
	item, _ = svc.Store.MarketItemByID(ctx, items[0].ID)
	if item.SoldQty != model.MustQty("1") {
		t.Errorf("撤销后已售数量应为 1，实际 %s", item.SoldQty)
	}

	// 结算后不能再记账
	user := mustUser(t, svc, "admin")
	// 收银台记的一笔销售 + 试饮，结算后库存应减少 1 瓶销售 + 1 杯试饮
	if err := svc.SettleMarket(ctx, marketID, user); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	code, body = postHTMX(t, h, "/markets/"+mkt+"/records",
		url.Values{"product_id": {pid}, "kind": {"sale"}, "qty": {"1"}}, cookie)
	if strings.Contains(body, "已记录") && !strings.Contains(body, "已结算") {
		t.Error("已结算的市集不应再接受记账")
	}
	after, _ := svc.Store.ProductByID(ctx, product.ID)
	if after.StockQty != model.MustQty("18") {
		t.Errorf("结算后库存应为 18（20-1销售-1试饮），实际 %s", after.StockQty)
	}

	// 结算后记录应带上成本快照
	records, _ = svc.Store.MarketRecords(ctx, marketID, 0)
	for _, r := range records {
		if !r.HasSnapshot() {
			t.Errorf("结算后记录 %d 应锁定成本快照", r.ID)
			continue
		}
		if r.CostBasis() != model.MustMoney("50") {
			t.Errorf("成本快照应为 50，实际 %s", r.CostBasis())
		}
	}
}

// TestBarcodeScanFlow 扫码出库：绑定条码 → 扫码记账 → 重复扫码不再重复记账。
func TestBarcodeScanFlow(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	code, _ := post(t, h, "/products/new", url.Values{
		"sku": {"SCAN-001"}, "name": {"扫码测试冰酒"}, "volume_ml": {"375"},
		"sale_price": {"268"}, "unit": {"瓶"}, "bottles_per_case": {"12"},
		"barcode": {"0690123456789"}, "is_active": {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建产品应 303，实际 %d", code)
	}
	product, _ := svc.Store.ProductBySKU(ctx, "SCAN-001")
	if product == nil {
		t.Fatal("产品未创建")
	}
	if product.Barcode != "0690123456789" {
		t.Fatalf("条码应被保存，实际 %q", product.Barcode)
	}

	// 按条码能查到产品
	found, err := svc.LookupBarcode(ctx, "0690123456789")
	if err != nil || found.Product == nil {
		t.Fatalf("按条码应能查到产品: %v", err)
	}
	if found.Product.ID != product.ID {
		t.Error("查到的产品不对")
	}

	// 条码查不到时应返回空而不是报错
	miss, err := svc.LookupBarcode(ctx, "0000000000000")
	if err != nil {
		t.Fatalf("未绑定的条码不应报错: %v", err)
	}
	if miss.Product != nil {
		t.Error("未绑定的条码不应查到产品")
	}

	// 建市集、上架、扫码出库
	post(t, h, "/markets/new", url.Values{
		"name": {"扫码测试市集"}, "start_date": {"2025-06-20"}, "end_date": {"2025-06-20"},
	}, cookie)
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "扫码测试"})
	marketID := markets[0].ID
	mkt := strconv.FormatInt(marketID, 10)
	postHTMX(t, h, "/markets/"+mkt+"/items",
		url.Values{"product_id": {strconv.FormatInt(product.ID, 10)}}, cookie)

	// 未知条码：返回绑定表单而不是报错
	code, body := postHTMX(t, h, "/markets/"+mkt+"/scan",
		url.Values{"code": {"9999999999999"}}, cookie)
	if code != http.StatusOK {
		t.Fatalf("扫码应 200，实际 %d", code)
	}
	if !strings.Contains(body, "还没有绑过产品") {
		t.Error("未知条码应提示绑定产品")
	}

	// 已绑定的条码：直接记一笔销售
	code, body = postHTMX(t, h, "/markets/"+mkt+"/scan",
		url.Values{"code": {"0690123456789"}}, cookie)
	if code != http.StatusOK {
		t.Fatalf("扫码应 200，实际 %d", code)
	}
	if !strings.Contains(body, "已扫码出库") {
		t.Errorf("扫码后应提示已出库，实际未包含提示")
	}
	records, _ := svc.Store.MarketRecords(ctx, marketID, 0)
	if len(records) != 1 {
		t.Fatalf("扫码应产生 1 笔记录，实际 %d", len(records))
	}
	if records[0].Channel != "scan" {
		t.Errorf("记录来源应为 scan，实际 %s", records[0].Channel)
	}
	if records[0].UnitPrice != model.MustMoney("268") {
		t.Errorf("扫码销售单价应取产品售价 268，实际 %s", records[0].UnitPrice)
	}
}

// TestBarcodeDuplicateRejected 同一个条码不能绑到两个产品上。
func TestBarcodeDuplicateRejected(t *testing.T) {
	_, svc, _ := testApp(t)
	ctx := context.Background()
	user := mustUser(t, svc, "admin")

	id1, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "DUP-1", Name: "产品一", Unit: "瓶",
		SalePrice: model.MustMoney("100"), IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := svc.Store.CreateProduct(ctx, &model.Product{
		SKU: "DUP-2", Name: "产品二", Unit: "瓶",
		SalePrice: model.MustMoney("100"), IsActive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.BindProductBarcode(ctx, id1, "1234567890128", user); err != nil {
		t.Fatalf("首次绑定应成功: %v", err)
	}
	if err := svc.BindProductBarcode(ctx, id2, "1234567890128", user); err == nil {
		t.Error("同一条码绑到第二个产品应被拒绝")
	}
}

// mustUser 取指定账号，测试里用于模拟操作人。
func mustUser(t *testing.T, svc *service.Service, username string) *model.User {
	t.Helper()
	u, err := svc.Store.UserByUsername(context.Background(), username)
	if err != nil {
		t.Fatalf("查询用户 %s 失败: %v", username, err)
	}
	if u == nil {
		t.Fatalf("用户 %s 不存在", username)
	}
	return u
}

// TestAdjustDirection 出入库用「方向 + 正数」而不是正负号。
func TestAdjustDirection(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	post(t, h, "/products/new", url.Values{
		"sku": {"DIR-001"}, "name": {"方向测试冰酒"}, "unit": {"瓶"},
		"sale_price": {"100"}, "is_active": {"1"},
	}, cookie)
	product, _ := svc.Store.ProductBySKU(ctx, "DIR-001")
	pid := strconv.FormatInt(product.ID, 10)

	// 入库 10：填正数 + 方向 in
	code, _ := post(t, h, "/inventory/adjust", url.Values{
		"product_id": {pid}, "direction": {"in"}, "qty": {"10"},
		"reason": {"opening"}, "unit_cost": {"30"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("入库应 303，实际 %d", code)
	}
	got, _ := svc.Store.ProductByID(ctx, product.ID)
	if got.StockQty != model.MustQty("10") {
		t.Errorf("入库后库存应为 10，实际 %s", got.StockQty)
	}

	// 出库 3：仍然填正数，方向 out
	code, _ = post(t, h, "/inventory/adjust", url.Values{
		"product_id": {pid}, "direction": {"out"}, "qty": {"3"},
		"reason": {"adjust_out"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("出库应 303，实际 %d", code)
	}
	got, _ = svc.Store.ProductByID(ctx, product.ID)
	if got.StockQty != model.MustQty("7") {
		t.Errorf("出库后库存应为 7，实际 %s", got.StockQty)
	}

	// 选了方向还填负数：应被拦下（库存不变）
	post(t, h, "/inventory/adjust", url.Values{
		"product_id": {pid}, "direction": {"in"}, "qty": {"-5"}, "reason": {"opening"},
	}, cookie)
	if got, _ = svc.Store.ProductByID(ctx, product.ID); got.StockQty != model.MustQty("7") {
		t.Errorf("方向为入库却填负数，库存不应变化，实际 %s", got.StockQty)
	}

	// 方向与类型不匹配：应被拦下（库存不变）
	post(t, h, "/inventory/adjust", url.Values{
		"product_id": {pid}, "direction": {"out"}, "qty": {"2"}, "reason": {"opening"},
	}, cookie)
	if got, _ = svc.Store.ProductByID(ctx, product.ID); got.StockQty != model.MustQty("7") {
		t.Errorf("出库却选「期初建账」，库存不应变化，实际 %s", got.StockQty)
	}

	// 兼容旧写法：只给负数、不给方向
	code, _ = post(t, h, "/inventory/adjust", url.Values{
		"product_id": {pid}, "qty": {"-2"}, "reason": {"adjust_out"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("兼容负数写法应 303，实际 %d", code)
	}
	got, _ = svc.Store.ProductByID(ctx, product.ID)
	if got.StockQty != model.MustQty("5") {
		t.Errorf("兼容写法出库后库存应为 5，实际 %s", got.StockQty)
	}
}

// TestProductImageUpload 产品图片上传：自动压缩、可访问、可删除。
func TestProductImageUpload(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	post(t, h, "/products/new", url.Values{
		"sku": {"IMG-001"}, "name": {"图片测试冰酒"}, "unit": {"瓶"},
		"sale_price": {"200"}, "is_active": {"1"},
	}, cookie)
	product, _ := svc.Store.ProductBySKU(ctx, "IMG-001")
	if product == nil {
		t.Fatal("产品未创建")
	}
	pid := strconv.FormatInt(product.ID, 10)

	// 造一张 2400×1600 的 PNG，用来验证服务端会缩到长边 1280
	src := image.NewRGBA(image.Rect(0, 0, 2400, 1600))
	for y := 0; y < 1600; y++ {
		for x := 0; x < 2400; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, src); err != nil {
		t.Fatal(err)
	}

	upload := func(name string, content []byte) *httptest.ResponseRecorder {
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		fw, err := mw.CreateFormFile("image", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatal(err)
		}
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/products/"+pid+"/image", body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := upload("bottle.png", raw.Bytes()); rec.Code != http.StatusSeeOther {
		t.Fatalf("上传应 303，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	updated, _ := svc.Store.ProductByID(ctx, product.ID)
	if !strings.HasPrefix(updated.ImageURL, "/uploads/products/") {
		t.Fatalf("图片地址应指向上传目录，实际 %q", updated.ImageURL)
	}

	// 上传的文件确实存在，且已经被压缩
	disk := filepath.Join(svc.UploadDir(), strings.TrimPrefix(updated.ImageURL, "/uploads/"))
	f, err := os.Open(disk)
	if err != nil {
		t.Fatalf("上传的图片文件不存在: %v", err)
	}
	conf, _, err := image.DecodeConfig(f)
	f.Close()
	if err != nil {
		t.Fatalf("读取图片失败: %v", err)
	}
	if conf.Width > 1280 || conf.Height > 1280 {
		t.Errorf("图片应被压到长边 1280，实际 %dx%d", conf.Width, conf.Height)
	}
	if conf.Width != 1280 {
		t.Errorf("2400×1600 等比缩放后宽应为 1280，实际 %d", conf.Width)
	}

	// 可以通过 HTTP 访问，且带长缓存头
	req := httptest.NewRequest(http.MethodGet, updated.ImageURL, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("图片应可访问，实际 %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("上传的图片应可长缓存，实际 %q", cc)
	}

	// 非图片文件应被拒绝：原有图片不能被替换掉
	upload("notes.txt", []byte("这不是图片"))
	if still, _ := svc.Store.ProductByID(ctx, product.ID); still.ImageURL != updated.ImageURL {
		t.Errorf("非图片文件不应替换已有图片，实际 %q", still.ImageURL)
	}

	// 选择产品时能看到图片：产品选项里带有图片地址
	options, err := svc.Store.ListProducts(ctx, store.ProductFilter{Keyword: "IMG-001"})
	if err != nil || len(options) == 0 {
		t.Fatal("查询产品失败")
	}
	if options[0].ImageURL == "" {
		t.Error("产品应带上图片地址")
	}

	// 删除图片
	code, _ := post(t, h, "/products/"+pid+"/image/delete", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("删除图片应 303，实际 %d", code)
	}
	cleared, _ := svc.Store.ProductByID(ctx, product.ID)
	if cleared.ImageURL != "" {
		t.Errorf("删除后图片地址应为空，实际 %q", cleared.ImageURL)
	}
	if _, err := os.Stat(disk); !os.IsNotExist(err) {
		t.Error("删除后磁盘上的图片文件也应被清掉")
	}
}

// TestAppBrandingAndInstallPrompt 应用名与安装引导的行为约定。
//
// 这套系统以后还会接别的产品线，所以界面上一律叫「ERP」，
// 不再出现「冰酒 ERP」这类绑定品类的名字；
// 安装到桌面的引导只在手机上出现，PC 浏览器不需要。
func TestAppBrandingAndInstallPrompt(t *testing.T) {
	h, _, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// PWA 清单：图标下面显示的名字就叫 ERP
	code, manifest := get(t, h, "/static/manifest.webmanifest", cookie)
	if code != http.StatusOK {
		t.Fatalf("清单应可访问，实际 %d", code)
	}
	var parsed struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
	}
	if err := json.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("清单不是合法 JSON: %v", err)
	}
	if parsed.ShortName != "ERP" {
		t.Errorf("清单 short_name 应为 ERP，实际 %q", parsed.ShortName)
	}
	if !strings.HasPrefix(parsed.Name, "ERP") {
		t.Errorf("清单 name 应以 ERP 开头，实际 %q", parsed.Name)
	}
	if strings.Contains(parsed.Name, "冰酒") {
		t.Errorf("应用名不应绑定品类，实际 %q", parsed.Name)
	}

	// 离线页与应用名保持一致
	_, offline := get(t, h, "/static/offline.html", cookie)
	if strings.Contains(offline, "冰酒") {
		t.Error("离线页不应出现品类名")
	}
	if !strings.Contains(offline, "ERP") {
		t.Error("离线页应显示应用名")
	}

	// 安装条用应用名（而不是账套里的公司名），并且文案只在手机端有意义
	_, page := get(t, h, "/", cookie)
	if !strings.Contains(page, "install-bar") {
		t.Fatal("页面里应有安装引导条")
	}
	if !strings.Contains(page, "把「"+cfg.AppName+"」装到手机桌面") {
		t.Error("安装提示应使用应用名")
	}

	// 品牌与交互脚本里也不应再写死品类名
	_, css := get(t, h, "/static/css/app.css", cookie)
	if strings.Contains(css, "冰酒") {
		t.Error("样式表注释里不应出现品类名")
	}
	if !strings.Contains(css, "sidebar__nav::-webkit-scrollbar") {
		t.Error("侧边栏应有细滚动条样式，避免系统默认滚动条切出白边")
	}
	// 宽屏下隐藏安装引导
	if !strings.Contains(css, ".install-bar.is-visible { display: none !important; }") {
		t.Error("PC 端应隐藏安装引导")
	}
}

// TestBackupRoutes 手动备份、下载与删除。
func TestBackupRoutes(t *testing.T) {
	h, svc, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 设置页应显示自动备份状态与备份列表
	code, page := get(t, h, "/settings", cookie)
	if code != http.StatusOK {
		t.Fatalf("设置页应可访问，实际 %d", code)
	}
	for _, want := range []string{"自动备份", "有变化时的间隔", "最多保留几份", "立即备份"} {
		if !strings.Contains(page, want) {
			t.Errorf("设置页应包含 %q", want)
		}
	}

	// 立即备份
	code, _ = post(t, h, "/settings/backup", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("手动备份应 303，实际 %d", code)
	}
	list, err := svc.ListBackups()
	if err != nil || len(list) == 0 {
		t.Fatalf("备份未生成: %v", err)
	}
	name := list[0].Name

	// 设置页能看到这份备份，并能下载
	_, page = get(t, h, "/settings", cookie)
	if !strings.Contains(page, name) {
		t.Errorf("设置页应列出备份 %s", name)
	}
	req := httptest.NewRequest(http.MethodGet, "/settings/backup/"+name, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("下载备份应 200，实际 %d", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Error("下载的备份内容不应为空")
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("应作为附件下载，实际 %q", cd)
	}

	// 目录穿越必须被挡住
	for _, bad := range []string{"..%2Fsecret.key", "x.txt"} {
		req := httptest.NewRequest(http.MethodGet, "/settings/backup/"+bad, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("非法备份名 %s 不应返回 200", bad)
		}
	}

	// 删除备份
	code, _ = post(t, h, "/settings/backup/"+name+"/delete", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("删除备份应 303，实际 %d", code)
	}
	if after, _ := svc.ListBackups(); len(after) != 0 {
		t.Errorf("删除后应没有备份，实际 %d 份", len(after))
	}

	// 没有 system 权限的人不能碰备份
	staffID, err := svc.CreateUser(context.Background(), "nosys", "staffpass1", "无权限",
		model.RoleStaff, []string{PermMarketView}, mustUser(t, svc, "admin"))
	if err != nil || staffID == 0 {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	staff := doLogin(t, h, cfg, "nosys", "staffpass1")
	if code, _ := post(t, h, "/settings/backup", url.Values{}, staff); code != http.StatusForbidden {
		t.Errorf("没有设置权限不应能备份，实际 %d", code)
	}
}

// TestSelfUsernameChange 用户在「我的账号」里改自己的账号名。
//
// 以前只有「用户管理」里能改，管理员本人常常找不到入口。
func TestSelfUsernameChange(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	admin := mustUser(t, svc, "admin")

	// 页面上有改名入口
	code, page := get(t, h, "/profile", cookie)
	if code != http.StatusOK {
		t.Fatalf("我的账号页应可访问，实际 %d", code)
	}
	if !strings.Contains(page, "/profile/username") {
		t.Fatal("「我的账号」里应当能修改账号名")
	}

	// 格式不合法要被拦下
	for _, bad := range []string{"ab", "有中文", "with space", "a@b"} {
		if code, _ := post(t, h, "/profile/username", url.Values{"username": {bad}}, cookie); code != http.StatusSeeOther {
			t.Fatalf("非法账号名 %q 应 303（带错误提示），实际 %d", bad, code)
		}
		if u, _ := svc.Store.UserByID(ctx, admin.ID); u.Username != "admin" {
			t.Fatalf("非法账号名不应写入，实际 %q", u.Username)
		}
	}

	// 与当前相同应提示没有变化
	post(t, h, "/profile/username", url.Values{"username": {"admin"}}, cookie)
	if u, _ := svc.Store.UserByID(ctx, admin.ID); u.Username != "admin" {
		t.Error("账号名未变时不应改动")
	}

	// 正常改名
	code, _ = post(t, h, "/profile/username", url.Values{"username": {"boss"}}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("改名应 303，实际 %d", code)
	}
	renamed, _ := svc.Store.UserByID(ctx, admin.ID)
	if renamed.Username != "boss" {
		t.Fatalf("账号名应改为 boss，实际 %q", renamed.Username)
	}
	// 角色与权限不能被这次改名影响
	if renamed.Role != model.RoleAdmin || !renamed.IsActive {
		t.Errorf("改名不应影响角色与启用状态: %+v", renamed)
	}

	// 当前会话继续有效（会话认的是用户 ID）
	if code, _ := get(t, h, "/", cookie); code != http.StatusOK {
		t.Errorf("改名后当前会话应继续可用，实际 %d", code)
	}
	// 新账号名可以登录，旧的不行
	doLogin(t, h, cfg, "boss", "admin123")
	if u, _ := svc.Store.UserByUsername(ctx, "admin"); u != nil {
		t.Error("旧账号名应已释放")
	}

	// 重名被拒
	if _, err := svc.CreateUser(ctx, "taken", "takenpass1", "占用", model.RoleStaff, nil, renamed); err != nil {
		t.Fatal(err)
	}
	post(t, h, "/profile/username", url.Values{"username": {"taken"}}, cookie)
	if u, _ := svc.Store.UserByID(ctx, admin.ID); u.Username != "boss" {
		t.Errorf("重名时不应改写，实际 %q", u.Username)
	}
}

// TestProductProfileFields 产品档案的完整字段（不只服务酒类）。
func TestProductProfileFields(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	code, _ := post(t, h, "/products/new", url.Values{
		"sku":              {"GIFT-001"},
		"name":             {"节日礼盒"},
		"name_en":          {"Holiday Gift Box"},
		"brand":            {"SoulFound"},
		"category":         {"礼盒"},
		"origin":           {"加拿大 尼亚加拉"},
		"vintage":          {"2020"},
		"volume_ml":        {"375"},
		"abv":              {"11.5%"},
		"unit":             {"盒"},
		"bottles_per_case": {"6"},
		"sale_price":       {"598"},
		"cost_price":       {"268.50"},
		"specs":            {"葡萄品种: 维代尔\n甜度：很甜\n375ml 双支装\n\n"},
		"low_stock_qty":    {"4"},
		"is_active":        {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建产品应 303，实际 %d", code)
	}

	p, err := svc.Store.ProductBySKU(ctx, "GIFT-001")
	if err != nil || p == nil {
		t.Fatalf("产品未创建: %v", err)
	}
	// 非酒类也能填：品类/单位自由输入
	if p.Category != "礼盒" || p.Unit != "盒" {
		t.Errorf("品类与单位应支持自由输入，实际 %q / %q", p.Category, p.Unit)
	}
	if p.Brand != "SoulFound" || p.Origin != "加拿大 尼亚加拉" {
		t.Errorf("品牌与产地未保存: %+v", p)
	}
	if p.ABV != 1150 {
		t.Errorf("酒精度应存成 1150（11.5%%），实际 %d", p.ABV)
	}
	if p.ABVText() != "11.5%" {
		t.Errorf("酒精度文本应为 11.5%%，实际 %q", p.ABVText())
	}
	if p.CostPrice != model.MustMoney("268.50") {
		t.Errorf("参考成本价应为 268.50，实际 %s", p.CostPrice)
	}
	if p.BottlesPerCase != 6 || p.VolumeML != 375 || p.Vintage != 2020 {
		t.Errorf("规格字段未保存: %+v", p)
	}

	// 规格参数：中英文冒号都认，没有冒号的行整行当值，空行丢掉
	specs := p.SpecList()
	if len(specs) != 3 {
		t.Fatalf("规格参数应解析出 3 条，实际 %d：%+v", len(specs), specs)
	}
	if specs[0].Label != "葡萄品种" || specs[0].Value != "维代尔" {
		t.Errorf("第一条解析错误: %+v", specs[0])
	}
	if specs[1].Label != "甜度" || specs[1].Value != "很甜" {
		t.Errorf("中文冒号解析错误: %+v", specs[1])
	}
	if specs[2].Label != "" || specs[2].Value != "375ml 双支装" {
		t.Errorf("无冒号行应整行作为值: %+v", specs[2])
	}

	// 没有采购记录时，参考成本价应当被当作成本使用
	if got := p.CostPriceOrAvg(); got != model.MustMoney("268.50") {
		t.Errorf("无采购记录时应退回参考成本价，实际 %s", got)
	}

	// 详情页要把这些都显示出来
	code, body := get(t, h, "/products/"+strconv.FormatInt(p.ID, 10), cookie)
	if code != http.StatusOK {
		t.Fatalf("产品详情应可访问，实际 %d", code)
	}
	for _, want := range []string{"品牌", "产地 / 产区", "酒精度", "参考成本价", "规格参数",
		"葡萄品种", "维代尔", "375ml 双支装", "11.5%"} {
		if !strings.Contains(body, want) {
			t.Errorf("详情页应显示 %q", want)
		}
	}

	// 编辑时能改回来（字段可空）
	code, _ = post(t, h, "/products/"+strconv.FormatInt(p.ID, 10)+"/edit", url.Values{
		"sku": {"GIFT-001"}, "name": {"节日礼盒"}, "category": {"礼盒"}, "unit": {"盒"},
		"sale_price": {"598"}, "abv": {""}, "is_active": {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("保存应 303，实际 %d", code)
	}
	updated, _ := svc.Store.ProductByID(ctx, p.ID)
	if updated.ABV != 0 || updated.ABVText() != "" {
		t.Errorf("清空酒精度后应为 0 且不显示，实际 %d / %q", updated.ABV, updated.ABVText())
	}
	if updated.Specs != "" {
		t.Errorf("未提交规格参数时应清空，实际 %q", updated.Specs)
	}

	// 非法酒精度要被拦下
	post(t, h, "/products/"+strconv.FormatInt(p.ID, 10)+"/edit", url.Values{
		"sku": {"GIFT-001"}, "name": {"节日礼盒"}, "sale_price": {"598"},
		"abv": {"120"}, "is_active": {"1"},
	}, cookie)
	if after, _ := svc.Store.ProductByID(ctx, p.ID); after.ABV != 0 {
		t.Errorf("超出 0-100 的酒精度不应写入，实际 %d", after.ABV)
	}
}

// TestPurchasePrefillsCostPrice 采购单的单价应预填成本价而不是售价。
func TestPurchasePrefillsCostPrice(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	post(t, h, "/products/new", url.Values{
		"sku": {"PF-001"}, "name": {"预填测试"}, "unit": {"瓶"},
		"sale_price": {"398"}, "cost_price": {"168"}, "is_active": {"1"},
	}, cookie)
	p, _ := svc.Store.ProductBySKU(ctx, "PF-001")
	if p == nil {
		t.Fatal("产品未创建")
	}

	// 采购单页面拿到的产品选项里，成本应是参考成本价
	opts, err := svc.Store.ListProducts(ctx, store.ProductFilter{Keyword: "PF-001"})
	if err != nil || len(opts) == 0 {
		t.Fatal("查询产品失败")
	}
	if got := opts[0].CostPriceOrAvg(); got != model.MustMoney("168") {
		t.Errorf("参考成本价应为 168，实际 %s", got)
	}
	if opts[0].CostPrice == opts[0].SalePrice {
		t.Error("参考成本价不应等于售价")
	}

	// 页面里要有成本价的提示文案
	_, page := get(t, h, "/purchases/new", cookie)
	if !strings.Contains(page, "成本") {
		t.Error("采购单页面的产品选项应显示成本")
	}
}

// TestPOSWorksWithoutPlanning 收银台不需要提前选产品。
//
// 用户反馈：每场市集都要先勾选产品太繁琐。正确用法是现场直接卖，
// 系统在记账时自动把产品补进本场明细。
func TestPOSWorksWithoutPlanning(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 建两个产品，但**都不加入市集**
	for _, s := range [][2]string{{"NP-001", "甲产品"}, {"NP-002", "乙产品"}} {
		post(t, h, "/products/new", url.Values{
			"sku": {s[0]}, "name": {s[1]}, "unit": {"瓶"},
			"sale_price": {"200"}, "is_active": {"1"},
		}, cookie)
	}
	a, _ := svc.Store.ProductBySKU(ctx, "NP-001")

	// 建一场空市集（一个产品都不加）
	post(t, h, "/markets/new", url.Values{
		"name": {"不预选测试市集"}, "start_date": {"2025-07-01"}, "end_date": {"2025-07-01"},
	}, cookie)
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "不预选测试"})
	marketID := markets[0].ID
	mkt := strconv.FormatInt(marketID, 10)

	// 收银台应能打开，并且列出全部在售产品
	code, body := get(t, h, "/markets/"+mkt+"/pos", cookie)
	if code != http.StatusOK {
		t.Fatalf("空市集的收银台也应能打开，实际 %d", code)
	}
	if !strings.Contains(body, "甲产品") || !strings.Contains(body, "乙产品") {
		t.Fatal("收银台应直接列出全部在售产品，不需要提前上架")
	}
	if !strings.Contains(body, "不需要提前选产品") {
		t.Error("收银台应说明不需要提前选产品")
	}

	// 直接记一笔销售：应成功，并自动把产品补进本场明细
	code, body = postHTMX(t, h, "/markets/"+mkt+"/records",
		url.Values{"product_id": {strconv.FormatInt(a.ID, 10)}, "kind": {"sale"}, "qty": {"2"}}, cookie)
	if code != http.StatusOK {
		t.Fatalf("未上架也要能直接记账，实际 %d", code)
	}
	if !strings.Contains(body, "¥400.00") {
		t.Errorf("2 瓶 × 200 应记 ¥400.00")
	}

	// 产品应当被自动加入本场
	m, _ := svc.Store.MarketByID(ctx, marketID)
	if len(m.Items) != 1 || m.Items[0].ProductID != a.ID {
		t.Fatalf("记账后应自动补进本场明细，实际 %+v", m.Items)
	}
	if m.Items[0].UnitPrice != model.MustMoney("200") {
		t.Errorf("自动上架时应带出产品建议售价，实际 %s", m.Items[0].UnitPrice)
	}

	// 没填带去数量时不应出现"尚未填写"这类打扰提示
	if strings.Contains(body, "尚未填写带去数量") {
		t.Error("不预选是正常用法，不应提示未填带去数量")
	}

	// 一键沿用上一场：新建第二场，从第一场复制产品
	post(t, h, "/markets/new", url.Values{
		"name": {"沿用测试市集"}, "start_date": {"2025-07-08"}, "end_date": {"2025-07-08"},
	}, cookie)
	next, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "沿用测试"})
	nextID := next[0].ID
	code, _ = postHTMX(t, h, "/markets/"+strconv.FormatInt(nextID, 10)+"/items/copy-last",
		url.Values{}, cookie)
	if code != http.StatusOK {
		t.Fatalf("沿用上一场应 200，实际 %d", code)
	}
	copied, _ := svc.Store.MarketByID(ctx, nextID)
	if len(copied.Items) == 0 {
		t.Fatal("沿用上一场后应带上产品")
	}
	// 再点一次不应产生重复
	before := len(copied.Items)
	postHTMX(t, h, "/markets/"+strconv.FormatInt(nextID, 10)+"/items/copy-last", url.Values{}, cookie)
	again, _ := svc.Store.MarketByID(ctx, nextID)
	if len(again.Items) != before {
		t.Errorf("重复沿用不应产生重复产品，%d → %d", before, len(again.Items))
	}

	// 一键加入全部在售
	post(t, h, "/markets/new", url.Values{
		"name": {"批量加入市集"}, "start_date": {"2025-07-15"}, "end_date": {"2025-07-15"},
	}, cookie)
	bulk, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "批量加入"})
	bulkID := bulk[0].ID
	postHTMX(t, h, "/markets/"+strconv.FormatInt(bulkID, 10)+"/items/add-all", url.Values{}, cookie)
	full, _ := svc.Store.MarketByID(ctx, bulkID)
	if len(full.Items) < 2 {
		t.Errorf("加入全部在售应至少带上 2 个产品，实际 %d", len(full.Items))
	}
}

// TestAllInteractiveElementsResolve 全站操作体检。
//
// 背景：用户反馈"某个按钮点了没反应"，这类问题肉眼很难查全。
// 这里把每个页面上的所有交互元素抓出来，逐个检查：
//   - 表单 action / HTMX 请求地址：路由是否真的注册过（含方法是否匹配）
//   - HTMX 的 hx-target：目标元素在当前页面上是否存在
//   - 站内链接：是否指向已注册的路由
//
// 检查走 ServeMux.Handler，只做匹配、不执行处理器，因此不会改动数据。
func TestAllInteractiveElementsResolve(t *testing.T) {
	srv, svc, cfg := newTestServer(t)
	h := srv.Handler()
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	ctx := context.Background()

	// 准备一些真实 ID，覆盖带参数的页面
	products, _ := svc.Store.ListProducts(ctx, store.ProductFilter{})
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{})
	suppliers, _ := svc.Store.ListSuppliers(ctx, "", true)
	users, _ := svc.Store.ListUsers(ctx)
	if len(products) == 0 || len(markets) == 0 || len(users) == 0 {
		t.Fatal("演示数据不足，无法体检")
	}
	pid := strconv.FormatInt(products[0].ID, 10)
	mid := strconv.FormatInt(markets[0].ID, 10)

	pages := []string{
		"/", "/more", "/profile", "/settings", "/import", "/logs", "/users", "/users/new",
		"/users/" + strconv.FormatInt(users[0].ID, 10) + "/edit",
		"/users/" + strconv.FormatInt(users[0].ID, 10) + "/password",
		"/products", "/products/new", "/products/" + pid, "/products/" + pid + "/edit",
		"/products/labels",
		"/suppliers", "/suppliers/new", "/customers", "/customers/new",
		"/purchases", "/purchases/new",
		"/inventory", "/inventory/movements", "/inventory/adjust",
		"/group-orders", "/group-orders/new",
		"/markets", "/markets/new", "/markets/" + mid, "/markets/" + mid + "/edit",
		"/markets/" + mid + "/pos",
		"/reports/markets", "/reports/products", "/reports/inventory",
	}
	if len(suppliers) > 0 {
		pages = append(pages, "/suppliers/"+strconv.FormatInt(suppliers[0].ID, 10)+"/edit")
	}

	formRe := regexp.MustCompile(`(?s)<form[^>]*method="([a-zA-Z]+)"[^>]*action="([^"]*)"`)
	formRe2 := regexp.MustCompile(`(?s)<form[^>]*action="([^"]*)"[^>]*method="([a-zA-Z]+)"`)
	hxRe := regexp.MustCompile(`hx-(get|post|put|delete|patch)="([^"]+)"`)
	targetRe := regexp.MustCompile(`hx-target="([^"]+)"`)
	linkRe := regexp.MustCompile(`<a\b[^>]*href="([^"]+)"`)
	idRe := regexp.MustCompile(`id="([^"]+)"`)

	checkedRoutes, checkedTargets, checkedLinks := 0, 0, 0

	// 站点有兜底 404 路由（模式为 "/"），任何地址都能匹配到它，
	// 所以不能只看"有没有匹配"，必须要求匹配到的是带方法的具体路由。
	methods := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "HEAD": true,
	}

	// routeExists 检查 (方法, 地址) 是否注册了具体路由，返回命中的模式。
	routeExists := func(method, rawURL string) string {
		u := html.UnescapeString(strings.TrimSpace(rawURL))
		if u == "" || !strings.HasPrefix(u, "/") {
			return "" // 外链、锚点、javascript: 不检查
		}
		req := httptest.NewRequest(strings.ToUpper(method), u, nil)
		_, pattern := srv.mux.Handler(req)
		parts := strings.SplitN(pattern, " ", 2)
		if len(parts) != 2 || !methods[parts[0]] {
			return "" // 命中兜底路由，等同于没有这个路由
		}
		return pattern
	}

	// 自检：不存在的路由必须被判为不存在，否则这套体检就是空跑
	if routeExists(http.MethodPost, "/definitely-not-a-route") != "" {
		t.Fatal("体检自身失效：不存在的路由竟然匹配成功")
	}
	if routeExists(http.MethodPost, "/products") != "" {
		t.Fatal("体检自身失效：POST 到只支持 GET 的路由竟然匹配成功")
	}

	for _, page := range pages {
		code, body := get(t, h, page, cookie)
		if code != http.StatusOK {
			t.Errorf("页面 %s 应可访问，实际 %d", page, code)
			continue
		}
		if !strings.Contains(body, "<form") && !strings.Contains(body, "hx-") &&
			!strings.Contains(body, "<a ") {
			continue
		}

		// 当前页面有哪些元素 id，用于校验 hx-target
		ids := map[string]bool{}
		for _, m := range idRe.FindAllStringSubmatch(body, -1) {
			ids[m[1]] = true
		}

		// 1) 表单提交目标
		for _, m := range formRe.FindAllStringSubmatch(body, -1) {
			checkedRoutes++
			if routeExists(m[1], m[2]) == "" {
				t.Errorf("%s：表单提交到 %s %s，但没有注册这个路由（按钮会 404/405）", page, m[1], m[2])
			}
		}
		for _, m := range formRe2.FindAllStringSubmatch(body, -1) {
			checkedRoutes++
			if routeExists(m[2], m[1]) == "" {
				t.Errorf("%s：表单提交到 %s %s，但没有注册这个路由（按钮会 404/405）", page, m[2], m[1])
			}
		}

		// 2) HTMX 请求地址
		for _, m := range hxRe.FindAllStringSubmatch(body, -1) {
			checkedRoutes++
			if routeExists(m[1], m[2]) == "" {
				t.Errorf("%s：按钮请求 %s %s，但没有注册这个路由（点了不会有反应）", page, strings.ToUpper(m[1]), m[2])
			}
		}

		// 3) hx-target 必须存在于当前页面
		for _, m := range targetRe.FindAllStringSubmatch(body, -1) {
			sel := strings.TrimSpace(html.UnescapeString(m[1]))
			if !strings.HasPrefix(sel, "#") {
				continue // this / closest tr / next 这类相对选择器跳过
			}
			checkedTargets++
			if !ids[strings.TrimPrefix(sel, "#")] {
				t.Errorf("%s：hx-target=%s 指向的元素不存在，局部刷新会静默失效", page, sel)
			}
		}

		// 4) 站内链接
		for _, m := range linkRe.FindAllStringSubmatch(body, -1) {
			href := html.UnescapeString(strings.TrimSpace(m[1]))
			if strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") ||
				strings.HasPrefix(href, "tel:") || strings.HasPrefix(href, "javascript:") {
				continue
			}
			checkedLinks++
			if routeExists(http.MethodGet, href) == "" {
				t.Errorf("%s：链接 %s 没有对应路由（点了会 404）", page, href)
			}
		}
	}

	if checkedRoutes < 40 || checkedTargets < 5 || checkedLinks < 40 {
		t.Fatalf("体检覆盖不足：路由 %d、hx-target %d、链接 %d",
			checkedRoutes, checkedTargets, checkedLinks)
	}
	t.Logf("已体检 %d 个提交/请求地址、%d 个 hx-target、%d 个站内链接",
		checkedRoutes, checkedTargets, checkedLinks)
}

// TestDirectSaleOutbound 销售出库：市集之外的销售也能记，并统计出收入。
func TestDirectSaleOutbound(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	admin := mustUser(t, svc, "admin")

	// 建产品并入库 10 瓶，成本 50
	post(t, h, "/products/new", url.Values{
		"sku": {"DS-001"}, "name": {"直销测试酒"}, "unit": {"瓶"},
		"sale_price": {"398"}, "is_active": {"1"},
	}, cookie)
	p, _ := svc.Store.ProductBySKU(ctx, "DS-001")
	if err := svc.AdjustStock(ctx, service.AdjustInput{
		ProductID: p.ID, Qty: model.MustQty("10"), UnitCost: model.MustMoney("50"),
		Reason: model.ReasonOpening, OccurredOn: "2025-06-01",
	}, admin); err != nil {
		t.Fatal(err)
	}

	// 建一个客户
	post(t, h, "/customers/new", url.Values{
		"name": {"张先生"}, "phone": {"13800000000"}, "is_active": {"1"},
	}, cookie)
	customers, _ := svc.Store.ListCustomers(ctx, "张先生", false)
	if len(customers) == 0 {
		t.Fatal("客户未创建")
	}

	// 出入库登记页应当出现「销售出库」这个类型
	code, page := get(t, h, "/inventory/adjust", cookie)
	if code != http.StatusOK {
		t.Fatalf("出入库登记应可访问，实际 %d", code)
	}
	if !strings.Contains(page, "销售出库") {
		t.Fatal("出库类型里应当有「销售出库」")
	}
	if !strings.Contains(page, "销售出库（卖给了客户）") {
		t.Error("销售出库应出现在可选类型里")
	}
	if !strings.Contains(page, "库存变动有三个入口") {
		t.Error("页面上应说明采购入库/市集收银台/本页登记的关系")
	}
	if !strings.Contains(page, "name=\"sale_price\"") || !strings.Contains(page, "name=\"customer_id\"") {
		t.Error("销售出库应有售价与客户输入")
	}

	// 登记一笔销售出库：出库 2 瓶，售价 398，客户张先生
	today := store.Today()
	code, _ = post(t, h, "/inventory/adjust", url.Values{
		"product_id":  {strconv.FormatInt(p.ID, 10)},
		"direction":   {model.DirectionOut},
		"qty":         {"2"},
		"reason":      {model.ReasonDirectSale},
		"sale_price":  {"398"},
		"customer_id": {strconv.FormatInt(customers[0].ID, 10)},
		"occurred_on": {today},
		"note":        {"朋友介绍"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("销售出库应 303，实际 %d", code)
	}

	// 库存减少 2
	after, _ := svc.Store.ProductByID(ctx, p.ID)
	if after.StockQty != model.MustQty("8") {
		t.Errorf("销售出库后库存应为 8，实际 %s", after.StockQty)
	}

	// 流水里应当带上单价、客户与销售额
	movements, err := svc.Store.ListMovements(ctx, store.MovementFilter{ProductID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	var sale *model.StockMovement
	for i := range movements {
		if movements[i].Reason == model.ReasonDirectSale {
			sale = &movements[i]
		}
	}
	if sale == nil {
		t.Fatal("应写入一条销售出库流水")
	}
	if sale.SalePrice != model.MustMoney("398") {
		t.Errorf("销售单价应为 398，实际 %s", sale.SalePrice)
	}
	if sale.CustomerName != "张先生" {
		t.Errorf("客户应为张先生，实际 %q", sale.CustomerName)
	}
	// 销售额 = 2 × 398 = 796
	if got := sale.SaleAmount(); got != model.MustMoney("796") {
		t.Errorf("销售额应为 796，实际 %s", got)
	}

	// 库存页的直销汇总应统计到这笔
	code, page = get(t, h, "/inventory", cookie)
	if code != http.StatusOK {
		t.Fatalf("库存页应可访问，实际 %d", code)
	}
	if !strings.Contains(page, "本月直销出库") || !strings.Contains(page, "¥796.00") {
		t.Errorf("库存页应显示本月直销出库 ¥796.00")
	}

	// 流水页显示销售额
	_, movesPage := get(t, h, "/inventory/movements", cookie)
	if !strings.Contains(movesPage, "¥796.00") {
		t.Error("库存流水里应显示销售额")
	}

	// 非销售类型的出库不应带上售价（避免误统计）
	post(t, h, "/inventory/adjust", url.Values{
		"product_id": {strconv.FormatInt(p.ID, 10)},
		"direction":  {model.DirectionOut},
		"qty":        {"1"},
		"reason":     {model.ReasonMarketLoss},
		"sale_price": {"999"},
	}, cookie)
	movements, _ = svc.Store.ListMovements(ctx, store.MovementFilter{ProductID: p.ID})
	for _, m := range movements {
		if m.Reason == model.ReasonMarketLoss && m.SalePrice != 0 {
			t.Errorf("非销售出库不应记录售价，实际 %s", m.SalePrice)
		}
	}

	// 汇总只应包含销售出库那 2 笔数量的金额
	summary, err := svc.Store.DirectSaleSummaryBetween(ctx, "2000-01-01", today)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 1 || summary.Amount != model.MustMoney("796") {
		t.Errorf("直销汇总应为 1 笔 796，实际 %d 笔 %s", summary.Count, summary.Amount)
	}
}

// TestGroupOrderRecordsWithoutTouchingStock 线下团单只做记录，不动库存。
//
// 这类订单由大仓发货，不在本系统管理的仓库里，
// 所以无论怎么增删改状态，库存数量与流水都必须纹丝不动。
func TestGroupOrderRecordsWithoutTouchingStock(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 建两个产品并入库，用于确认库存不受影响
	post(t, h, "/products/new", url.Values{
		"sku": {"GO-001"}, "name": {"团单产品甲"}, "unit": {"瓶"},
		"sale_price": {"398"}, "is_active": {"1"},
	}, cookie)
	post(t, h, "/products/new", url.Values{
		"sku": {"GO-002"}, "name": {"团单产品乙"}, "unit": {"瓶"},
		"sale_price": {"268"}, "is_active": {"1"},
	}, cookie)
	a, _ := svc.Store.ProductBySKU(ctx, "GO-001")
	b, _ := svc.Store.ProductBySKU(ctx, "GO-002")
	admin := mustUser(t, svc, "admin")
	if err := svc.AdjustStock(ctx, service.AdjustInput{
		ProductID: a.ID, Qty: model.MustQty("50"), UnitCost: model.MustMoney("150"),
		Reason: model.ReasonOpening, OccurredOn: "2025-06-01",
	}, admin); err != nil {
		t.Fatal(err)
	}

	before, _ := svc.Store.ProductByID(ctx, a.ID)
	movementsBefore, _ := svc.Store.ListMovements(ctx, store.MovementFilter{ProductID: a.ID})
	logsBefore, _ := svc.Store.CountMovements(ctx, store.MovementFilter{})

	// 新建团单：两行产品，优惠 100，其它费用 50
	code, _ := post(t, h, "/group-orders/new", url.Values{
		"customer_name": {"某某公司"},
		"contact":       {"李经理"},
		"phone":         {"13800000000"},
		"order_date":    {"2025-07-01"},
		"ship_date":     {"2025-07-05"},
		"warehouse":     {"大仓"},
		"discount":      {"100"},
		"extra_fee":     {"50"},
		"note":          {"走大仓发货"},
		"product_id":    {strconv.FormatInt(a.ID, 10), strconv.FormatInt(b.ID, 10)},
		"product_name":  {"团单产品甲", "团单产品乙"},
		"item_sku":      {"GO-001", "GO-002"},
		"qty":           {"10", "20"},
		"unit":          {"瓶", "瓶"},
		"unit_price":    {"398", "268"},
		"item_note":     {"", "礼盒装"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("新建团单应 303，实际 %d", code)
	}

	orders, err := svc.Store.ListGroupOrders(ctx, store.GroupOrderFilter{Keyword: "某某公司"})
	if err != nil || len(orders) == 0 {
		t.Fatalf("团单未创建: %v", err)
	}
	o := orders[0]
	if o.Code == "" || !strings.HasPrefix(o.Code, "GT") {
		t.Errorf("团单号应形如 GT…，实际 %q", o.Code)
	}
	if len(o.Items) != 2 {
		t.Fatalf("应有 2 行明细，实际 %d", len(o.Items))
	}
	// 明细合计 10×398 + 20×268 = 3980 + 5360 = 9340；订单金额 9340 − 100 + 50 = 9290
	if got := o.Subtotal(); got != model.MustMoney("9340") {
		t.Errorf("明细合计应为 9340，实际 %s", got)
	}
	if got := o.Total(); got != model.MustMoney("9290") {
		t.Errorf("订单金额应为 9290，实际 %s", got)
	}
	if o.TotalQty() != model.MustQty("30") {
		t.Errorf("数量合计应为 30，实际 %s", o.TotalQty())
	}
	if o.Status != model.GroupDraft {
		t.Errorf("新团单应为草稿，实际 %s", o.Status)
	}

	// 关键：库存与流水完全不受影响
	after, _ := svc.Store.ProductByID(ctx, a.ID)
	if after.StockQty != before.StockQty {
		t.Errorf("团单不应改动库存：%s → %s", before.StockQty, after.StockQty)
	}
	movementsAfter, _ := svc.Store.ListMovements(ctx, store.MovementFilter{ProductID: a.ID})
	if len(movementsAfter) != len(movementsBefore) {
		t.Errorf("团单不应产生库存流水：%d → %d", len(movementsBefore), len(movementsAfter))
	}
	logsAfter, _ := svc.Store.CountMovements(ctx, store.MovementFilter{})
	if logsAfter != logsBefore {
		t.Errorf("团单不应产生任何库存流水：%d → %d", logsBefore, logsAfter)
	}

	// 改状态：草稿 → 已出货 → 已完成
	for _, st := range []string{model.GroupShipped, model.GroupDone} {
		code, _ = post(t, h, "/group-orders/"+strconv.FormatInt(o.ID, 10)+"/status",
			url.Values{"status": {st}}, cookie)
		if code != http.StatusSeeOther {
			t.Fatalf("改状态应 303，实际 %d", code)
		}
	}
	updated, _ := svc.Store.GroupOrderByID(ctx, o.ID)
	if updated.Status != model.GroupDone {
		t.Errorf("状态应为已完成，实际 %s", updated.Status)
	}
	after, _ = svc.Store.ProductByID(ctx, a.ID)
	if after.StockQty != before.StockQty {
		t.Error("改状态也不应改动库存")
	}

	// 列表页与详情页可访问，并能看到金额
	code, listPage := get(t, h, "/group-orders", cookie)
	if code != http.StatusOK {
		t.Fatalf("团单列表应可访问，实际 %d", code)
	}
	if !strings.Contains(listPage, "某某公司") || !strings.Contains(listPage, "¥9,290.00") {
		t.Error("列表应显示客户与订单金额")
	}
	code, detail := get(t, h, "/group-orders/"+strconv.FormatInt(o.ID, 10), cookie)
	if code != http.StatusOK {
		t.Fatalf("团单详情应可访问，实际 %d", code)
	}
	if !strings.Contains(detail, "不关联本系统的库存") {
		t.Error("详情页应说明不影响库存")
	}
	if !strings.Contains(detail, "大仓") {
		t.Error("详情页应显示发货仓")
	}

	// 汇总：只统计非取消的
	summary, err := svc.GroupOrderSummaryBetween(ctx, "2025-01-01", "2025-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Count != 1 || summary.Amount != model.MustMoney("9290") {
		t.Errorf("汇总应为 1 张 9290，实际 %d 张 %s", summary.Count, summary.Amount)
	}

	// 导出
	code, _ = get(t, h, "/export/group-orders.xlsx", cookie)
	if code != http.StatusOK {
		t.Errorf("团单导出应 200，实际 %d", code)
	}

	// 删除后库存依然不变
	code, _ = post(t, h, "/group-orders/"+strconv.FormatInt(o.ID, 10)+"/delete", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("删除应 303，实际 %d", code)
	}
	if left, _ := svc.Store.ListGroupOrders(ctx, store.GroupOrderFilter{Keyword: "某某公司"}); len(left) != 0 {
		t.Error("删除后不应再查到团单")
	}
	after, _ = svc.Store.ProductByID(ctx, a.ID)
	if after.StockQty != before.StockQty {
		t.Error("删除团单也不应改动库存")
	}
}

// TestGroupOrderPermissionAndValidation 团单的权限与校验。
func TestGroupOrderPermissionAndValidation(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 客户名称与明细缺失都要拦下
	code, _ := post(t, h, "/group-orders/new", url.Values{
		"order_date": {"2025-07-01"}, "qty": {"1"}, "unit_price": {"10"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("提交应 303（带错误提示），实际 %d", code)
	}
	if list, _ := svc.Store.ListGroupOrders(ctx, store.GroupOrderFilter{}); len(list) != 0 {
		t.Errorf("校验不通过时不应写入，实际 %d 张", len(list))
	}

	// 有查看权限但没有维护权限的账号：能看列表，不能新建
	if _, err := svc.CreateUser(ctx, "gviewer", "viewerpass1", "只看",
		model.RoleStaff, []string{PermGroupView}, mustUser(t, svc, "admin")); err != nil {
		t.Fatal(err)
	}
	viewer := doLogin(t, h, cfg, "gviewer", "viewerpass1")
	if code, _ := get(t, h, "/group-orders", viewer); code != http.StatusOK {
		t.Errorf("有查看权限应能打开列表，实际 %d", code)
	}
	if code, _ := get(t, h, "/group-orders/new", viewer); code != http.StatusForbidden {
		t.Errorf("没有维护权限不应能新建，实际 %d", code)
	}
}

// TestImportFromSpreadsheet 从表格导入产品、进货与出库。
//
// 对应真实场景：用户原来用 Excel 记进销存，要一次性搬进系统。
// 重点验证：给了产品编码就只按编码匹配（不同产品共用条形码很常见）、
// 重复导入不会产生重复数据、库存能按明细还原出来。
func TestImportFromSpreadsheet(t *testing.T) {
	srv, svc, cfg := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	admin := mustUser(t, svc, "admin")

	// 产品：其中两款故意共用同一个条形码（真实表格里很常见）
	// endpoint 是上传地址，filename 决定按 xlsx 还是 csv 解析
	upload := func(endpoint, filename string, rows [][]string) {
		t.Helper()
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		for _, r := range rows {
			if err := w.Write(r); err != nil {
				t.Fatal(err)
			}
		}
		w.Flush()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, err := mw.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(buf.Bytes()); err != nil {
			t.Fatal(err)
		}
		mw.Close()

		req := httptest.NewRequest(http.MethodPost, endpoint, &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 导入失败：%d %s", endpoint, rec.Code, rec.Body.String())
		}
	}

	productRows := [][]string{
		{"产品编码", "条形码", "产品名称", "货品简称", "品类", "品牌", "单位", "参考成本", "建议售价", "供应商"},
		{"SF-001", "832136002173", "邦得唯优小冰甜375ml", "邦小冰", "加拿大冰酒", "邦得唯优", "瓶", "55", "130", "北洲贸易"},
		{"SF-002", "832136001480", "邦得唯优洛尤凡375ml", "洛尤凡", "加拿大冰酒", "邦得唯优", "瓶", "220", "460", "北洲贸易"},
		{"SF-003", "832136001480", "邦得唯优维达尔375ml", "邦冰白", "加拿大冰酒", "邦得唯优", "瓶", "220", "460", "北洲贸易"},
	}
	upload("/import/products", "products.csv", productRows)

	// 演示数据里本来就有产品，这里只统计本次导入的（编码 SF- 开头）
	countImported := func() int {
		list, _ := svc.Store.ListProducts(ctx, store.ProductFilter{})
		n := 0
		for _, p := range list {
			if strings.HasPrefix(p.SKU, "SF-") {
				n++
			}
		}
		return n
	}
	if n := countImported(); n != 3 {
		t.Fatalf("共用条形码的产品也必须各自建一个，实际 %d 个", n)
	}
	p1, _ := svc.Store.ProductBySKU(ctx, "SF-001")
	if p1 == nil || p1.Barcode != "832136002173" {
		t.Fatal("产品编码与条形码应正确写入")
	}
	if p1.CostPrice != model.MustMoney("55") || p1.SalePrice != model.MustMoney("130") {
		t.Errorf("成本与售价应写入：%s / %s", p1.CostPrice, p1.SalePrice)
	}
	if p1.NameEn != "邦小冰" {
		t.Errorf("货品简称应写入别名，实际 %q", p1.NameEn)
	}
	if sups, _ := svc.Store.ListSuppliers(ctx, "北洲贸易", true); len(sups) == 0 {
		t.Error("供应商应自动创建")
	}
	// 第三款产品因条码被占用而未写入条形码，但不能影响导入
	p3, _ := svc.Store.ProductBySKU(ctx, "SF-003")
	if p3 == nil || p3.Barcode != "" {
		t.Errorf("条码被占用时应跳过条码但保留产品，实际 %q", p3.Barcode)
	}

	// 重复导入同一份文件：不能产生重复产品
	upload("/import/products", "products.csv", productRows)
	if again := countImported(); again != 3 {
		t.Errorf("重复导入不应新增产品，实际 %d 个", again)
	}

	// 进货：同一天同一供应商合并成一张采购单
	upload("/import/purchases", "purchases.csv", [][]string{
		{"日期", "供应商", "产品编码", "产品名称", "数量", "单价"},
		{"2026年6月29日", "北洲贸易", "SF-001", "邦小冰", "36", "55"},
		{"2026年6月29日", "北洲贸易", "SF-002", "洛尤凡", "10", "220"},
		{"2026/8/15", "北洲贸易", "SF-001", "邦小冰", "12", "55"},
	})
	// 演示数据里也有采购单，这里只数导入产生的（备注为「由表格导入」）
	purchases, _ := svc.Store.ListPurchases(ctx, store.PurchaseFilter{})
	importedOrders := 0
	for _, pur := range purchases {
		if strings.Contains(pur.Notes, "由表格导入") {
			importedOrders++
		}
	}
	if importedOrders != 2 {
		t.Fatalf("同一天同一供应商应合并成一张单，实际 %d 张", importedOrders)
	}
	afterPurchase, _ := svc.Store.ProductByID(ctx, p1.ID)
	if afterPurchase.StockQty != model.MustQty("48") {
		t.Errorf("进货确认后库存应为 48，实际 %s", afterPurchase.StockQty)
	}

	// 出库：市集销售 + 试饮 + 非市集的调拨/损耗
	upload("/import/outbound", "outbound.csv", [][]string{
		{"市集名称", "日期", "产品编码", "产品名称", "类型", "数量", "单价"},
		{"凤凰汇市集", "2026年8月7日", "SF-001", "邦小冰", "销售", "7", "130"},
		{"凤凰汇市集", "2026年8月7日", "SF-001", "邦小冰", "试饮", "2", ""},
		{"", "2026年8月16日", "SF-001", "邦小冰", "调拨", "3", ""},
		{"", "2026年9月22日", "SF-001", "邦小冰", "损耗", "1", ""},
	})
	// 演示数据里也有市集，这里按名称找导入创建的那一场
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "凤凰汇市集"})
	if len(markets) != 1 || markets[0].Name != "凤凰汇市集" {
		t.Fatalf("市集应按名称自动创建，实际 %+v", markets)
	}
	m, _ := svc.Store.MarketByID(ctx, markets[0].ID)
	if m.SaleCount() == 0 {
		t.Error("市集应写入现场销售记录")
	}
	if m.StartDate != "2026-08-07" {
		t.Errorf("市集日期应取明细里的日期，实际 %s", m.StartDate)
	}
	// 再次导入出库：不应重复建市集
	upload("/import/outbound", "outbound.csv", [][]string{
		{"市集名称", "日期", "产品编码", "产品名称", "类型", "数量", "单价"},
		{"凤凰汇市集", "2026年8月7日", "SF-001", "邦小冰", "销售", "1", "130"},
	})
	if again, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "凤凰汇市集"}); len(again) != 1 {
		t.Errorf("重复导入不应重复建市集，实际 %d 场", len(again))
	}

	// 调拨与损耗直接扣库存（不走市集）
	afterOut, _ := svc.Store.ProductByID(ctx, p1.ID)
	if afterOut.StockQty != model.MustQty("44") {
		t.Errorf("调拨 3 + 损耗 1 后应剩 44，实际 %s", afterOut.StockQty)
	}

	// 结算市集后才扣销售与试饮
	if err := svc.SettleMarket(ctx, m.ID, admin); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	// 48（进货）− 3（调拨）− 1（损耗）− 7（销售）− 2（试饮）− 1（重复导入那一笔销售）= 34
	final, _ := svc.Store.ProductByID(ctx, p1.ID)
	if final.StockQty != model.MustQty("34") {
		t.Errorf("结算后应剩 34，实际 %s", final.StockQty)
	}

	// 非市集出库的原因要正确归类
	movements, _ := svc.Store.ListMovements(ctx, store.MovementFilter{ProductID: p1.ID})
	seen := map[string]int{}
	for _, mv := range movements {
		seen[mv.Reason]++
	}
	if seen[model.ReasonTransferOut] == 0 {
		t.Error("调拨应记成调拨出库")
	}
	if seen[model.ReasonMarketLoss] == 0 {
		t.Error("仓库损耗应记成破损损耗")
	}
	if seen[model.ReasonMarketSale] == 0 || seen[model.ReasonMarketTasting] == 0 {
		t.Error("市集销售与试饮应在结算后写入流水")
	}
}

// TestDeleteMarket 删除市集：未结算可直接删，已结算必须先撤销结算。
//
// 用户反馈新建了测试市集却找不到删除入口——后端本来就有，
// 缺的是界面按钮，所以这里同时验证按钮存在与删除行为正确。
func TestDeleteMarket(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")
	admin := mustUser(t, svc, "admin")

	// 建一个测试市集，加上产品、记一笔销售、加一条费用
	post(t, h, "/markets/new", url.Values{
		"name": {"测试市集（待删除）"}, "start_date": {"2025-06-01"}, "end_date": {"2025-06-01"},
	}, cookie)
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "测试市集（待删除）"})
	if len(markets) == 0 {
		t.Fatal("市集未创建")
	}
	mid := markets[0].ID
	id := strconv.FormatInt(mid, 10)
	products, _ := svc.Store.ListProducts(ctx, store.ProductFilter{})
	pid := products[0].ID

	if _, err := svc.AddMarketProduct(ctx, mid, pid, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddMarketRecord(ctx, mid, service.MarketRecordInput{
		ProductID: pid, Kind: model.RecordSale, Qty: model.MustQty("1"),
	}, admin); err != nil {
		t.Fatal(err)
	}
	if err := svc.SaveMarketExpenses(ctx, mid, []model.MarketExpense{
		{Category: model.ExpenseBooth, Amount: model.MustMoney("100"), Calc: model.ExpenseCalcFixed},
	}, admin); err != nil {
		t.Fatal(err)
	}

	// 详情页与列表页都要有删除入口
	code, detail := get(t, h, "/markets/"+id, cookie)
	if code != http.StatusOK {
		t.Fatalf("市集详情应可访问，实际 %d", code)
	}
	if !strings.Contains(detail, "/markets/"+id+"/delete") {
		t.Error("市集详情页应有删除入口")
	}
	if !strings.Contains(detail, "删除这场市集") {
		t.Error("详情页应显示删除按钮")
	}
	_, list := get(t, h, "/markets", cookie)
	if !strings.Contains(list, "/markets/"+id+"/delete") {
		t.Error("市集列表应有删除入口")
	}

	// 未结算：可以直接删除，且不影响库存
	stockBefore, _ := svc.Store.ProductByID(ctx, pid)
	code, _ = post(t, h, "/markets/"+id+"/delete", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("删除应 303，实际 %d", code)
	}
	if gone, _ := svc.Store.MarketByID(ctx, mid); gone != nil {
		t.Error("删除后不应再查到市集")
	}
	// 明细、记录、费用一并清掉
	items, _ := svc.Store.MarketItems(ctx, mid)
	if len(items) != 0 {
		t.Errorf("明细应一并删除，实际 %d 行", len(items))
	}
	records, _ := svc.Store.MarketRecords(ctx, mid, 0)
	if len(records) != 0 {
		t.Errorf("现场记录应一并删除，实际 %d 条", len(records))
	}
	stockAfter, _ := svc.Store.ProductByID(ctx, pid)
	if stockAfter.StockQty != stockBefore.StockQty {
		t.Errorf("未结算的市集删除不应影响库存：%s → %s", stockBefore.StockQty, stockAfter.StockQty)
	}

	// 已结算的市集：删除被拒，撤销结算后才能删
	post(t, h, "/markets/new", url.Values{
		"name": {"测试市集（已结算）"}, "start_date": {"2025-06-02"}, "end_date": {"2025-06-02"},
	}, cookie)
	settled, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{Keyword: "测试市集（已结算）"})
	sid := settled[0].ID
	sidStr := strconv.FormatInt(sid, 10)
	if _, err := svc.AddMarketProduct(ctx, sid, pid, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddMarketRecord(ctx, sid, service.MarketRecordInput{
		ProductID: pid, Kind: model.RecordSale, Qty: model.MustQty("1"),
	}, admin); err != nil {
		t.Fatal(err)
	}
	if err := svc.SettleMarket(ctx, sid, admin); err != nil {
		t.Fatal(err)
	}

	// 已结算时界面改成提示先撤销结算，不再显示删除按钮
	_, settledDetail := get(t, h, "/markets/"+sidStr, cookie)
	if !strings.Contains(settledDetail, "撤销结算") {
		t.Error("已结算的市集应提示先撤销结算")
	}
	if strings.Contains(settledDetail, "/markets/"+sidStr+"/delete") {
		t.Error("已结算的市集不应显示删除按钮")
	}
	// 注意：失败也会 303（带错误提示跳回），所以只能看真实状态
	settledStock, _ := svc.Store.ProductByID(ctx, pid)
	post(t, h, "/markets/"+sidStr+"/delete", url.Values{}, cookie)
	if still, _ := svc.Store.MarketByID(ctx, sid); still == nil {
		t.Fatal("已结算的市集不应被删掉")
	}
	// 库存也不应被动过（结算时已扣，删除尝试不该再动）
	if after, _ := svc.Store.ProductByID(ctx, pid); after.StockQty != settledStock.StockQty {
		t.Errorf("尝试删除已结算市集不应改动库存：%s → %s",
			settledStock.StockQty, after.StockQty)
	}

	// 撤销结算 → 库存冲回 → 此时可以删除
	if err := svc.UnsettleMarket(ctx, sid, admin); err != nil {
		t.Fatal(err)
	}
	code, _ = post(t, h, "/markets/"+sidStr+"/delete", url.Values{}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("撤销结算后应可删除，实际 %d", code)
	}
	if gone, _ := svc.Store.MarketByID(ctx, sid); gone != nil {
		t.Error("撤销结算后应能删除市集")
	}
}

// TestProductImageOnSave 图片随产品表单一起提交。
//
// 用户反馈"插入产品图片无法保存"：原因是图片上传表单被嵌套在产品表单里，
// HTML 不允许表单嵌套，浏览器会忽略内层 form，文件根本没被提交。
// 现在图片是主表单的一部分，这里验证真实路径：
// 一次提交既保存产品也保存图片、替换会清掉旧文件、勾选删除会清空。
func TestProductImageOnSave(t *testing.T) {
	srv, svc, cfg := newTestServer(t)
	h := srv.Handler()
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 造一张 2400×1600 的 PNG，验证服务端会缩到长边 1280
	src := image.NewRGBA(image.Rect(0, 0, 2400, 1600))
	for y := 0; y < 1600; y += 3 {
		for x := 0; x < 2400; x += 3 {
			src.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 120, A: 255})
		}
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, src); err != nil {
		t.Fatal(err)
	}
	var jpgBytes bytes.Buffer
	small := image.NewRGBA(image.Rect(0, 0, 400, 300))
	if err := jpeg.Encode(&jpgBytes, small, nil); err != nil {
		t.Fatal(err)
	}

	// submit 提交产品表单，fields 为文本字段，file 为空表示不带图片
	submit := func(target string, fields map[string]string, filename string, content []byte) *httptest.ResponseRecorder {
		t.Helper()
		body := &bytes.Buffer{}
		mw := multipart.NewWriter(body)
		for k, v := range fields {
			if err := mw.WriteField(k, v); err != nil {
				t.Fatal(err)
			}
		}
		if filename != "" {
			fw, err := mw.CreateFormFile("image", filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write(content); err != nil {
				t.Fatal(err)
			}
		}
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, target, body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	base := map[string]string{
		"sku": "IMGS-001", "name": "图片随保存", "unit": "瓶",
		"sale_price": "200", "is_active": "1",
	}
	uploadDir := func() string { return filepath.Join(cfg.DataDir, "uploads", "products") }
	countFiles := func() int {
		items, err := os.ReadDir(uploadDir())
		if err != nil {
			return 0
		}
		return len(items)
	}

	// 1) 新建 + 图片一次提交（以前必须先保存产品再回来传）
	if rec := submit("/products/new", base, "wine.png", pngBytes.Bytes()); rec.Code != http.StatusSeeOther {
		t.Fatalf("新建应 303，实际 %d %s", rec.Code, rec.Body.String())
	}
	product, _ := svc.Store.ProductBySKU(ctx, "IMGS-001")
	if product == nil {
		t.Fatal("产品未创建")
	}
	if product.ImageURL == "" {
		t.Fatal("图片应随产品一起保存")
	}
	if got := countFiles(); got != 1 {
		t.Fatalf("上传目录应有 1 个文件，实际 %d", got)
	}
	firstPath := filepath.Join(cfg.DataDir, filepath.FromSlash(strings.TrimPrefix(product.ImageURL, "/")))
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("图片文件应存在: %v", err)
	}
	// 长边应被压到 1280
	if f, err := os.Open(firstPath); err == nil {
		if img, _, err := image.Decode(f); err == nil {
			if b := img.Bounds(); b.Dx() > 1280 || b.Dy() > 1280 {
				t.Errorf("图片应压缩到长边 1280，实际 %d×%d", b.Dx(), b.Dy())
			}
		}
		f.Close()
	}

	pid := strconv.FormatInt(product.ID, 10)
	editFields := func(extra map[string]string) map[string]string {
		m := map[string]string{
			"sku": "IMGS-001", "name": "图片随保存", "unit": "瓶",
			"sale_price": "200", "is_active": "1",
			"image_url": product.ImageURL,
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	// 2) 换一张图：旧文件要被清掉，不能越攒越多
	if rec := submit("/products/"+pid+"/edit", editFields(nil), "wine2.jpg", jpgBytes.Bytes()); rec.Code != http.StatusSeeOther {
		t.Fatalf("替换图片应 303，实际 %d", rec.Code)
	}
	updated, _ := svc.Store.ProductByID(ctx, product.ID)
	if updated.ImageURL == product.ImageURL {
		t.Error("替换后图片地址应变化")
	}
	if got := countFiles(); got != 1 {
		t.Errorf("替换后上传目录应仍只有 1 个文件（旧图已删），实际 %d", got)
	}
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Error("被替换掉的旧图片文件应已删除")
	}

	// 3) 非图片文件：产品信息照常保存，但要给出错误提示
	rec := submit("/products/"+pid+"/edit", editFields(map[string]string{
		"brand": "测试品牌",
	}), "fake.png", []byte("这不是图片"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("应 303 带错误提示，实际 %d", rec.Code)
	}
	after, _ := svc.Store.ProductByID(ctx, product.ID)
	if after.ImageURL != updated.ImageURL {
		t.Error("图片无效时不应改动已有图片")
	}
	if after.Brand != "测试品牌" {
		t.Error("图片无效不应影响产品其它字段的保存")
	}
	if page := flashHTML(t, h, rec, "/products/"+pid+"/edit", cookie); !strings.Contains(page, "不是有效的图片") {
		t.Error("应提示图片无效")
	}

	// 4) 勾选删除图片
	keep := updated.ImageURL
	if rec := submit("/products/"+pid+"/edit", editFields(map[string]string{"remove_image": "1"}), "", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("删除图片应 303，实际 %d", rec.Code)
	}
	cleared, _ := svc.Store.ProductByID(ctx, product.ID)
	if cleared.ImageURL != "" {
		t.Errorf("勾选删除后图片地址应为空，实际 %q", cleared.ImageURL)
	}
	if got := countFiles(); got != 0 {
		t.Errorf("删除后上传目录应为空，实际 %d 个文件", got)
	}
	_ = keep
}

// flashHTML 模拟浏览器跟随跳转：带上响应里的 Flash Cookie 再请求一次，
// 返回渲染后的页面（提示文字就在里面）。Flash 值是签名过的，所以不解码，直接看页面。
func flashHTML(t *testing.T, h http.Handler, rec *httptest.ResponseRecorder, path string, session *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(session)
	for _, c := range rec.Result().Cookies() {
		if strings.Contains(c.Name, "flash") {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Body.String()
}

// TestGroupOrderFormSubmitsLines 团单明细必须真的能被提交。
//
// 用户反馈"新建团单填完信息却提示请至少填写一行产品明细"：
// 明细的提交字段原本放在一个多根 <template x-for> 里，
// Alpine 只克隆第一个根元素，导致只有 product_id 提交、其余全丢。
// 这里断言表单里的明细输入框本身带 name，能随表单一起提交。
func TestGroupOrderFormSubmitsLines(t *testing.T) {
	h, _, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	code, page := get(t, h, "/group-orders/new", cookie)
	if code != http.StatusOK {
		t.Fatalf("新建团单页应可访问，实际 %d", code)
	}

	// 明细行里的每个字段都要是可提交的表单控件（带 name）
	for _, field := range []string{"product_id", "product_name", "item_sku", "qty", "unit", "unit_price", "item_note"} {
		if !strings.Contains(page, `name="`+field+`"`) {
			t.Errorf("明细字段 %s 没有 name 属性，不会被提交", field)
		}
	}

	// 不能再出现"单独一段隐藏字段模板"的写法（多根模板会丢字段）
	hiddenTemplate := regexp.MustCompile(`(?is)<template[^>]*x-for[^>]*>\s*<input type="hidden" name="product_id"`)
	if hiddenTemplate.MatchString(page) {
		t.Error("明细提交字段不应另开隐藏字段模板（Alpine 只渲染第一个根元素）")
	}

	// 客户与日期字段照旧可提交
	for _, field := range []string{"customer_id", "customer_name", "order_date", "warehouse", "discount", "extra_fee"} {
		if !strings.Contains(page, `name="`+field+`"`) {
			t.Errorf("表单缺少字段 %s", field)
		}
	}
}

// TestPerformanceGuardrails 性能相关的护栏。
//
// 用户反馈切换页面有点卡。排查发现两处：
//  1. 328KB 的扫码库（zxing）写在了公共布局里，每个页面都加载，
//     而其实只有收银台和产品表单需要；
//  2. 所有响应都没有压缩，列表页 HTML 有 25-53KB，CSS 约 60KB。
//
// 这里把结论固定下来，避免以后又被改回去。
func TestPerformanceGuardrails(t *testing.T) {
	h, _, cfg := testApp(t)
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 1) 重资源不能出现在公共布局里
	raw, err := os.ReadFile("../assets/templates/layouts/base.html")
	if err != nil {
		t.Fatalf("读取布局失败: %v", err)
	}
	layout := string(raw)
	// 只允许"按需引入"（同一行带 {{if .NeedsScanner}} 之类的条件），
	// 不允许无条件引入重资源
	for _, line := range strings.Split(layout, "\n") {
		if !strings.Contains(line, "<script") {
			continue
		}
		for _, heavy := range []string{"zxing.min.js", "scan.js", "chart.umd.js", "jsbarcode.min.js"} {
			if strings.Contains(line, heavy) && !strings.Contains(line, "{{if") {
				t.Errorf("公共布局无条件引入了 %s：只有个别页面需要，会拖慢所有页面", heavy)
			}
		}
	}

	// 2) 需要扫码的页面要有，其它页面不能有
	withScanner := []string{"/markets/1/pos", "/products/new"}
	withoutScanner := []string{"/", "/markets", "/products", "/inventory", "/settings"}
	for _, p := range withScanner {
		_, body := get(t, h, p, cookie)
		if !strings.Contains(body, "zxing.min.js") {
			t.Errorf("%s 需要扫码功能，应引入 zxing", p)
		}
	}
	for _, p := range withoutScanner {
		_, body := get(t, h, p, cookie)
		if strings.Contains(body, "zxing.min.js") {
			t.Errorf("%s 不需要扫码，不应引入 328KB 的 zxing", p)
		}
	}

	// 3) 文本响应要支持 gzip，且内容能正确解压
	req := httptest.NewRequest(http.MethodGet, "/markets", nil)
	req.AddCookie(cookie)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("支持 gzip 的客户端应收到压缩响应，实际 Content-Encoding=%q", enc)
	}
	if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
		t.Error("压缩响应必须带 Vary: Accept-Encoding")
	}
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("响应声称是 gzip 但解不开: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("解压失败: %v", err)
	}
	if !bytes.Contains(plain, []byte("</html>")) {
		t.Error("解压后应是完整的 HTML 页面")
	}
	if len(plain) <= rec.Body.Len() {
		t.Errorf("压缩后应更小：原始 %d，压缩 %d", len(plain), rec.Body.Len())
	}

	// 不接受 gzip 的客户端要拿到未压缩内容
	req2 := httptest.NewRequest(http.MethodGet, "/markets", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if enc := rec2.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("未声明支持 gzip 时不应压缩，实际 %q", enc)
	}

	// 4) Service Worker 预缓存列表里的资源必须真实存在
	swRaw, err := os.ReadFile("../assets/static/sw.js")
	if err != nil {
		t.Fatalf("读取 sw.js 失败: %v", err)
	}
	listRe := regexp.MustCompile(`(?s)const PRECACHE = \[(.*?)\];`)
	m := listRe.FindStringSubmatch(string(swRaw))
	if m == nil {
		t.Fatal("sw.js 里没有找到 PRECACHE 列表")
	}
	urls := regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1)
	if len(urls) == 0 {
		t.Fatal("PRECACHE 列表为空")
	}
	for _, u := range urls {
		// 预缓存里有一个资源取不到，cache.addAll 会整体失败，离线缓存就废了
		path := filepath.Join("..", "assets", filepath.FromSlash(strings.TrimPrefix(u[1], "/")))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("Service Worker 预缓存了不存在的资源 %s（会导致 cache.addAll 整体失败）", u[1])
		}
	}
}

// TestAllAccountsActionsAreLogged 所有账号的操作都要有记录，且主账号能按人查到。
//
// 用户反馈"其他账号的操作记录看不到"：实际是两个问题叠加——
// 产品、供应商、客户这些模块压根没有写日志，日志页也没有按操作人筛选。
func TestAllAccountsActionsAreLogged(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	admin := doLogin(t, h, cfg, "admin", "admin123")

	// 建一个员工账号（有产品/库存/往来单位权限）
	post(t, h, "/users/new", url.Values{
		"username": {"stafflog"}, "password": {"staffpass123"}, "full_name": {"李四"},
		"role": {"staff"}, "is_active": {"1"},
		// 故意用逗号串，验证"逗号分隔"这种写法也能被接受
		"permissions": {"product.view,product.manage,partner.view,partner.manage"},
	}, admin)
	staff := doLogin(t, h, cfg, "stafflog", "staffpass123")

	// 员工做一串操作
	post(t, h, "/products/new", url.Values{
		"sku": {"LOG-001"}, "name": {"日志测试产品"}, "unit": {"瓶"},
		"sale_price": {"200"}, "is_active": {"1"},
	}, staff)
	product, _ := svc.Store.ProductBySKU(ctx, "LOG-001")
	if product == nil {
		t.Fatal("员工应有权限新建产品")
	}
	post(t, h, "/products/"+strconv.FormatInt(product.ID, 10)+"/edit", url.Values{
		"sku": {"LOG-001"}, "name": {"日志测试产品改名"}, "unit": {"瓶"},
		"sale_price": {"220"}, "is_active": {"1"},
	}, staff)
	post(t, h, "/customers/new", url.Values{"name": {"日志测试客户"}, "is_active": {"1"}}, staff)

	// 数据库里要有这个账号的操作记录，且动作要能区分
	logs, err := svc.Store.ListLogsFiltered(ctx, store.LogFilter{Keyword: "日志测试"})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("产品与客户的操作必须写日志，否则主账号无从追溯")
	}
	actions := map[string]bool{}
	actors := map[string]bool{}
	for _, l := range logs {
		actions[l.Action] = true
		actors[l.Username] = true
	}
	for _, want := range []string{"新建产品", "修改产品", "新建客户"} {
		if !actions[want] {
			t.Errorf("缺少动作日志：%s（实际记录：%v）", want, actions)
		}
	}
	if !actors["李四"] {
		t.Errorf("日志要记到具体账号上，实际操作人：%v", actors)
	}

	// 主账号按操作人筛选，要能只看到这个员工的操作
	filtered, err := svc.Store.ListLogsFiltered(ctx, store.LogFilter{Keyword: "李四"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) == 0 {
		t.Error("按操作人搜索应能查到该账号的记录")
	}

	// 日志页本身可用，并且带上了筛选控件
	code, page := get(t, h, "/logs?q=日志测试", admin)
	if code != http.StatusOK {
		t.Fatalf("日志页应可访问，实际 %d", code)
	}
	for _, need := range []string{`name="actor"`, `name="action"`, `name="from"`, `name="q"`, "操作人"} {
		if !strings.Contains(page, need) {
			t.Errorf("日志页缺少筛选控件 %s", need)
		}
	}
	if !strings.Contains(page, "李四") {
		t.Error("按关键词搜索应能显示该员工的记录")
	}

	// 系统设置与改密码这类敏感操作也要留痕
	post(t, h, "/settings", url.Values{
		"company_name": {"SoulFound"}, "currency": {"CNY"}, "currency_symbol": {"¥"},
	}, admin)
	post(t, h, "/profile/password", url.Values{
		"old_password":     {"staffpass123"},
		"new_password":     {"staffpass456"},
		"confirm_password": {"staffpass456"},
	}, staff)

	all, err := svc.Store.ListLogsFiltered(ctx, store.LogFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, l := range all {
		seen[l.Action] = true
	}
	if !seen["修改系统设置"] {
		t.Error("修改系统设置应写日志")
	}
	if !seen["修改密码"] {
		t.Error("修改密码应写日志")
	}
	if !seen["新建用户"] {
		t.Error("新建用户应写日志")
	}
}

// TestProductImagesEverywhere 产品图该显示的地方都要显示，并且能悬停放大。
//
// （主题切换功能按用户要求已撤掉，相关断言一并移除。）
func TestProductImagesEverywhere(t *testing.T) {
	h, svc, cfg := testApp(t)
	ctx := context.Background()
	cookie := doLogin(t, h, cfg, "admin", "admin123")

	// 悬停放大的脚本与属性要在
	js, err := os.ReadFile("../assets/static/js/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, need := range []string{"img-zoom", "data-zoom"} {
		if !strings.Contains(string(js), need) {
			t.Errorf("app.js 缺少 %s", need)
		}
	}
	css, err := os.ReadFile("../assets/static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), "#img-zoom") {
		t.Error("样式里缺少放大浮层")
	}
	// 主题功能已撤掉，不应该再有残留
	for _, gone := range []string{`data-theme="dark"`, "erpApplyTheme", "theme-choice"} {
		if strings.Contains(string(css), gone) || strings.Contains(string(js), gone) {
			t.Errorf("主题功能应已撤掉，但仍残留 %s", gone)
		}
	}

	// 给一个产品配上图片
	p, _ := svc.Store.ProductBySKU(ctx, "ICE-VID-375")
	if p == nil {
		products, _ := svc.Store.ListProducts(ctx, store.ProductFilter{})
		if len(products) == 0 {
			t.Fatal("没有产品可用于测试")
		}
		p = &products[0]
	}
	p.ImageURL = "/uploads/products/test.png"
	if err := svc.Store.UpdateProduct(ctx, p); err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"/products", "/inventory"} {
		_, body := get(t, h, target, cookie)
		if !strings.Contains(body, p.ImageURL) {
			t.Errorf("%s 应显示产品图", target)
		}
		if !strings.Contains(body, `data-zoom="`+p.ImageURL+`"`) {
			t.Errorf("%s 的产品图应可悬停放大", target)
		}
	}

	// 市集明细也要有缩略图
	markets, _ := svc.Store.ListMarkets(ctx, store.MarketFilter{})
	for _, m := range markets {
		if len(m.Items) == 0 {
			continue
		}
		_, body := get(t, h, "/markets/"+strconv.FormatInt(m.ID, 10), cookie)
		if !strings.Contains(body, "prod-thumb") {
			t.Error("市集明细的产品列应显示缩略图")
		}
		break
	}
}

// TestAppIcons 应用图标的基本要求。
//
// 用户反馈图标太丑（原来是两行文字，缩到手机桌面 60px 就糊了），
// 现在换成酒瓶剪影。这里守住几条硬要求，避免以后换图时踩坑：
// 尺寸正确、maskable 铺满整块（不能有透明边，否则系统裁切后露底色）、
// iOS 图标不带透明通道。
func TestAppIcons(t *testing.T) {
	dir := "../assets/static/icons"
	cases := []struct {
		name    string
		size    int
		noAlpha bool // 必须是不透明图
	}{
		{"icon-192.png", 192, false},
		{"icon-512.png", 512, false},
		{"maskable-512.png", 512, true},
		{"apple-touch-icon.png", 180, true},
		{"favicon-32.png", 32, false},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("图标缺失：%s（PWA 与 Service Worker 预缓存都依赖它）", c.name)
			continue
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Errorf("%s 不是有效图片: %v", c.name, err)
			continue
		}
		b := img.Bounds()
		if b.Dx() != c.size || b.Dy() != c.size {
			t.Errorf("%s 尺寸应为 %d×%d，实际 %d×%d", c.name, c.size, c.size, b.Dx(), b.Dy())
		}
		// 取中心像素确认不是空白图
		if _, _, _, a := img.At(b.Dx()/2, b.Dy()/2).RGBA(); a == 0 {
			t.Errorf("%s 中心是透明的，图标内容是空的", c.name)
		}
		if c.noAlpha {
			if _, _, _, a := img.At(1, 1).RGBA(); a < 65535 {
				t.Errorf("%s 不能有透明区域：maskable 与 iOS 图标会被系统裁切，"+
					"透明处会露出系统底色", c.name)
			}
		}
	}

	// manifest 与页面里引用的图标必须存在（这些文件名不能随意改）
	mf, err := os.ReadFile("../assets/static/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"/static/icons/([a-z0-9-]+\.png)"`)
	found := re.FindAllStringSubmatch(string(mf), -1)
	if len(found) == 0 {
		t.Fatal("manifest 里没有引用任何图标")
	}
	for _, m := range found {
		if _, err := os.Stat(filepath.Join(dir, m[1])); err != nil {
			t.Errorf("manifest 引用了不存在的图标 %s", m[1])
		}
	}
}

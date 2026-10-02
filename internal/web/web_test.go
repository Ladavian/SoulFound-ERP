package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	return srv.Handler(), svc, cfg
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

	if _, err := svc.CreateUser(ctx, "staff1", "staff123", "店员小王", model.RoleStaff, nil); err != nil {
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
		"库存成本", "库存总值", "毛利", "净利润", "净利率"}
	staffPages := []string{"/", "/markets", "/products", "/products/1",
		"/inventory", "/inventory/movements"}
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

	// 修改角色与状态
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/edit", url.Values{
		"full_name": {"小李（升职）"},
		"role":      {model.RoleManager},
		"is_active": {"1"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("修改用户应 303，实际 %d", code)
	}
	u, _ = svc.Store.UserByID(ctx, u.ID)
	if u.Role != model.RoleManager || u.FullName != "小李（升职）" {
		t.Errorf("用户未被正确更新: %+v", u)
	}

	// 重置密码后应能用新密码登录
	code, _ = post(t, h, "/users/"+strconv.FormatInt(u.ID, 10)+"/password", url.Values{
		"password":         {"newpass99"},
		"confirm_password": {"newpass99"},
	}, cookie)
	if code != http.StatusSeeOther {
		t.Fatalf("重置密码应 303，实际 %d", code)
	}
	doLogin(t, h, cfg, "xiaoli", "newpass99")

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

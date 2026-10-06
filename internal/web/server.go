package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"icewine-erp/internal/assets"
	"icewine-erp/internal/config"
	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
)

type ctxKey int

const (
	ctxUserKey ctxKey = iota
	ctxSettingsKey
	ctxFlashKey
)

const flashCookie = "erp_flash"

// Server HTTP 服务。
type Server struct {
	cfg *config.Config
	svc *service.Service
	rnd *Renderer
	mux *http.ServeMux
}

// New 构造 HTTP 服务。
func New(cfg *config.Config, svc *service.Service) (*Server, error) {
	// 把权限清单交给服务层做合法性校验（service 不反向依赖 web）
	service.RegisterPermissions(AllPermissions())

	// Go 内置的 MIME 表里没有 .webmanifest，不注册的话会以 text/plain 返回，
	// 部分浏览器会因此忽略 PWA manifest。
	if err := mime.AddExtensionType(".webmanifest", "application/manifest+json"); err != nil {
		return nil, fmt.Errorf("注册 manifest 类型失败: %w", err)
	}

	sym := cfg.CurrencySymbol
	if st, err := svc.Store.Settings(context.Background()); err == nil && st.CurrencySymbol != "" {
		sym = st.CurrencySymbol
	}
	webDir := cfg.WebDir
	if webDir == "" {
		webDir = "internal/assets"
	}
	s := &Server{
		cfg: cfg,
		svc: svc,
		rnd: NewRenderer(webDir, cfg.Dev, sym),
		mux: http.NewServeMux(),
	}
	s.routes()
	return s, nil
}

// Handler 组装中间件与路由。
func (s *Server) Handler() http.Handler {
	var h http.Handler = s.mux
	h = s.recoverer(h)
	h = s.context(h)
	// gzip 放最外层：HTML、CSS、JS、JSON 都能压到约 1/4，手机端切页明显更快
	h = withGzip(h)
	return h
}

// ---------------------------------------------------------------- 中间件

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("请求异常 %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				if isHTMX(r) {
					w.Header().Set("HX-Reswap", "none")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				s.rnd.RenderError(w, http.StatusInternalServerError, "服务器内部错误",
					"操作未能完成，请重试；如果反复出现请查看服务器日志。", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestTimeout 给每个请求加上超时。
//
// SQLite 的连接池只有 1 条连接，万一将来又有人在事务里误用连接池，
// 没有超时就会永久挂住整个服务；加上超时后会变成一条明确的错误日志。
const requestTimeout = 30 * time.Second

func (s *Server) context(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		if st, err := s.svc.Store.Settings(ctx); err == nil {
			ctx = context.WithValue(ctx, ctxSettingsKey, st)
		}

		if cookie, err := r.Cookie(s.cfg.SessionCookie); err == nil && cookie.Value != "" {
			if uid, ok := s.svc.ParseSession(cookie.Value); ok {
				if user, err := s.svc.Store.UserByID(ctx, uid); err == nil && user != nil && user.IsActive {
					ctx = context.WithValue(ctx, ctxUserKey, user)
				}
			}
		}

		// 一次性提示：读取后立即清除
		if cookie, err := r.Cookie(flashCookie); err == nil && cookie.Value != "" {
			if payload, ok := s.svc.VerifyValue(cookie.Value); ok {
				ctx = context.WithValue(ctx, ctxFlashKey, decodeFlash(payload))
			}
			http.SetCookie(w, &http.Cookie{
				Name: flashCookie, Value: "", Path: "/", MaxAge: -1,
				HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.CookieSecure,
			})
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------------------------------------------------------------- Flash 提示

func encodeFlash(level, text string) string {
	text = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(text)
	return level + "\t" + text
}

// decodeFlash 解析已签名的 Flash 内容。
//
// 提示文本可能包含制表符等 Cookie 非法字符，因此 setFlash 先做 base64
// 编码，这里再解回来，避免 Cookie 被 net/http 丢弃。
func decodeFlash(payload string) []Flash {
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	payload = string(raw)
	var out []Flash
	for _, line := range strings.Split(payload, "\x1e") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		out = append(out, Flash{Level: parts[0], Text: parts[1]})
	}
	return out
}

func (s *Server) setFlash(w http.ResponseWriter, level, text string) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(encodeFlash(level, text)))
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: s.svc.SignValue(encoded),
		Path: "/", MaxAge: 60, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.cfg.CookieSecure,
	})
}

// redirect 跳转（HTMX 请求使用 HX-Redirect 触发整页跳转）。
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, target string) {
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ok 带成功提示跳转。
func (s *Server) ok(w http.ResponseWriter, r *http.Request, target, message string) {
	if message != "" {
		s.setFlash(w, "success", message)
	}
	s.redirect(w, r, target)
}

// back 跳回来源页（仅限同站点）。
func (s *Server) back(r *http.Request, fallback string) string {
	ref := r.Referer()
	if ref == "" {
		return fallback
	}
	parsed, err := url.Parse(ref)
	if err != nil || parsed.Host != r.Host {
		return fallback
	}
	return parsed.String()
}

// fail 统一错误处理：业务错误提示用户，程序错误记日志。
func (s *Server) fail(w http.ResponseWriter, r *http.Request, fallback string, err error) {
	if err == nil {
		return
	}
	if msg, ok := service.AsUserError(err); ok {
		s.setFlash(w, "error", msg)
		s.redirect(w, r, s.back(r, fallback))
		return
	}
	log.Printf("处理 %s %s 失败: %v", r.Method, r.URL.Path, err)
	s.setFlash(w, "error", "操作失败："+err.Error())
	s.redirect(w, r, s.back(r, fallback))
}

// failPartial 在 HTMX 局部刷新场景下返回错误片段。
func (s *Server) failPartial(w http.ResponseWriter, r *http.Request, page, name string, data map[string]any, err error) {
	msg := "操作失败"
	if m, ok := service.AsUserError(err); ok {
		msg = m
	} else {
		log.Printf("处理 %s %s 失败: %v", r.Method, r.URL.Path, err)
		msg = err.Error()
	}
	if data == nil {
		data = map[string]any{}
	}
	data["Error"] = msg
	if e := s.rnd.RenderPartial(w, page, name, data); e != nil {
		http.Error(w, msg, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------- 权限包装

// guard 要求登录（perm 非空时还需具备权限）。
func (s *Server) guard(perm string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userFrom(r)
		if user == nil {
			if isHTMX(r) {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if perm != "" && !permsForUser(user).Has(perm) {
			s.forbidden(w, r, perm)
			return
		}
		h(w, r)
	})
}

func (s *Server) forbidden(w http.ResponseWriter, r *http.Request, perm string) {
	if isHTMX(r) {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusForbidden)
		return
	}
	data := s.newPage(r, "无权访问", "")
	detail := "当前账号角色为「" + model.RoleLabel(userFrom(r).Role) + "」，没有执行该操作的权限。"
	s.rnd.RenderError(w, http.StatusForbidden, "无权访问", detail, data)
}

// ---------------------------------------------------------------- 路由

func (s *Server) routes() {
	m := s.mux

	// 静态资源
	if staticFS, err := StaticFS(); err == nil {
		fileServer := http.FileServer(http.FS(staticFS))
		m.Handle("GET /static/", http.StripPrefix("/static/", cacheControl(fileServer)))
	}
	m.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	m.HandleFunc("GET /sw.js", s.handleServiceWorker)

	// 上传的产品图片。文件名带时间戳，可以长缓存。
	uploadRoot := s.svc.UploadDir()
	if err := os.MkdirAll(uploadRoot, 0o755); err != nil {
		log.Printf("创建图片目录失败（上传功能将不可用）: %v", err)
	}
	m.Handle("GET /uploads/", http.StripPrefix("/uploads/", immutable(http.FileServer(http.Dir(uploadRoot)))))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok " + s.cfg.Version))
	})

	// 登录
	m.HandleFunc("GET /login", s.handleLoginPage)
	m.HandleFunc("POST /login", s.handleLoginSubmit)
	m.HandleFunc("POST /logout", s.handleLogout)

	// 首页
	m.Handle("GET /{$}", s.guard(PermDashboardView, s.handleDashboard))

	// 全部功能（底部导航「更多」，纯链接、不依赖 JS）
	m.Handle("GET /more", s.guard("", s.handleMore))
	// 电商平台账单：入口先占住（菜单里标"规划中"），功能后续接入
	m.Handle("GET /ecommerce", s.guard("", s.handleEcommerce))
	m.Handle("GET /ecommerce/orders/{id}", s.guard("", s.handleEcOrderDetail))
	m.Handle("POST /ecommerce/bind", s.guard(PermProductManage, s.handleEcBind))
	m.Handle("POST /ecommerce/bind-item", s.guard(PermProductManage, s.handleEcBindItem))
	m.Handle("POST /import/ec-orders", s.guard(PermProductManage, s.handleImportEcOrders))

	// 产品
	m.Handle("GET /products", s.guard(PermProductView, s.handleProductList))
	m.Handle("GET /products/new", s.guard(PermProductManage, s.handleProductForm))
	m.Handle("POST /products/new", s.guard(PermProductManage, s.handleProductSave))
	m.Handle("GET /products/{id}", s.guard(PermProductView, s.handleProductDetail))
	m.Handle("GET /products/{id}/edit", s.guard(PermProductManage, s.handleProductForm))
	m.Handle("POST /products/{id}/edit", s.guard(PermProductManage, s.handleProductSave))
	m.Handle("POST /products/{id}/toggle", s.guard(PermProductManage, s.handleProductToggle))
	m.Handle("POST /products/{id}/delete", s.guard(PermProductManage, s.handleProductDelete))
	m.Handle("POST /products/{id}/barcode", s.guard(PermProductManage, s.handleBindBarcode))
	m.Handle("POST /products/{id}/image", s.guard(PermProductManage, s.handleProductImageUpload))
	m.Handle("POST /products/{id}/image/delete", s.guard(PermProductManage, s.handleProductImageDelete))
	m.Handle("GET /products/labels", s.guard(PermProductView, s.handleProductLabels))
	m.Handle("GET /scan", s.guard(PermProductView, s.handleBarcodeLookup))

	// 供应商 / 客户
	m.Handle("GET /suppliers", s.guard(PermPartnerView, s.partnerList("supplier")))
	m.Handle("GET /suppliers/new", s.guard(PermPartnerManage, s.partnerForm("supplier")))
	m.Handle("POST /suppliers/new", s.guard(PermPartnerManage, s.partnerSave("supplier")))
	m.Handle("GET /suppliers/{id}/edit", s.guard(PermPartnerManage, s.partnerForm("supplier")))
	m.Handle("POST /suppliers/{id}/edit", s.guard(PermPartnerManage, s.partnerSave("supplier")))
	m.Handle("POST /suppliers/{id}/delete", s.guard(PermPartnerManage, s.partnerDelete("supplier")))
	m.Handle("GET /customers", s.guard(PermPartnerView, s.partnerList("customer")))
	m.Handle("GET /customers/new", s.guard(PermPartnerManage, s.partnerForm("customer")))
	m.Handle("POST /customers/new", s.guard(PermPartnerManage, s.partnerSave("customer")))
	m.Handle("GET /customers/{id}/edit", s.guard(PermPartnerManage, s.partnerForm("customer")))
	m.Handle("POST /customers/{id}/edit", s.guard(PermPartnerManage, s.partnerSave("customer")))
	m.Handle("POST /customers/{id}/delete", s.guard(PermPartnerManage, s.partnerDelete("customer")))

	// 采购入库
	m.Handle("GET /purchases", s.guard(PermPurchaseView, s.handlePurchaseList))
	m.Handle("GET /purchases/new", s.guard(PermPurchaseManage, s.handlePurchaseForm))
	m.Handle("POST /purchases/new", s.guard(PermPurchaseManage, s.handlePurchaseSave))
	m.Handle("GET /purchases/{id}", s.guard(PermPurchaseView, s.handlePurchaseDetail))
	m.Handle("GET /purchases/{id}/edit", s.guard(PermPurchaseManage, s.handlePurchaseForm))
	m.Handle("POST /purchases/{id}/edit", s.guard(PermPurchaseManage, s.handlePurchaseSave))
	m.Handle("POST /purchases/{id}/confirm", s.guard(PermPurchaseConfirm, s.handlePurchaseConfirm))
	m.Handle("POST /purchases/{id}/unconfirm", s.guard(PermPurchaseConfirm, s.handlePurchaseUnconfirm))
	m.Handle("POST /purchases/{id}/void", s.guard(PermPurchaseManage, s.handlePurchaseVoid))
	m.Handle("POST /purchases/{id}/delete", s.guard(PermPurchaseManage, s.handlePurchaseDelete))

	// 库存
	m.Handle("GET /inventory", s.guard(PermInventoryView, s.handleInventory))
	m.Handle("GET /inventory/movements", s.guard(PermInventoryView, s.handleMovements))
	m.Handle("GET /inventory/adjust", s.guard(PermInventoryAdjust, s.handleAdjustForm))
	m.Handle("POST /inventory/adjust", s.guard(PermInventoryAdjust, s.handleAdjustSave))

	// 市集
	// 线下大团单：只做销售订单记录，不动库存（货从大仓发）
	m.Handle("GET /group-orders", s.guard(PermGroupView, s.handleGroupOrderList))
	m.Handle("GET /group-orders/new", s.guard(PermGroupManage, s.handleGroupOrderForm))
	m.Handle("POST /group-orders/new", s.guard(PermGroupManage, s.handleGroupOrderSave))
	m.Handle("GET /group-orders/{id}", s.guard(PermGroupView, s.handleGroupOrderDetail))
	m.Handle("GET /group-orders/{id}/edit", s.guard(PermGroupManage, s.handleGroupOrderForm))
	m.Handle("POST /group-orders/{id}/edit", s.guard(PermGroupManage, s.handleGroupOrderSave))
	m.Handle("POST /group-orders/{id}/status", s.guard(PermGroupManage, s.handleGroupOrderStatus))
	m.Handle("POST /group-orders/{id}/delete", s.guard(PermGroupManage, s.handleGroupOrderDelete))
	m.Handle("GET /export/group-orders.xlsx", s.guard(PermReportExport, s.handleExportGroupOrders))

	m.Handle("GET /markets", s.guard(PermMarketView, s.handleMarketList))
	m.Handle("GET /markets/new", s.guard(PermMarketManage, s.handleMarketForm))
	m.Handle("POST /markets/new", s.guard(PermMarketManage, s.handleMarketSave))
	m.Handle("GET /markets/{id}", s.guard(PermMarketView, s.handleMarketDetail))
	m.Handle("GET /markets/{id}/edit", s.guard(PermMarketManage, s.handleMarketForm))
	m.Handle("POST /markets/{id}/edit", s.guard(PermMarketManage, s.handleMarketSave))
	m.Handle("POST /markets/{id}/status", s.guard(PermMarketManage, s.handleMarketStatus))
	m.Handle("POST /markets/{id}/items", s.guard(PermMarketManage, s.handleMarketItemAdd))
	m.Handle("POST /markets/{id}/items/copy-last", s.guard(PermMarketManage, s.handleMarketItemsCopyLast))
	m.Handle("POST /markets/{id}/items/add-all", s.guard(PermMarketManage, s.handleMarketItemsAddAll))
	m.Handle("POST /markets/{id}/items/{itemID}", s.guard(PermMarketManage, s.handleMarketItemUpdate))
	m.Handle("POST /markets/{id}/items/{itemID}/delete", s.guard(PermMarketManage, s.handleMarketItemDelete))
	m.Handle("POST /markets/{id}/expenses", s.guard(PermMarketManage, s.handleMarketExpenses))
	m.Handle("POST /markets/{id}/settle", s.guard(PermMarketSettle, s.handleMarketSettle))
	m.Handle("POST /markets/{id}/unsettle", s.guard(PermMarketSettle, s.handleMarketUnsettle))
	m.Handle("POST /markets/{id}/delete", s.guard(PermMarketManage, s.handleMarketDelete))

	// 市集收银台：现场逐笔记账（点按钮或扫码），实时看单数与销售额
	m.Handle("GET /markets/{id}/pos", s.guard(PermMarketView, s.handleMarketPOS))
	m.Handle("POST /markets/{id}/records", s.guard(PermMarketManage, s.handleAddRecord))
	m.Handle("POST /markets/{id}/records/{rid}", s.guard(PermMarketManage, s.handleUpdateRecord))
	m.Handle("POST /markets/{id}/records/{rid}/delete", s.guard(PermMarketManage, s.handleDeleteRecord))
	m.Handle("POST /markets/{id}/scan", s.guard(PermMarketManage, s.handlePOSScan))
	m.Handle("POST /markets/{id}/scan/bind", s.guard(PermMarketManage, s.handlePOSScanBind))

	// 报表
	m.Handle("GET /reports/markets", s.guard(PermReportView, s.handleReportMarkets))
	m.Handle("GET /reports/products", s.guard(PermReportView, s.handleReportProducts))
	m.Handle("GET /reports/inventory", s.guard(PermInventoryView, s.handleReportInventory))

	// 导出
	m.Handle("GET /export/markets.xlsx", s.guard(PermReportExport, s.handleExportMarkets))
	m.Handle("GET /export/products.xlsx", s.guard(PermReportExport, s.handleExportProducts))
	m.Handle("GET /export/market.xlsx", s.guard(PermReportExport, s.handleExportMarketDetail))
	m.Handle("GET /export/inventory.xlsx", s.guard(PermInventoryView, s.handleExportInventory))
	m.Handle("GET /export/movements.csv", s.guard(PermInventoryView, s.handleExportMovements))

	// 用户与设置
	m.Handle("GET /users", s.guard(PermUserManage, s.handleUserList))
	m.Handle("GET /users/new", s.guard(PermUserManage, s.handleUserForm))
	m.Handle("POST /users/new", s.guard(PermUserManage, s.handleUserSave))
	m.Handle("GET /users/{id}/edit", s.guard(PermUserManage, s.handleUserForm))
	m.Handle("POST /users/{id}/edit", s.guard(PermUserManage, s.handleUserSave))
	m.Handle("GET /users/{id}/password", s.guard(PermUserManage, s.handleUserPasswordForm))
	m.Handle("POST /users/{id}/password", s.guard(PermUserManage, s.handleUserPasswordSave))
	m.Handle("POST /users/{id}/delete", s.guard(PermUserManage, s.handleUserDelete))

	m.Handle("GET /profile", s.guard("", s.handleProfile))
	m.Handle("POST /profile/password", s.guard("", s.handleProfilePassword))
	m.Handle("POST /profile/username", s.guard("", s.handleProfileUsername))
	// 数据导入（把原来表格里的数据一次性搬进来）
	m.Handle("GET /import", s.guard(PermSettingManage, s.handleImportPage))
	m.Handle("POST /import/products", s.guard(PermSettingManage, s.handleImportProducts))
	m.Handle("POST /import/purchases", s.guard(PermSettingManage, s.handleImportPurchases))
	m.Handle("POST /import/outbound", s.guard(PermSettingManage, s.handleImportOutbound))
	m.Handle("GET /settings", s.guard(PermSettingManage, s.handleSettings))
	m.Handle("POST /settings", s.guard(PermSettingManage, s.handleSettingsSave))
	m.Handle("POST /settings/rebuild", s.guard(PermSettingManage, s.handleSettingsRebuild))
	m.Handle("POST /settings/backup", s.guard(PermSettingManage, s.handleSettingsBackup))
	m.Handle("GET /settings/backup/{name}", s.guard(PermSettingManage, s.handleBackupDownload))
	m.Handle("POST /settings/backup/{name}/delete", s.guard(PermSettingManage, s.handleBackupDelete))
	m.Handle("GET /logs", s.guard(PermUserManage, s.handleLogs))

	// 兜底 404
	m.Handle("/", s.guard("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isHTMX(r) {
			w.Header().Set("HX-Reswap", "none")
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data := s.newPage(r, "页面不存在", "")
		s.rnd.RenderError(w, http.StatusNotFound, "页面不存在",
			"请检查地址是否正确，或从左侧菜单重新进入。", data)
	})))
}

// cacheControl 给静态资源加上缓存头。
//
// 关键点：应用自己的 css/js 在模板里带了 ?v=<构建版本>，URL 会随版本变化，
// 因此可以放心长缓存；没有带版本号的资源（图片等）只做协商缓存，
// 避免改版后浏览器继续用旧文件。
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "vendor/"):
			w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
		case r.URL.Query().Get("v") != "":
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		next.ServeHTTP(w, r)
	})
}

// immutable 给文件名带时间戳的上传文件加长缓存头。
func immutable(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}

// handleServiceWorker 从站点根路径提供 Service Worker 脚本。
//
// 必须放在根路径：/static/sw.js 的作用域最多只能覆盖 /static/，
// 注册时申请 scope "/" 会被浏览器直接拒绝，离线缓存与 PWA 安装都会失效。
// 同时把构建版本注入脚本，缓存名随版本变化，升级后旧缓存会被自动清理。
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	source, err := assets.ReadStatic("sw.js")
	if err != nil {
		http.Error(w, "service worker 不可用", http.StatusInternalServerError)
		return
	}
	body := bytes.ReplaceAll(source, []byte("__ERP_VERSION__"), []byte(s.cfg.Version))
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(body)
}

// noCache 用于 HTML 页面。
// noCache 动态页面不缓存。
//
// 用 no-cache（每次使用前要跟服务器核对）而不是 no-store：
// no-store 会让浏览器彻底放弃这份文档，连前进/后退缓存都不能用，
// 于是"返回上一页"也要整页重新请求，用起来发卡。
func noCache(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

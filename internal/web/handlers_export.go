package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"icewine-erp/internal/model"
	"icewine-erp/internal/store"
)

// hasKey 判断表单/查询串中是否出现过某个字段（用于区分“未传”和“传了空值”）。
func (f *formReader) hasKey(key string) bool {
	if _, ok := f.r.Form[key]; ok {
		return true
	}
	_, ok := f.r.URL.Query()[key]
	return ok
}

// sendFile 以附件形式输出文件。
func sendFile(w http.ResponseWriter, r *http.Request, data []byte, contentType, filename string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+asciiFallback(filename)+`"; filename*=UTF-8''`+url.PathEscape(filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

// asciiFallback 为非 ASCII 文件名提供安全回退名。
func asciiFallback(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 128 && r != '"' && r != '\\' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "export"
	}
	return b.String()
}

func stamp() string { return timeNow().Format("20060102") }

// ---------------------------------------------------------------- 导出处理

func (s *Server) handleExportMarkets(w http.ResponseWriter, r *http.Request) {
	f := newFormReader(r)
	from, to := reportRange(f)
	data, contentType, err := s.svc.ExportMarketSummary(r.Context(), from, to, f.Str("status"), f.Str("q"))
	if err != nil {
		s.fail(w, r, "/reports/markets", err)
		return
	}
	sendFile(w, r, data, contentType, "市集利润报表-"+stamp()+".xlsx")
}

func (s *Server) handleExportProducts(w http.ResponseWriter, r *http.Request) {
	f := newFormReader(r)
	from, to := reportRange(f)
	data, contentType, err := s.svc.ExportProductSales(r.Context(), from, to)
	if err != nil {
		s.fail(w, r, "/reports/products", err)
		return
	}
	sendFile(w, r, data, contentType, "产品销售报表-"+stamp()+".xlsx")
}

func (s *Server) handleExportMarketDetail(w http.ResponseWriter, r *http.Request) {
	// 路由是 /export/market.xlsx?id=N（Go 1.22+ 的 ServeMux 不支持
	// /export/market/{id}.xlsx 这种"通配符后面还有后缀"的写法）
	f := newFormReader(r)
	id := f.ID("id", "市集")
	data, contentType, err := s.svc.ExportMarketDetail(r.Context(), id)
	if err != nil {
		s.fail(w, r, "/markets/"+strconv.FormatInt(id, 10), err)
		return
	}
	filename := "市集报表-" + stamp() + ".xlsx"
	if m, err := s.svc.Store.MarketByID(r.Context(), id); err == nil && m != nil {
		filename = m.Code + "-" + m.Name + ".xlsx"
	}
	sendFile(w, r, data, contentType, filename)
}

func (s *Server) handleExportInventory(w http.ResponseWriter, r *http.Request) {
	data, contentType, err := s.svc.ExportInventory(r.Context(), true)
	if err != nil {
		s.fail(w, r, "/inventory", err)
		return
	}
	sendFile(w, r, data, contentType, "库存报表-"+stamp()+".xlsx")
}

func (s *Server) handleExportMovements(w http.ResponseWriter, r *http.Request) {
	f := newFormReader(r)
	filter := store.MovementFilter{
		ProductID: f.ID("product", "产品"),
		Reason:    f.Str("reason"),
		From:      f.Str("from"),
		To:        f.Str("to"),
		Keyword:   f.Str("q"),
		Direction: f.Str("direction"),
	}
	data, contentType, err := s.svc.ExportMovementsCSV(r.Context(), filter)
	if err != nil {
		s.fail(w, r, "/inventory/movements", err)
		return
	}
	sendFile(w, r, data, contentType, "库存流水-"+stamp()+".csv")
}

var _ = model.Money(0)

// handleExportGroupOrders 导出行下团单记录。
func (s *Server) handleExportGroupOrders(w http.ResponseWriter, r *http.Request) {
	f := newFormReader(r)
	data, contentType, err := s.svc.ExportGroupOrders(r.Context(), store.GroupOrderFilter{
		Keyword: f.Str("q"),
		Status:  f.Str("status"),
		From:    f.Str("from"),
		To:      f.Str("to"),
		Sort:    f.Str("sort"),
	})
	if err != nil {
		s.fail(w, r, "/group-orders", err)
		return
	}
	sendFile(w, r, data, contentType, "线下团单-"+stamp()+".xlsx")
}

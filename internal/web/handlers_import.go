package web

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
)

// maxImportSize 上传文件大小上限。
const maxImportSize = 12 << 20

// importSheet 读进来的表格内容。
type importSheet struct {
	Headers []string
	Rows    [][]string
}

// cell 按列名取值，支持多个别名，找不到返回空串。
func (s importSheet) cell(row []string, aliases ...string) string {
	for _, alias := range aliases {
		key := normalizeHeader(alias)
		for i, h := range s.Headers {
			if normalizeHeader(h) == key {
				if i < len(row) {
					return cleanCell(row[i])
				}
				return ""
			}
		}
	}
	return ""
}

// normalizeHeader 表头归一化：去掉空格与常见标点，便于宽松匹配。
func normalizeHeader(s string) string {
	v := strings.TrimSpace(s)
	v = strings.ReplaceAll(v, " ", "")
	v = strings.ReplaceAll(v, "\u3000", "")
	v = strings.ReplaceAll(v, "*", "")
	v = strings.ReplaceAll(v, "（", "(")
	v = strings.ReplaceAll(v, "）", ")")
	return strings.ToLower(v)
}

// cleanCell 清洗单元格：去掉首尾空白与全角空格。
func cleanCell(s string) string {
	v := strings.TrimSpace(s)
	v = strings.Trim(v, "\u3000")
	v = strings.TrimSpace(v)
	// Excel 会把长条码读成 8.32136E+11，这里还原成整数串
	if strings.ContainsAny(v, "eE") && strings.ContainsAny(v, "0123456789") {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			v = strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return v
}

// readImportFile 读取上传的 xlsx / csv，返回第一个工作表的内容。
func readImportFile(file io.Reader, filename string) (importSheet, error) {
	var sh importSheet
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".csv"), strings.HasSuffix(lower, ".txt"):
		cr := csv.NewReader(file)
		cr.FieldsPerRecord = -1
		cr.LazyQuotes = true
		records, err := cr.ReadAll()
		if err != nil {
			return sh, fmt.Errorf("读取 CSV 失败：%w", err)
		}
		rows := make([][]string, 0, len(records))
		for _, r := range records {
			rows = append(rows, r)
		}
		return buildSheet(rows), nil
	case strings.HasSuffix(lower, ".xlsx"), strings.HasSuffix(lower, ".xlsm"):
		f, err := excelize.OpenReader(file)
		if err != nil {
			return sh, fmt.Errorf("读取 Excel 失败：%w", err)
		}
		defer f.Close()
		sheets := f.GetSheetList()
		if len(sheets) == 0 {
			return sh, fmt.Errorf("文件里没有工作表")
		}
		rows, err := f.GetRows(sheets[0])
		if err != nil {
			return sh, fmt.Errorf("读取工作表失败：%w", err)
		}
		return buildSheet(rows), nil
	default:
		return sh, fmt.Errorf("只支持 .xlsx 或 .csv 文件")
	}
}

// buildSheet 找到表头行（第一行非空），其余作为数据行。
func buildSheet(rows [][]string) importSheet {
	var sh importSheet
	for _, r := range rows {
		if isEmptyRow(r) {
			continue
		}
		sh.Headers = r
		rows = rows[1:]
		break
	}
	for _, r := range rows {
		if isEmptyRow(r) {
			continue
		}
		sh.Rows = append(sh.Rows, r)
	}
	return sh
}

func isEmptyRow(r []string) bool {
	for _, c := range r {
		if cleanCell(c) != "" {
			return false
		}
	}
	return true
}

// importUpload 读取上传的文件并返回表格内容。
func importUpload(w http.ResponseWriter, r *http.Request) (importSheet, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "请选择要导入的文件（.xlsx 或 .csv）", http.StatusBadRequest)
		return importSheet{}, false
	}
	defer file.Close()
	sh, err := readImportFile(file, header.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return importSheet{}, false
	}
	if len(sh.Headers) == 0 || len(sh.Rows) == 0 {
		http.Error(w, "文件里没有可导入的数据行", http.StatusBadRequest)
		return importSheet{}, false
	}
	return sh, true
}

// ---------------------------------------------------------------- 页面与导入

func (s *Server) handleImportPage(w http.ResponseWriter, r *http.Request) {
	noCache(w)
	page := s.newPage(r, "数据导入", "import")
	if msg := strings.TrimSpace(r.URL.Query().Get("msg")); msg != "" {
		page["Flash"] = []Flash{{Level: "info", Text: msg}}
	}
	if err := s.rnd.Render(w, "import", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleImportProducts 导入产品档案与期初库存。
func (s *Server) handleImportProducts(w http.ResponseWriter, r *http.Request) {
	sh, ok := importUpload(w, r)
	if !ok {
		return
	}
	rows := make([]service.ProductImportRow, 0, len(sh.Rows))
	for _, row := range sh.Rows {
		rows = append(rows, service.ProductImportRow{
			SKU:        sh.cell(row, "产品编码", "货品编码", "编码", "SKU", "条形码"),
			Barcode:    sh.cell(row, "条形码", "条码", "barcode"),
			Name:       sh.cell(row, "产品名称", "货品名称", "名称", "品名"),
			ShortName:  sh.cell(row, "货品简称", "产品简称", "简称", "别名"),
			Category:   sh.cell(row, "品类", "货品类型", "分类", "类别"),
			Brand:      sh.cell(row, "品牌"),
			Origin:     sh.cell(row, "产地", "产区"),
			Unit:       sh.cell(row, "单位"),
			CostPrice:  sh.cell(row, "参考成本", "货品成本", "成本价", "成本"),
			SalePrice:  sh.cell(row, "建议售价", "货品售价", "售价", "零售价"),
			LowStock:   sh.cell(row, "库存预警线", "预警线", "安全库存"),
			Supplier:   sh.cell(row, "供应商"),
			OpeningQty: sh.cell(row, "期初库存", "初始库存", "库存数量"),
			OpeningOn:  sh.cell(row, "期初日期"),
			Notes:      sh.cell(row, "备注"),
		})
	}
	res, err := s.svc.ImportProducts(r.Context(), rows, userFrom(r))
	if err != nil {
		s.fail(w, r, "/import", err)
		return
	}
	s.importDone(w, r, "产品", res)
}

// handleImportPurchases 导入进货明细。
func (s *Server) handleImportPurchases(w http.ResponseWriter, r *http.Request) {
	sh, ok := importUpload(w, r)
	if !ok {
		return
	}
	rows := make([]service.PurchaseImportRow, 0, len(sh.Rows))
	for _, row := range sh.Rows {
		rows = append(rows, service.PurchaseImportRow{
			Date:      sh.cell(row, "日期", "进货日期", "下单日期", "采购日期"),
			Supplier:  sh.cell(row, "供应商"),
			SKU:       sh.cell(row, "产品编码", "货品编码", "编码", "SKU", "条形码"),
			Product:   sh.cell(row, "产品名称", "货品名称", "下单商品", "产品", "品名", "货品简称", "简称"),
			Qty:       sh.cell(row, "数量", "进货数量"),
			UnitPrice: sh.cell(row, "单价", "进货单价", "成本价", "单位成本"),
			Note:      sh.cell(row, "备注"),
		})
	}
	res, err := s.svc.ImportPurchases(r.Context(), rows, userFrom(r))
	if err != nil {
		s.fail(w, r, "/import", err)
		return
	}
	s.importDone(w, r, "进货", res)
}

// handleImportOutbound 导入出库明细（市集现场记录与手工出库）。
func (s *Server) handleImportOutbound(w http.ResponseWriter, r *http.Request) {
	sh, ok := importUpload(w, r)
	if !ok {
		return
	}
	rows := make([]service.OutboundImportRow, 0, len(sh.Rows))
	for _, row := range sh.Rows {
		rows = append(rows, service.OutboundImportRow{
			Market:  sh.cell(row, "市集名称", "市集", "活动名称", "渠道"),
			Date:    sh.cell(row, "日期", "出货日期", "下单日期"),
			SKU:     sh.cell(row, "产品编码", "货品编码", "编码", "SKU", "条形码"),
			Product: sh.cell(row, "产品名称", "货品名称", "下单商品", "产品", "品名", "货品简称", "简称"),
			Kind:    sh.cell(row, "类型", "出库类型", "客户"),
			Qty:     sh.cell(row, "数量", "出货数量"),
			Price:   sh.cell(row, "单价", "售价", "成交价"),
			Note:    sh.cell(row, "备注"),
		})
	}
	res, err := s.svc.ImportOutbound(r.Context(), rows, userFrom(r))
	if err != nil {
		s.fail(w, r, "/import", err)
		return
	}
	s.importDone(w, r, "出库", res)
}

// importDone 导入结束后把结果回显在导入页上。
func (s *Server) importDone(w http.ResponseWriter, r *http.Request, label string, res service.ImportResult) {
	// 导入会批量改数据，必须留痕：谁、什么时候、导了什么、结果如何
	s.logAction(r, "导入"+label, "import", nil, fmt.Sprintf(
		"读取 %d 行，新建 %d 条，更新 %d 条，跳过 %d 行",
		res.Total, res.Created, res.Updated, res.Skipped))
	noCache(w)
	page := s.newPage(r, "数据导入", "import")
	page["Result"] = res
	page["ResultLabel"] = label
	// Flash 必须是 []Flash（模板里的 flash 组件会 range 它）
	if len(res.Errors) == 0 {
		page["Flash"] = []Flash{{Level: "success", Text: fmt.Sprintf(
			"%s导入完成：读取 %d 行，新建 %d 条，更新 %d 条，跳过 %d 行",
			label, res.Total, res.Created, res.Updated, res.Skipped)}}
	} else {
		page["Flash"] = []Flash{{Level: "error", Text: fmt.Sprintf(
			"%s导入完成，但有 %d 行没能导入，请看下面的说明", label, res.Skipped)}}
	}
	if err := s.rnd.Render(w, "import", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ---------------------------------------------------------------- 淘宝订单导入

// taobaoOrderRow 把一行表格映射成订单明细。
//
// 列名按淘宝"导出订单列表"的实际表头来，同时留几个常见别名，
// 平台改版或换导出模板时不至于整列读不到。
func taobaoOrderRow(sh importSheet, row []string) service.EcOrderRow {
	return service.EcOrderRow{
		SubOrderNo:   sh.cell(row, "子订单编号", "子订单号"),
		OrderNo:      sh.cell(row, "主订单编号", "主订单号", "订单编号", "订单号"),
		Title:        sh.cell(row, "商品标题", "宝贝标题", "商品名称"),
		EcProductID:  sh.cell(row, "商品ID", "宝贝ID", "商品id"),
		EcSKU:        sh.cell(row, "商品属性", "规格", "销售属性", "SKU属性"),
		MerchantCode: sh.cell(row, "商家编码", "外部系统编号", "商家sku编码", "货号"),
		Qty:          sh.cell(row, "购买数量", "数量", "商品数量"),
		UnitPrice:    sh.cell(row, "商品价格", "单价", "宝贝价格"),
		PayableAmt:   sh.cell(row, "买家应付货款", "应付货款", "应付金额"),
		PaidAmt:      sh.cell(row, "买家实付金额", "实付金额", "买家实付"),
		RefundStatus: sh.cell(row, "退款状态"),
		RefundAmt:    sh.cell(row, "退款金额"),
		ItemStatus:   sh.cell(row, "订单状态", "子订单状态"),
		CreatedAt:    sh.cell(row, "订单创建时间", "创建时间"),
		PaidAt:       sh.cell(row, "订单付款时间", "付款时间"),
		ShippedAt:    sh.cell(row, "发货时间"),
		LogisticsNo:  sh.cell(row, "物流单号", "运单号"),
		LogisticsCo:  sh.cell(row, "物流公司", "快递公司"),
		SellerNote:   sh.cell(row, "商家备注", "卖家备注"),
		BuyerNote:    sh.cell(row, "主订单买家留言", "买家留言", "买家备注"),
	}
}

// handleImportEcOrders 导入平台订单明细。
func (s *Server) handleImportEcOrders(w http.ResponseWriter, r *http.Request) {
	platform := strings.TrimSpace(r.FormValue("platform"))
	if platform == "" {
		platform = model.EcTaobao
	}
	sh, ok := importUpload(w, r)
	if !ok {
		return
	}
	rows := make([]service.EcOrderRow, 0, len(sh.Rows))
	for _, row := range sh.Rows {
		rows = append(rows, taobaoOrderRow(sh, row))
	}
	summary, err := s.svc.ImportEcOrders(r.Context(), platform, rows, userFrom(r))
	if err != nil {
		s.fail(w, r, "/ecommerce", err)
		return
	}
	msg := fmt.Sprintf("导入完成：%d 张订单 / %d 行明细", summary.Orders+summary.OrdersUpd, summary.Items)
	if summary.Unmatched > 0 {
		msg += fmt.Sprintf("；还有 %d 种商品没绑定产品，请在下方绑定", len(summary.UnmatchedEc))
	}
	if len(summary.Errors) > 0 {
		msg += fmt.Sprintf("；%d 行有问题（%s）", len(summary.Errors), summary.Errors[0])
	}
	s.ok(w, r, "/ecommerce", msg)
}

// handleEcBind 把电商商品ID 绑到 ERP 产品。
func (s *Server) handleEcBind(w http.ResponseWriter, r *http.Request) {
	ecID := strings.TrimSpace(r.FormValue("ec_product_id"))
	platform := strings.TrimSpace(r.FormValue("platform"))
	productID := formID(r, "product_id")
	if productID <= 0 {
		s.fail(w, r, "/ecommerce", service.UserErrf("请选择要绑定的产品"))
		return
	}
	fixed, err := s.svc.BindEcLink(r.Context(), platform, ecID,
		strings.TrimSpace(r.FormValue("ec_sku_id")),
		strings.TrimSpace(r.FormValue("title")), productID, userFrom(r))
	if err != nil {
		s.fail(w, r, "/ecommerce", err)
		return
	}
	msg := "绑定成功，之后这个商品ID 的订单会自动对上"
	if fixed > 0 {
		msg += fmt.Sprintf("；顺带补齐了 %d 行历史订单明细", fixed)
	}
	s.ok(w, r, "/ecommerce", msg)
}

// handleEcBindItem 单独绑一条订单明细（不回写产品档案）。
func (s *Server) handleEcBindItem(w http.ResponseWriter, r *http.Request) {
	itemID := formID(r, "item_id")
	productID := formID(r, "product_id")
	item, _ := s.svc.Store.EcItemByID(r.Context(), itemID)
	back := "/ecommerce"
	if item != nil {
		back = fmt.Sprintf("/ecommerce/orders/%d", item.OrderID)
	}
	if err := s.svc.BindEcItem(r.Context(), itemID, productID, userFrom(r)); err != nil {
		s.fail(w, r, back, err)
		return
	}
	s.ok(w, r, back, "已绑定这一行（只影响这一行，产品的电商商品ID 未改动）")
}

// handleEcUnbind 解除一条平台商品绑定。
func (s *Server) handleEcUnbind(w http.ResponseWriter, r *http.Request) {
	id := formID(r, "link_id")
	if err := s.svc.UnbindEcLink(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, "/ecommerce", err)
		return
	}
	s.ok(w, r, "/ecommerce", "已解绑，对应订单明细的绑定也一起松开了")
}

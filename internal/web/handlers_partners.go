package web

import (
	"net/http"
	"strconv"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
)

// partnerRow 供应商与客户的统一展示结构，便于共用模板。
type partnerRow struct {
	ID          int64
	Name        string
	ContactName string
	Phone       string
	Email       string
	Country     string
	Address     string
	Notes       string
	IsActive    bool
	CreatedAt   string
}

var commonCountries = []string{"加拿大", "美国", "中国", "法国", "意大利", "澳大利亚", "其他"}

func partnerKindLabel(kind string) string {
	if kind == "supplier" {
		return "供应商"
	}
	return "客户"
}

func partnerBasePath(kind string) string {
	if kind == "supplier" {
		return "/suppliers"
	}
	return "/customers"
}

func (s *Server) partnerList(kind string) http.HandlerFunc {
	base := partnerBasePath(kind)
	label := partnerKindLabel(kind)
	return func(w http.ResponseWriter, r *http.Request) {
		f := newFormReader(r)
		keyword := f.Str("q")
		includeInactive := f.Str("all") == "1"

		rows := make([]partnerRow, 0)
		if kind == "supplier" {
			items, err := s.svc.Store.ListSuppliers(r.Context(), keyword, includeInactive)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			for _, it := range items {
				rows = append(rows, partnerRow{
					ID: it.ID, Name: it.Name, ContactName: it.ContactName, Phone: it.Phone,
					Email: it.Email, Country: it.Country, Address: it.Address,
					Notes: it.Notes, IsActive: it.IsActive, CreatedAt: it.CreatedAt,
				})
			}
		} else {
			items, err := s.svc.Store.ListCustomers(r.Context(), keyword, includeInactive)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			for _, it := range items {
				rows = append(rows, partnerRow{
					ID: it.ID, Name: it.Name, Phone: it.Phone, Email: it.Email,
					Address: it.Address, Notes: it.Notes, IsActive: it.IsActive,
					CreatedAt: it.CreatedAt,
				})
			}
		}

		noCache(w)
		page := s.newPage(r, label+"管理", kind+"s")
		page["Kind"] = kind
		page["KindLabel"] = label
		page["BasePath"] = base
		page["Rows"] = rows
		page["Keyword"] = keyword
		page["IncludeInactive"] = includeInactive
		page["IsSupplier"] = kind == "supplier"
		page["Count"] = len(rows)
		page["CanManage"] = canEdit(r, PermPartnerManage)
		if err := s.rnd.Render(w, "partners/list", page); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (s *Server) partnerForm(kind string) http.HandlerFunc {
	base := partnerBasePath(kind)
	label := partnerKindLabel(kind)
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "id")
		isNew := id == 0
		row := partnerRow{IsActive: true, Country: "加拿大"}

		if !isNew {
			if kind == "supplier" {
				item, err := s.svc.Store.SupplierByID(r.Context(), id)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if item == nil {
					s.notFound(w, r, label+"不存在或已被删除")
					return
				}
				row = partnerRow{
					ID: item.ID, Name: item.Name, ContactName: item.ContactName,
					Phone: item.Phone, Email: item.Email, Country: item.Country,
					Address: item.Address, Notes: item.Notes, IsActive: item.IsActive,
					CreatedAt: item.CreatedAt,
				}
			} else {
				item, err := s.svc.Store.CustomerByID(r.Context(), id)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				if item == nil {
					s.notFound(w, r, label+"不存在或已被删除")
					return
				}
				row = partnerRow{
					ID: item.ID, Name: item.Name, Phone: item.Phone, Email: item.Email,
					Address: item.Address, Notes: item.Notes, IsActive: item.IsActive,
					CreatedAt: item.CreatedAt,
				}
			}
		}

		noCache(w)
		page := s.newPage(r, label+"管理", kind+"s")
		page["Kind"] = kind
		page["KindLabel"] = label
		page["BasePath"] = base
		page["Row"] = row
		page["IsNew"] = isNew
		page["IsSupplier"] = kind == "supplier"
		page["Countries"] = commonCountries
		page["FormAction"] = base + "/new"
		if !isNew {
			page["FormAction"] = base + "/" + strconv.FormatInt(id, 10) + "/edit"
		}
		if err := s.rnd.Render(w, "partners/form", page); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func (s *Server) partnerSave(kind string) http.HandlerFunc {
	base := partnerBasePath(kind)
	label := partnerKindLabel(kind)
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "id")
		f := newFormReader(r)
		row := partnerRow{
			ID:          id,
			Name:        f.Required("name", label+"名称"),
			ContactName: f.Str("contact_name"),
			Phone:       f.Str("phone"),
			Email:       f.Str("email"),
			Country:     f.Str("country"),
			Address:     f.Str("address"),
			Notes:       f.Str("notes"),
			IsActive:    f.Bool("is_active"),
		}
		if err := f.Err(); err != nil {
			s.fail(w, r, base, err)
			return
		}

		ctx := r.Context()
		if kind == "supplier" {
			item := &model.Supplier{
				ID: row.ID, Name: row.Name, ContactName: row.ContactName, Phone: row.Phone,
				Email: row.Email, Country: row.Country, Address: row.Address,
				Notes: row.Notes, IsActive: row.IsActive,
			}
			if id > 0 {
				if err := s.svc.Store.UpdateSupplier(ctx, item); err != nil {
					s.fail(w, r, base, err)
					return
				}
				s.ok(w, r, base, label+"已更新")
				return
			}
			if _, err := s.svc.Store.CreateSupplier(ctx, item); err != nil {
				s.fail(w, r, base, err)
				return
			}
			s.ok(w, r, base, label+"已创建")
			return
		}

		item := &model.Customer{
			ID: row.ID, Name: row.Name, Phone: row.Phone, Email: row.Email,
			Address: row.Address, Notes: row.Notes, IsActive: row.IsActive,
		}
		if id > 0 {
			if err := s.svc.Store.UpdateCustomer(ctx, item); err != nil {
				s.fail(w, r, base, err)
				return
			}
			s.ok(w, r, base, label+"已更新")
			return
		}
		if _, err := s.svc.Store.CreateCustomer(ctx, item); err != nil {
			s.fail(w, r, base, err)
			return
		}
		s.ok(w, r, base, label+"已创建")
	}
}

func (s *Server) partnerDelete(kind string) http.HandlerFunc {
	base := partnerBasePath(kind)
	label := partnerKindLabel(kind)
	return func(w http.ResponseWriter, r *http.Request) {
		id := pathID(r, "id")
		ctx := r.Context()
		var err error
		if kind == "supplier" {
			err = s.svc.Store.DeleteSupplier(ctx, id)
		} else {
			err = s.svc.Store.DeleteCustomer(ctx, id)
		}
		if err != nil {
			s.fail(w, r, base, err)
			return
		}
		s.ok(w, r, base, label+"已删除")
	}
}

var _ = service.UserErrf

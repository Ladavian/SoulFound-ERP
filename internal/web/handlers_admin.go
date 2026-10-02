package web

import (
	"net/http"
	"strconv"
	"time"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
)

func timeNow() time.Time { return time.Now() }

// ---------------------------------------------------------------- 用户管理

func (s *Server) handleUserList(w http.ResponseWriter, r *http.Request) {
	users, err := s.svc.Store.ListUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	descriptions := make([]map[string]string, 0, len(model.RoleOptions))
	for _, opt := range model.RoleOptions {
		descriptions = append(descriptions, map[string]string{
			"Value": opt.Value, "Label": opt.Label, "Description": roleDescription(opt.Value),
		})
	}
	noCache(w)
	page := s.newPage(r, "用户管理", "users")
	page["Users"] = users
	page["RoleDescriptions"] = descriptions
	page["RoleDescriptionsMap"] = map[string]string{
		model.RoleAdmin:   roleDescription(model.RoleAdmin),
		model.RoleManager: roleDescription(model.RoleManager),
		model.RoleStaff:   roleDescription(model.RoleStaff),
		model.RoleViewer:  roleDescription(model.RoleViewer),
	}
	page["Count"] = len(users)
	if err := s.rnd.Render(w, "users/list", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleUserForm(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	isNew := id == 0
	target := &model.User{Role: model.RoleStaff, IsActive: true}
	if !isNew {
		var err error
		target, err = s.svc.Store.UserByID(r.Context(), id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if target == nil {
			s.notFound(w, r, "用户不存在")
			return
		}
	}

	noCache(w)
	page := s.newPage(r, "用户管理", "users")
	page["Target"] = target
	page["IsNew"] = isNew
	page["RoleOptions"] = model.RoleOptions
	page["RoleDescriptions"] = map[string]string{
		model.RoleManager: roleDescription(model.RoleManager),
		model.RoleStaff:   roleDescription(model.RoleStaff),
		model.RoleViewer:  roleDescription(model.RoleViewer),
	}
	page["FormAction"] = "/users/new"
	if !isNew {
		page["FormAction"] = "/users/" + strconv.FormatInt(id, 10) + "/edit"
	}
	if err := s.rnd.Render(w, "users/form", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleUserSave(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	f := newFormReader(r)
	role := f.Str("role")
	isActive := f.Bool("is_active")
	fallback := "/users"
	if id > 0 {
		fallback = "/users/" + strconv.FormatInt(id, 10) + "/edit"
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, fallback, err)
		return
	}

	if id == 0 {
		_, err := s.svc.CreateUser(r.Context(),
			f.Required("username", "用户名"), f.Raw("password"),
			f.Str("full_name"), role, userFrom(r))
		if err != nil {
			s.fail(w, r, fallback, err)
			return
		}
		s.ok(w, r, "/users", "用户已创建")
		return
	}
	if err := s.svc.UpdateUser(r.Context(), id, f.Str("full_name"), role, isActive, userFrom(r)); err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	s.ok(w, r, "/users", "用户资料已更新")
}

func (s *Server) handleUserPasswordForm(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	target, err := s.svc.Store.UserByID(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if target == nil {
		s.notFound(w, r, "用户不存在")
		return
	}
	noCache(w)
	page := s.newPage(r, "重置密码", "users")
	page["Target"] = target
	page["FormAction"] = "/users/" + strconv.FormatInt(id, 10) + "/password"
	if err := s.rnd.Render(w, "users/password", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleUserPasswordSave(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	f := newFormReader(r)
	password := f.Raw("password")
	confirm := f.Raw("confirm_password")
	fallback := "/users/" + strconv.FormatInt(id, 10) + "/password"
	if password != confirm {
		s.fail(w, r, fallback, service.UserErrf("两次输入的密码不一致"))
		return
	}
	if err := s.svc.ResetPassword(r.Context(), id, password, userFrom(r)); err != nil {
		s.fail(w, r, fallback, err)
		return
	}
	s.ok(w, r, "/users", "密码已重置")
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "id")
	if err := s.svc.DeleteUser(r.Context(), id, userFrom(r)); err != nil {
		s.fail(w, r, "/users", err)
		return
	}
	s.ok(w, r, "/users", "用户已删除")
}

// ---------------------------------------------------------------- 系统设置

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.svc.Store.Settings(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	version, _ := s.svc.Store.SchemaVersion(ctx)
	logs, _ := s.svc.Store.ListLogs(ctx, 30)

	noCache(w)
	page := s.newPage(r, "系统设置", "settings")
	page["FormSettings"] = settings
	page["Version"] = s.cfg.Version
	page["SchemaVersion"] = version
	page["DBPath"] = s.cfg.DBPath
	page["BackupDir"] = s.cfg.BackupDir
	page["Logs"] = logs
	page["SessionHours"] = int(s.svc.SessionMaxAge() / 3600)
	if err := s.rnd.Render(w, "settings", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	current, err := s.svc.Store.Settings(ctx)
	if err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	updated := &model.Settings{
		CompanyName:     f.Str("company_name"),
		Currency:        f.Str("currency"),
		CurrencySymbol:  f.Str("currency_symbol"),
		DefaultLowQty:   f.Qty("default_low_qty", "默认库存预警线"),
		DefaultBoothFee: f.Money("default_booth_fee", "默认摊位费"),
		AllowNegative:   f.Bool("allow_negative_stock"),
		UpdatedAt:       current.UpdatedAt,
	}
	if updated.CompanyName == "" {
		updated.CompanyName = current.CompanyName
	}
	if updated.Currency == "" {
		updated.Currency = current.Currency
	}
	if updated.CurrencySymbol == "" {
		updated.CurrencySymbol = current.CurrencySymbol
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	if err := s.svc.Store.SaveSettings(ctx, updated); err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	s.rnd.SetSymbol(updated.CurrencySymbol)
	s.ok(w, r, "/settings", "设置已保存")
}

func (s *Server) handleSettingsRebuild(w http.ResponseWriter, r *http.Request) {
	count, err := s.svc.RebuildAllStock(r.Context())
	if err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	s.ok(w, r, "/settings", "已依据库存流水重算 "+strconv.Itoa(count)+" 个产品的库存与平均成本")
}

func (s *Server) handleSettingsBackup(w http.ResponseWriter, r *http.Request) {
	path, err := s.svc.Store.Backup(r.Context(), s.cfg.BackupDir)
	if err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	s.ok(w, r, "/settings", "备份已生成："+path)
}

// ---------------------------------------------------------------- 操作日志

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.svc.Store.ListLogs(r.Context(), 300)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	noCache(w)
	page := s.newPage(r, "操作日志", "logs")
	page["Logs"] = logs
	page["Count"] = len(logs)
	if err := s.rnd.Render(w, "logs", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

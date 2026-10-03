package web

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"icewine-erp/internal/model"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
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
	page["PermissionGroups"] = PermissionGroups
	page["RoleDefaultsJSON"] = template.JS(jsonEncode(RoleDefaultsByRole()))
	current := permsForUser(target)
	page["CurrentPerms"] = current
	page["CurrentPermList"] = current.List()
	page["AllPermCount"] = len(AllPermissions())
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
			f.Str("full_name"), role, splitPermissions(f.List("permissions")), userFrom(r))
		if err != nil {
			s.fail(w, r, fallback, err)
			return
		}
		s.ok(w, r, "/users", "用户已创建")
		return
	}
	if err := s.svc.UpdateUser(r.Context(), id, f.Required("username", "用户名"),
		f.Str("full_name"), role, isActive, splitPermissions(f.List("permissions")), userFrom(r)); err != nil {
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
	backups, _ := s.svc.ListBackups()
	decision, _ := s.svc.DecideAutoBackup(ctx, timeNow())

	noCache(w)
	page := s.newPage(r, "系统设置", "settings")
	page["FormSettings"] = settings
	page["Version"] = s.cfg.Version
	page["SchemaVersion"] = version
	page["DBPath"] = s.cfg.DBPath
	page["BackupDir"] = s.svc.BackupDir()
	page["Backups"] = backups
	page["BackupDecision"] = decision
	page["BackupStateText"] = backupStateText(decision)
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

		AutoBackup:        f.Bool("auto_backup"),
		BackupKeep:        f.Int("backup_keep", "备份保留份数"),
		BackupActiveHours: f.Int("backup_active_hours", "有变化时的备份间隔"),
		BackupIdleDays:    f.Int("backup_idle_days", "无变化时的备份间隔"),
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
	s.logAction(r, "修改系统设置", "setting", nil,
		"公司名 "+f.Str("company_name")+" · 币种 "+f.Str("currency"))
	s.ok(w, r, "/settings", "设置已保存")
}

func (s *Server) handleSettingsRebuild(w http.ResponseWriter, r *http.Request) {
	count, err := s.svc.RebuildAllStock(r.Context())
	if err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	s.logAction(r, "重算库存", "setting", nil, "依据库存流水重算库存与平均成本")
	s.ok(w, r, "/settings", "已依据库存流水重算 "+strconv.Itoa(count)+" 个产品的库存与平均成本")
}

func (s *Server) handleSettingsBackup(w http.ResponseWriter, r *http.Request) {
	// 走 service：会一并清理旧备份、写备份记录、更新自动备份的判断依据
	info, err := s.svc.BackupNow(r.Context(), userFrom(r))
	if err != nil {
		s.fail(w, r, "/settings", err)
		return
	}
	s.ok(w, r, "/settings", "备份已生成："+info.Name+"（"+info.SizeText()+"）")
}

// ---------------------------------------------------------------- 操作日志

// logPageSize 操作日志每页条数。
const logPageSize = 100

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f := newFormReader(r)
	pageNo := atoiDefault(f.Str("page"), 1)
	if pageNo < 1 {
		pageNo = 1
	}

	// 主账号在这里能按账号、动作、时间、关键词查所有人的操作记录
	filter := store.LogFilter{
		Keyword: f.Str("q"),
		Action:  f.Str("action"),
		From:    f.Str("from"),
		To:      f.Str("to"),
		Limit:   logPageSize,
		Offset:  (pageNo - 1) * logPageSize,
	}
	actor := f.Str("actor")
	if actor != "" {
		if u, err := s.svc.Store.UserByUsername(ctx, actor); err == nil && u != nil {
			id := u.ID
			filter.UserID = &id
		}
	}

	logs, err := s.svc.Store.ListLogsFiltered(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, err := s.svc.Store.CountLogs(ctx, filter)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	actors, _ := s.svc.Store.LogActors(ctx, 60)
	actions, _ := s.svc.Store.LogActions(ctx, 60)

	totalPages := (total + logPageSize - 1) / logPageSize
	if totalPages < 1 {
		totalPages = 1
	}

	noCache(w)
	page := s.newPage(r, "操作日志", "logs")
	page["Logs"] = logs
	page["Count"] = len(logs)
	page["Total"] = total
	page["Keyword"] = filter.Keyword
	page["Action"] = filter.Action
	page["Actor"] = actor
	page["From"] = filter.From
	page["To"] = filter.To
	page["Actors"] = actors
	page["Actions"] = actions
	page["Page"] = pageNo
	page["TotalPages"] = totalPages
	page["HasPrev"] = pageNo > 1
	page["HasNext"] = pageNo < totalPages
	page["PrevURL"] = logsWithPage(r, pageNo-1)
	page["NextURL"] = logsWithPage(r, pageNo+1)
	page["HasFilter"] = filter.Keyword != "" || filter.Action != "" || actor != "" ||
		filter.From != "" || filter.To != ""
	if err := s.rnd.Render(w, "logs", page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func logsWithPage(r *http.Request, pageNo int) string {
	values := r.URL.Query()
	values.Set("page", strconv.Itoa(pageNo))
	return r.URL.Path + "?" + values.Encode()
}

// handleBackupDownload 下载一份备份文件，方便取到本机或另一块盘保存。
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	info, err := s.svc.FindBackup(name)
	if err != nil {
		s.notFound(w, r, "备份文件不存在或已被清理")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+info.Name+`"`)
	http.ServeFile(w, r, info.Path)
}

// handleBackupDelete 删除一份备份。
func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.svc.DeleteBackup(r.Context(), name, userFrom(r)); err != nil {
		s.setFlash(w, "error", userMessage(err))
		s.redirect(w, r, "/settings")
		return
	}
	s.setFlash(w, "success", "备份已删除")
	s.redirect(w, r, "/settings")
}

// backupStateText 把自动备份的判断结果写成一句人话。
func backupStateText(d service.AutoBackupDecision) string {
	if d.Reason == "自动备份已关闭" {
		return d.Reason
	}
	last := "还没有备份"
	if d.HasAny {
		last = d.LastAt.Format("2006-01-02 15:04")
	}
	state := "数据无变化"
	if d.Changed {
		state = "数据有变化"
	}
	return "上次备份 " + last + " · " + state + " · " + d.Reason
}

// splitPermissions 把权限项规整成一个列表。
//
// 表单会提交多个同名字段，但手工调用（或用接口/脚本）时
// 常写成用逗号分隔的一串，两种都接受更不容易踩坑。
func splitPermissions(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

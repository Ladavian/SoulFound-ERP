package web

import (
	"net/http"
	"net/url"
	"strings"

	"icewine-erp/internal/service"
)

// handleLoginPage 登录页。
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if userFrom(r) != nil {
		s.redirect(w, r, "/")
		return
	}
	noCache(w)
	data := s.newPage(r, "登录", "")
	data["Next"] = safeNext(r.URL.Query().Get("next"))
	if err := s.rnd.Render(w, "login", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	f := newFormReader(r)
	username := f.Required("username", "用户名")
	password := f.Raw("password")
	next := safeNext(f.Str("next"))
	if err := f.Err(); err != nil {
		s.loginError(w, r, err, next)
		return
	}
	user, err := s.svc.Authenticate(r.Context(), username, password)
	if err != nil {
		s.loginError(w, r, err, next)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.SessionCookie,
		Value:    s.svc.SignSession(user.ID),
		Path:     "/",
		MaxAge:   s.svc.SessionMaxAge(),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.cfg.CookieSecure,
	})
	s.ok(w, r, next, "欢迎回来，"+user.DisplayName())
}

func (s *Server) loginError(w http.ResponseWriter, r *http.Request, err error, next string) {
	msg := "登录失败"
	if m, ok := service.AsUserError(err); ok {
		msg = m
	}
	noCache(w)
	data := s.newPage(r, "登录", "")
	data["Error"] = msg
	data["Username"] = r.PostFormValue("username")
	data["Next"] = next
	if e := s.rnd.Render(w, "login", data); e != nil {
		http.Error(w, e.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: s.cfg.SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.CookieSecure,
	})
	s.ok(w, r, "/login", "已退出登录")
}

// ---------------------------------------------------------------- 个人设置

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	noCache(w)
	data := s.newPage(r, "我的账号", "profile")
	data["RoleDescription"] = roleDescription(userFrom(r).Role)
	if err := s.rnd.Render(w, "profile", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleProfilePassword(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r)
	f := newFormReader(r)
	oldPassword := f.Raw("old_password")
	newPassword := f.Raw("new_password")
	confirm := f.Raw("confirm_password")
	if oldPassword == "" {
		f.AddError("请输入当前密码")
	}
	if err := f.Err(); err != nil {
		s.fail(w, r, "/profile", err)
		return
	}
	if newPassword != confirm {
		s.fail(w, r, "/profile", service.UserErrf("两次输入的新密码不一致"))
		return
	}
	if err := s.svc.ChangePassword(r.Context(), user.ID, oldPassword, newPassword); err != nil {
		s.fail(w, r, "/profile", err)
		return
	}
	s.ok(w, r, "/profile", "密码已更新")
}

// safeNext 只允许站内相对路径跳转，避免开放重定向。
func safeNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	if parsed, err := url.Parse(next); err != nil || parsed.Host != "" {
		return "/"
	}
	return next
}

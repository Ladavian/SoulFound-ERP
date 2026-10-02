// Package service 承载业务逻辑：库存过账、采购到岸成本、市集损益、报表与导出。
package service

import (
	"errors"
	"fmt"

	"icewine-erp/internal/config"
	"icewine-erp/internal/store"
)

// Service 业务逻辑入口。
type Service struct {
	Store *store.Store
	Cfg   *config.Config
}

// New 构造 Service。
func New(st *store.Store, cfg *config.Config) *Service {
	return &Service{Store: st, Cfg: cfg}
}

// UserError 可以直接展示给用户的业务错误（区别于程序错误）。
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

// UserErrf 构造用户可见错误。
func UserErrf(format string, args ...any) error {
	return &UserError{Msg: fmt.Sprintf(format, args...)}
}

// AsUserError 判断错误是否为用户可见错误，并取出提示文本。
func AsUserError(err error) (string, bool) {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Msg, true
	}
	return "", false
}

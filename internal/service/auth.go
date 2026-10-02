package service

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"icewine-erp/internal/model"
)

const (
	pbkdf2Iterations = 210_000
	passwordKeyLen   = 32
	passwordSaltLen  = 16
	passwordScheme   = "pbkdf2_sha256"
	sessionVersion   = "v1"
)

// HashPassword 使用 PBKDF2-HMAC-SHA256 生成密码哈希。
// 存储格式：pbkdf2_sha256$迭代次数$盐(base64)$哈希(base64)
func HashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成随机盐失败: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, passwordKeyLen)
	if err != nil {
		return "", fmt.Errorf("计算密码哈希失败: %w", err)
	}
	return fmt.Sprintf("%s$%d$%s$%s", passwordScheme, pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验密码（恒定时间比较）。
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 || iter > 5_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return false
	}
	return hmac.Equal(got, want)
}

// ValidatePassword 密码强度校验。
func ValidatePassword(password string) error {
	if len([]rune(password)) < 6 {
		return UserErrf("密码至少需要 6 位字符")
	}
	if len([]rune(password)) > 128 {
		return UserErrf("密码过长")
	}
	return nil
}

// Authenticate 校验用户名与密码。
func (s *Service) Authenticate(ctx context.Context, username, password string) (*model.User, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, UserErrf("请输入用户名和密码")
	}
	user, err := s.Store.UserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if user == nil || !VerifyPassword(user.PasswordHash, password) {
		return nil, UserErrf("用户名或密码错误")
	}
	if !user.IsActive {
		return nil, UserErrf("该账号已被停用，请联系管理员")
	}
	if err := s.Store.TouchLogin(ctx, user.ID); err != nil {
		return nil, err
	}
	return user, nil
}

// ChangePassword 修改密码（校验旧密码）。
func (s *Service) ChangePassword(ctx context.Context, userID int64, oldPassword, newPassword string) error {
	user, err := s.Store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user == nil {
		return UserErrf("用户不存在")
	}
	if !VerifyPassword(user.PasswordHash, oldPassword) {
		return UserErrf("当前密码不正确")
	}
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	return s.Store.UpdatePassword(ctx, userID, hash)
}

// ---------------------------------------------------------------- 令牌签名

func (s *Service) sign(payload string) string {
	mac := hmac.New(sha256.New, s.Cfg.SecretKey)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SignValue 生成 "内容.签名" 形式的防篡改字符串（会话、Flash 提示均使用）。
func (s *Service) SignValue(payload string) string {
	return payload + "." + s.sign(payload)
}

// VerifyValue 校验并解出内容。
func (s *Service) VerifyValue(token string) (string, bool) {
	idx := strings.LastIndex(token, ".")
	if idx <= 0 {
		return "", false
	}
	payload, sig := token[:idx], token[idx+1:]
	expected := s.sign(payload)
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}
	return payload, true
}

// SignSession 生成登录会话令牌。
func (s *Service) SignSession(userID int64) string {
	expiry := time.Now().Add(s.Cfg.SessionTTL).Unix()
	return s.SignValue(fmt.Sprintf("%s|%d|%d", sessionVersion, userID, expiry))
}

// ParseSession 校验会话令牌并返回用户 ID。
func (s *Service) ParseSession(token string) (int64, bool) {
	payload, ok := s.VerifyValue(token)
	if !ok {
		return 0, false
	}
	parts := strings.Split(payload, "|")
	if len(parts) != 3 || parts[0] != sessionVersion {
		return 0, false
	}
	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || userID <= 0 {
		return 0, false
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || time.Now().Unix() > expiry {
		return 0, false
	}
	return userID, true
}

// SessionMaxAge 会话有效期（秒）。
func (s *Service) SessionMaxAge() int {
	return int(s.Cfg.SessionTTL.Seconds())
}

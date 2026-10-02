package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"icewine-erp/internal/model"
)

// resolveSecret 返回会话签名密钥。
//
// 优先使用环境变量 ERP_SECRET_KEY；没有配置时在数据目录里生成并保存一个
// 随机密钥（secret.key），这样开箱即用、重启后登录状态不会失效，而且不需要
// 用户手工配置任何东西。
func resolveSecret(dataDir string) []byte {
	if v := strings.TrimSpace(os.Getenv("ERP_SECRET_KEY")); v != "" {
		return []byte(v)
	}
	keyPath := filepath.Join(dataDir, "secret.key")
	if data, err := os.ReadFile(keyPath); err == nil && len(strings.TrimSpace(string(data))) >= 32 {
		return []byte(strings.TrimSpace(string(data)))
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		log.Printf("警告：生成随机密钥失败，请设置 ERP_SECRET_KEY 环境变量: %v", err)
		return []byte("insecure-fallback-secret-change-me")
	}
	secret := hex.EncodeToString(buf)
	if err := os.WriteFile(keyPath, []byte(secret+"\n"), 0o600); err != nil {
		log.Printf("警告：无法写入密钥文件 %s（%v），请设置 ERP_SECRET_KEY 环境变量", keyPath, err)
	} else {
		log.Printf("已生成会话密钥：%s", keyPath)
	}
	return []byte(secret)
}

// Config 应用配置，全部可由环境变量覆盖，便于 Docker 部署。
type Config struct {
	AppName string
	Version string

	Addr      string
	Dev       bool
	DataDir   string
	DBPath    string
	WebDir    string
	BackupDir string

	SecretKey     []byte
	SessionCookie string
	SessionTTL    time.Duration
	CookieSecure  bool

	Currency       string
	CurrencySymbol string
	DefaultLowQty  model.Qty

	AdminUsername string
	AdminPassword string
	AdminName     string

	SeedDemo bool
	Location *time.Location
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "":
		return def
	case "1", "true", "yes", "on", "y":
		return true
	default:
		return false
	}
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Load 读取环境变量生成配置。
func Load() *Config {
	dataDir := getenv("ERP_DATA_DIR", "data")
	if abs, err := filepath.Abs(dataDir); err == nil {
		dataDir = abs
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Printf("警告：无法创建数据目录 %s: %v", dataDir, err)
	}

	loc := time.Local
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		} else {
			log.Printf("警告：无法加载时区 %s，回退到 %s", tz, loc)
		}
	}

	lowQty, err := model.ParseQty(getenv("ERP_LOW_STOCK", "6"))
	if err != nil {
		lowQty = model.QtyFromInt(6)
	}

	cfg := &Config{
		AppName:        getenv("ERP_APP_NAME", "SoulFound ERP"),
		Version:        getenv("ERP_VERSION", "1.0.0"),
		Addr:           getenv("ERP_ADDR", ":8123"),
		Dev:            envBool("ERP_DEV", false),
		DataDir:        dataDir,
		DBPath:         getenv("ERP_DB_PATH", filepath.Join(dataDir, "erp.sqlite3")),
		WebDir:         getenv("ERP_WEB_DIR", "internal/assets"),
		BackupDir:      getenv("ERP_BACKUP_DIR", filepath.Join(dataDir, "backups")),
		SecretKey:      resolveSecret(dataDir),
		SessionCookie:  getenv("ERP_SESSION_COOKIE", "erp_session"),
		SessionTTL:     time.Duration(envInt("ERP_SESSION_HOURS", 72)) * time.Hour,
		CookieSecure:   envBool("ERP_COOKIE_SECURE", false),
		Currency:       getenv("ERP_CURRENCY", "CNY"),
		CurrencySymbol: getenv("ERP_CURRENCY_SYMBOL", "¥"),
		DefaultLowQty:  lowQty,
		AdminUsername:  getenv("ERP_ADMIN_USERNAME", "admin"),
		AdminPassword:  getenv("ERP_ADMIN_PASSWORD", "admin123"),
		AdminName:      getenv("ERP_ADMIN_NAME", "系统管理员"),
		SeedDemo:       envBool("ERP_SEED_DEMO", false),
		Location:       loc,
	}
	return cfg
}

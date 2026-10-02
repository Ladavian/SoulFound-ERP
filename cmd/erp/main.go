// Command erp 启动加拿大冰酒 ERP 服务，并提供少量运维命令。
//
// 用法：
//
//	erp                                  启动 Web 服务
//	erp version                          查看版本
//	erp reset-password <用户名>           重置某个账号的密码（忘记密码时用）
//	erp backup [目录]                     生成一次数据库一致性备份
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"icewine-erp/internal/config"
	"icewine-erp/internal/service"
	"icewine-erp/internal/store"
	"icewine-erp/internal/web"
)

// buildVersion 由构建时通过 -ldflags "-X main.buildVersion=..." 注入。
var buildVersion = ""

func main() {
	log.SetFlags(log.LstdFlags)

	cfg := config.Load()
	if buildVersion != "" {
		cfg.Version = buildVersion
	}

	// ---------------------------------------------------------- 运维命令
	if len(os.Args) > 1 {
		cmd := os.Args[1]
		switch cmd {
		case "version", "-version", "--version", "-v":
			fmt.Printf("%s %s\n", cfg.AppName, cfg.Version)
			return
		case "reset-password", "backup":
			if err := runCommand(cfg, cmd, os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "错误:", err)
				os.Exit(1)
			}
			return
		case "help", "-h", "--help":
			usage()
			return
		default:
			if strings.HasPrefix(cmd, "-") {
				fmt.Fprintf(os.Stderr, "未知参数 %s\n\n", cmd)
				usage()
				os.Exit(2)
			}
		}
	}

	// ---------------------------------------------------------- 启动服务
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("无法打开数据库: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	if err := st.Migrate(ctx); err != nil {
		cancel()
		log.Fatalf("数据库迁移失败: %v", err)
	}
	svc := service.New(st, cfg)
	if err := svc.Bootstrap(ctx); err != nil {
		cancel()
		log.Fatalf("初始化失败: %v", err)
	}
	cancel()

	handler, err := web.New(cfg, svc)
	if err != nil {
		log.Fatalf("初始化 HTTP 服务失败: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		log.Printf("========================================")
		log.Printf(" %s v%s 已启动", cfg.AppName, cfg.Version)
		log.Printf(" 监听地址 : http://%s", displayAddr(cfg.Addr))
		log.Printf(" 数据文件 : %s", cfg.DBPath)
		log.Printf(" 备份目录 : %s", cfg.BackupDir)
		log.Printf(" 时区     : %s", cfg.Location)
		log.Printf("========================================")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("正在停止服务…")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭 HTTP 服务出错: %v", err)
	}
	log.Println("服务已停止，数据已安全写入")
}

func usage() {
	fmt.Print(`加拿大冰酒 ERP

用法：
  erp                            启动 Web 服务（默认监听 :8000）
  erp version                    查看版本
  erp reset-password <用户名>     重置账号密码（忘记密码时使用）
  erp backup [目录]               生成一次数据库一致性备份

常用环境变量：
  ERP_ADDR              监听地址，默认 :8000
  ERP_DATA_DIR          数据目录，默认 ./data
  ERP_DB_PATH           SQLite 文件路径，默认 <数据目录>/erp.sqlite3
  ERP_SECRET_KEY        会话签名密钥；不设置会自动生成到 <数据目录>/secret.key
  ERP_ADMIN_USERNAME    初始管理员账号，默认 admin
  ERP_ADMIN_PASSWORD    初始管理员密码，默认 admin123
  ERP_CURRENCY          币种代码，默认 CAD
  ERP_CURRENCY_SYMBOL   币种符号，默认 C$
  ERP_LOW_STOCK         默认库存预警线，默认 6
  ERP_SEED_DEMO         设为 1 时首次启动写入演示数据
  ERP_COOKIE_SECURE     走 HTTPS 时设为 1
  TZ                    时区，例如 Asia/Shanghai
`)
}

func runCommand(cfg *config.Config, name string, args []string) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		return err
	}

	switch name {
	case "reset-password":
		if len(args) < 1 {
			return errors.New("用法: erp reset-password <用户名>")
		}
		username := args[0]
		user, err := st.UserByUsername(ctx, username)
		if err != nil {
			return err
		}
		if user == nil {
			return fmt.Errorf("用户 %q 不存在，请检查用户名", username)
		}
		fmt.Printf("为「%s」设置新密码（输入内容会显示在屏幕上）: ", user.DisplayName())
		reader := bufio.NewReader(os.Stdin)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("读取密码失败: %w", err)
		}
		password := strings.TrimSpace(line)
		if err := service.ValidatePassword(password); err != nil {
			return err
		}
		hash, err := service.HashPassword(password)
		if err != nil {
			return err
		}
		if err := st.UpdatePassword(ctx, user.ID, hash); err != nil {
			return err
		}
		fmt.Println("密码已更新，请用新密码登录。")
		return nil

	case "backup":
		dir := cfg.BackupDir
		if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
			dir = args[0]
		}
		path, err := st.Backup(ctx, dir)
		if err != nil {
			return err
		}
		fmt.Println("备份已生成:", path)
		return nil
	}
	return fmt.Errorf("未知命令 %q", name)
}

func displayAddr(addr string) string {
	if addr == "" {
		return "127.0.0.1:8000"
	}
	if addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}

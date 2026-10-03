// Package store 负责数据库连接、迁移与所有 SQL 访问。
//
// 使用纯 Go 的 SQLite 驱动（modernc.org/sqlite），因此可以 CGO_ENABLED=0
// 编译出完全静态的二进制，Docker 镜像可以做得很小。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"icewine-erp/internal/model"
)

// DBTX 抽象 *sql.DB 与 *sql.Tx，便于把查询函数复用到事务里。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store 数据库句柄。
type Store struct {
	db   *sql.DB
	Path string
}

// Open 打开（必要时创建）SQLite 数据库。
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}
	// _txlock=immediate：事务一开始就取写锁，配合 busy_timeout 彻底避免
	// “读升级为写”导致的 SQLITE_BUSY 死锁，对小型系统最稳。
	dsn := "file:" + path + "?" + strings.Join([]string{
		"_pragma=busy_timeout(10000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}, "&")

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// SQLite 同一时刻只允许一个写事务。把连接池限制为 1 条连接，
	// 让所有读写在本进程内自然排队，可以从根本上避免 SQLITE_BUSY。
	// 本项目数据量与并发都很低，串行化的性能代价可以忽略。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	return &Store{db: db, Path: path}, nil
}

// DB 返回底层连接池。
func (s *Store) DB() *sql.DB { return s.db }

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// Tx 在事务中执行 fn，出错自动回滚。
func (s *Store) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- 迁移

// splitStatements 按分号切分 SQL 脚本，忽略 `--` 行注释。
func splitStatements(script string) []string {
	var out []string
	var buf strings.Builder
	for _, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	for _, part := range strings.Split(buf.String(), ";") {
		if stmt := strings.TrimSpace(part); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// Migrate 建立/升级数据库结构，幂等可重复执行。
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("初始化 schema_meta 失败: %w", err)
	}
	// 逐个版本判断是否已执行：只认"记录在案"的版本号，
	// 这样即使日后单独补一个中间版本的迁移也能正确补跑。
	applied := map[int]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_meta`)
	if err != nil {
		return fmt.Errorf("读取数据库版本失败: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("读取数据库版本失败: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("读取数据库版本失败: %w", err)
	}
	rows.Close()

	for i, script := range migrations {
		version := i + 1
		if applied[version] {
			continue
		}
		for _, stmt := range splitStatements(script) {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("执行迁移 v%d 失败: %w\nSQL: %s", version, err, firstLine(stmt))
			}
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO schema_meta(version) VALUES (?)`, version); err != nil {
			return fmt.Errorf("记录迁移版本失败: %w", err)
		}
	}
	return nil
}

// SchemaVersion 当前数据库版本。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_meta`).Scan(&v)
	return v, err
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx > 0 {
		return s[:idx]
	}
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

// Backup 使用 VACUUM INTO 生成一致性快照，可在系统运行时安全执行。
//
// 同一秒内连续备份时会自动加序号，避免目标文件已存在导致 VACUUM 失败。
func (s *Store) Backup(ctx context.Context, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建备份目录失败: %w", err)
	}
	stamp := time.Now().Format("20060102-150405")
	var target string
	for i := 0; i < 100; i++ {
		name := "erp-backup-" + stamp + ".sqlite3"
		if i > 0 {
			name = fmt.Sprintf("erp-backup-%s-%02d.sqlite3", stamp, i)
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			continue // 已存在，换个序号
		}
		target = candidate
		break
	}
	if target == "" {
		return "", fmt.Errorf("备份失败：同名备份文件过多，请稍后重试")
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		// SQLite 对写不进去的文件只会回一句 "unable to open database file"，
		// 看不出是权限问题，这里补一句人能看懂的说明与处理办法。
		if !dirWritable(dir) {
			return "", fmt.Errorf("备份目录不可写：%s。"+
				"容器部署时通常是宿主机挂载目录属主不对，"+
				"在宿主机执行 sudo chown -R 1000:1000 ./data 后重启即可（原始错误: %w）",
				dir, err)
		}
		return "", fmt.Errorf("备份失败: %w", err)
	}
	return target, nil
}

// ---------------------------------------------------------------- 单据号

// NextCode 生成单据号，形如 PO-20251002-001。需在事务内调用以保证连续。
func (s *Store) NextCode(ctx context.Context, tx DBTX, scope, prefix string, day time.Time) (string, error) {
	dayKey := day.Format("20060102")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO doc_seq(scope, day, seq) VALUES (?, ?, 0)
		 ON CONFLICT(scope, day) DO NOTHING`, scope, dayKey); err != nil {
		return "", err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`UPDATE doc_seq SET seq = seq + 1 WHERE scope = ? AND day = ? RETURNING seq`,
		scope, dayKey).Scan(&seq); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%03d", prefix, dayKey, seq), nil
}

// PeekCode 仅计算下一个单据号，不占用序号（用于表单预览）。
func (s *Store) PeekCode(ctx context.Context, scope, prefix string, day time.Time) (string, error) {
	dayKey := day.Format("20060102")
	var seq int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(seq, 0) + 1 FROM doc_seq WHERE scope = ? AND day = ?`,
		scope, dayKey).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		seq = 1
	} else if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%s-%03d", prefix, dayKey, seq), nil
}

// ---------------------------------------------------------------- 时间

// Now 当前时间戳（RFC3339，随容器 TZ）。
func Now() string { return time.Now().Format(time.RFC3339) }

// Today 今天（YYYY-MM-DD）。
func Today() string { return time.Now().Format("2006-01-02") }

// ---------------------------------------------------------------- 扫描辅助

func nzStr(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func nzInt(v sql.NullInt64) int64 {
	if v.Valid {
		return v.Int64
	}
	return 0
}

func ptrInt(v sql.NullInt64) *int64 {
	if v.Valid {
		n := v.Int64
		return &n
	}
	return nil
}

func nzMoney(v sql.NullInt64) model.Money {
	if v.Valid {
		return model.Money(v.Int64)
	}
	return 0
}

func ptrMoney(v sql.NullInt64) *model.Money {
	if v.Valid {
		m := model.Money(v.Int64)
		return &m
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func i2b(i int64) bool { return i != 0 }

func nzFloat(v sql.NullFloat64) float64 {
	if v.Valid {
		return v.Float64
	}
	return 0
}

// ---------------------------------------------------------------- 设置

// EnsureSettings 保证设置行存在。
func (s *Store) EnsureSettings(ctx context.Context, def model.Settings) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings(id, company_name, currency, currency_symbol,
		        default_low_qty, default_booth_fee, allow_negative_stock, updated_at,
		        auto_backup, backup_keep, backup_active_hours, backup_idle_days)
		 VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO NOTHING`,
		def.CompanyName, def.Currency, def.CurrencySymbol,
		int64(def.DefaultLowQty), int64(def.DefaultBoothFee), b2i(def.AllowNegative), Now(),
		b2i(def.AutoBackup), def.BackupKeepCount(), activeHours(def), idleDays(def))
	return err
}

// Settings 读取全局设置。
func (s *Store) Settings(ctx context.Context) (*model.Settings, error) {
	var (
		out            model.Settings
		lowQty         int64
		boothFee       int64
		allowNeg       int64
		autoBackup     int64
		backupKeep     int64
		backupActiveH  int64
		backupIdleDays int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT company_name, currency, currency_symbol, default_low_qty,
		        default_booth_fee, allow_negative_stock, updated_at,
		        auto_backup, backup_keep, backup_active_hours, backup_idle_days
		 FROM settings WHERE id = 1`).
		Scan(&out.CompanyName, &out.Currency, &out.CurrencySymbol, &lowQty,
			&boothFee, &allowNeg, &out.UpdatedAt,
			&autoBackup, &backupKeep, &backupActiveH, &backupIdleDays)
	if errors.Is(err, sql.ErrNoRows) {
		out = model.Settings{
			CompanyName:    "SoulFound",
			Currency:       "CNY",
			CurrencySymbol: "¥",
			DefaultLowQty:  model.QtyFromInt(6),
		}
		return &out, nil
	}
	if err != nil {
		return nil, err
	}
	out.DefaultLowQty = model.Qty(lowQty)
	out.DefaultBoothFee = model.Money(boothFee)
	out.AllowNegative = i2b(allowNeg)
	out.AutoBackup = i2b(autoBackup)
	out.BackupKeep = int(backupKeep)
	out.BackupActiveHours = int(backupActiveH)
	out.BackupIdleDays = int(backupIdleDays)
	return &out, nil
}

// SaveSettings 保存全局设置。
func (s *Store) SaveSettings(ctx context.Context, in *model.Settings) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE settings SET company_name = ?, currency = ?, currency_symbol = ?,
		        default_low_qty = ?, default_booth_fee = ?, allow_negative_stock = ?,
		        updated_at = ?,
		        auto_backup = ?, backup_keep = ?, backup_active_hours = ?, backup_idle_days = ?
		 WHERE id = 1`,
		in.CompanyName, in.Currency, in.CurrencySymbol,
		int64(in.DefaultLowQty), int64(in.DefaultBoothFee), b2i(in.AllowNegative), Now(),
		b2i(in.AutoBackup), in.BackupKeepCount(), activeHours(*in), idleDays(*in))
	return err
}

// ---------------------------------------------------------------- 用户

const userCols = `id, username, password_hash, full_name, role, is_active, last_login_at, created_at, COALESCE(permissions, '')`

func scanUser(row interface{ Scan(...any) error }) (*model.User, error) {
	var (
		u           model.User
		isActive    int64
		permissions string
	)
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.FullName, &u.Role,
		&isActive, &u.LastLoginAt, &u.CreatedAt, &permissions); err != nil {
		return nil, err
	}
	u.IsActive = i2b(isActive)
	u.Permissions = ParsePermissions(permissions)
	return &u, nil
}

// UserByUsername 按用户名查询。
func (s *Store) UserByUsername(ctx context.Context, username string) (*model.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE username = ?`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// UserByID 按 ID 查询。
func (s *Store) UserByID(ctx context.Context, id int64) (*model.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// ListUsers 用户列表。
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// CreateUser 新建用户。
func (s *Store) CreateUser(ctx context.Context, u *model.User) (int64, error) {
	return createUser(ctx, s.db, u)
}

// CreateUserTx 在事务内新建用户。
//
// 注意：在 Store.Tx 内部必须使用带 Tx 后缀的方法。连接池只有 1 条连接，
// 在事务里改用 s.db 会等待被自己占用着的连接，导致永久阻塞。
func (s *Store) CreateUserTx(ctx context.Context, tx DBTX, u *model.User) (int64, error) {
	return createUser(ctx, tx, u)
}

func createUser(ctx context.Context, q DBTX, u *model.User) (int64, error) {
	res, err := q.ExecContext(ctx,
		`INSERT INTO users(username, password_hash, full_name, role, is_active, permissions, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.Username, u.PasswordHash, u.FullName, u.Role, b2i(u.IsActive),
		u.PermissionsText(), Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateUser 更新用户资料与角色。
func (s *Store) UpdateUser(ctx context.Context, u *model.User) error {
	return updateUser(ctx, s.db, u)
}

func activeHours(in model.Settings) int {
	if in.BackupActiveHours <= 0 {
		return 24
	}
	return in.BackupActiveHours
}

func idleDays(in model.Settings) int {
	if in.BackupIdleDays <= 0 {
		return 7
	}
	return in.BackupIdleDays
}

// TotalChanges 自本次进程启动以来的行变更总数（SQLite total_changes）。
//
// 用来判断"上次备份之后数据有没有变化"：文件 mtime 在 WAL 模式下不可靠
// （写完 mtime 与大小都可能不动），而这个计数只在真正发生写入时增长。
func (s *Store) TotalChanges(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&n)
	return n, err
}

// ParsePermissions 解析逗号分隔的权限文本。
func ParsePermissions(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// UpdateUserTx 在事务内更新用户。
func (s *Store) UpdateUserTx(ctx context.Context, tx DBTX, u *model.User) error {
	return updateUser(ctx, tx, u)
}

func updateUser(ctx context.Context, q DBTX, u *model.User) error {
	_, err := q.ExecContext(ctx,
		`UPDATE users SET username = ?, full_name = ?, role = ?, is_active = ?,
		        permissions = ? WHERE id = ?`,
		u.Username, u.FullName, u.Role, b2i(u.IsActive),
		u.PermissionsText(), u.ID)
	return err
}

// UpdatePassword 修改密码。
func (s *Store) UpdatePassword(ctx context.Context, id int64, hash string) error {
	return updatePassword(ctx, s.db, id, hash)
}

// UpdateUsernameTx 只改账号名，不动角色与权限。
func (s *Store) UpdateUsernameTx(ctx context.Context, tx DBTX, id int64, username string) error {
	_, err := tx.ExecContext(ctx, `UPDATE users SET username = ? WHERE id = ?`, username, id)
	return err
}

// UpdatePasswordTx 在事务内修改密码。
func (s *Store) UpdatePasswordTx(ctx context.Context, tx DBTX, id int64, hash string) error {
	return updatePassword(ctx, tx, id, hash)
}

func updatePassword(ctx context.Context, q DBTX, id int64, hash string) error {
	_, err := q.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// DeleteUser 删除用户。
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	return deleteUser(ctx, s.db, id)
}

// DeleteUserTx 在事务内删除用户。
func (s *Store) DeleteUserTx(ctx context.Context, tx DBTX, id int64) error {
	return deleteUser(ctx, tx, id)
}

func deleteUser(ctx context.Context, q DBTX, id int64) error {
	_, err := q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// TouchLogin 记录最后登录时间。
func (s *Store) TouchLogin(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, Now(), id)
	return err
}

// CountUsers 用户总数。
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountActiveAdmins 有效管理员数量（用于防止删掉最后一个管理员）。
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role = ? AND is_active = 1`, model.RoleAdmin).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------- 操作日志

// Log 写入操作日志。
func (s *Store) Log(ctx context.Context, tx DBTX, user *model.User, action, entity string, entityID *int64, detail string) error {
	var (
		uid  *int64
		name string
	)
	if user != nil {
		id := user.ID
		uid = &id
		name = user.DisplayName()
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO activity_logs(user_id, username, action, entity, entity_id, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		uid, name, action, entity, entityID, detail, Now())
	return err
}

// ListLogs 最近的日志。
func (s *Store) ListLogs(ctx context.Context, limit int) ([]model.ActivityLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, username, action, entity, entity_id, detail, created_at
		 FROM activity_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.ActivityLog
	for rows.Next() {
		var (
			l        model.ActivityLog
			uid      sql.NullInt64
			entityID sql.NullInt64
		)
		if err := rows.Scan(&l.ID, &uid, &l.Username, &l.Action, &l.Entity, &entityID, &l.Detail, &l.CreatedAt); err != nil {
			return nil, err
		}
		l.UserID = ptrInt(uid)
		l.EntityID = ptrInt(entityID)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ErrNotFound 目标记录不存在。
var ErrNotFound = errors.New("记录不存在")

// dirWritable 通过实际写一个临时文件判断目录是否可写。
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	_ = os.Remove(name)
	return true
}

package store

import (
	"context"
	"path/filepath"
	"testing"

	"icewine-erp/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.sqlite3"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	// 真实启动流程里 Bootstrap 会写入设置行；这里保持一致，
	// 否则后续的 UPDATE 会作用在 0 行上，测试就失去意义。
	if err := st.EnsureSettings(ctx, model.Settings{
		CompanyName:    "SoulFound",
		Currency:       "CNY",
		CurrencySymbol: "¥",
		DefaultLowQty:  model.QtyFromInt(6),
	}); err != nil {
		t.Fatalf("写入默认设置失败: %v", err)
	}
	return st
}

// rollbackMigration 把某个版本的记录删掉，模拟"停留在旧版本的库"。
func rollbackMigration(t *testing.T, st *Store, version int) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(),
		`DELETE FROM schema_meta WHERE version = ?`, version); err != nil {
		t.Fatalf("回退迁移版本失败: %v", err)
	}
}

// TestMigrationV2UpgradesLegacyDefaults 验证旧库的加元设置会被升级成人民币。
//
// settings 表只在首次启动写入，所以代码默认值变了也必须靠迁移来修正存量数据，
// 否则老实例会一直显示加元。
func TestMigrationV2UpgradesLegacyDefaults(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// 造出"旧库"的样子：停留在 v1，且设置沿用旧的默认值
	rollbackMigration(t, st, 2)
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE settings SET currency = 'CAD', currency_symbol = 'C$',
		        company_name = '加拿大冰酒' WHERE id = 1`); err != nil {
		t.Fatalf("准备旧数据失败: %v", err)
	}

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("重新迁移失败: %v", err)
	}
	got, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Currency != "CNY" || got.CurrencySymbol != "¥" {
		t.Errorf("旧库应升级为人民币，实际 %s / %s", got.Currency, got.CurrencySymbol)
	}
	if got.CompanyName != "SoulFound" {
		t.Errorf("旧库的公司名应升级为 SoulFound，实际 %q", got.CompanyName)
	}

	// 用户自己设置过的币种不能被迁移覆盖
	rollbackMigration(t, st, 2)
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE settings SET currency = 'USD', currency_symbol = 'US$' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Settings(ctx)
	if got.Currency != "USD" || got.CurrencySymbol != "US$" {
		t.Errorf("用户自定义的币种不应被迁移覆盖，实际 %s / %s", got.Currency, got.CurrencySymbol)
	}
}

// TestFreshInstallUsesRMB 新库应当直接是人民币。
func TestFreshInstallUsesRMB(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.EnsureSettings(ctx, model.Settings{
		CompanyName: "SoulFound", Currency: "CNY", CurrencySymbol: "¥",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Currency != "CNY" || got.CurrencySymbol != "¥" {
		t.Errorf("新库应为人民币，实际 %s / %s", got.Currency, got.CurrencySymbol)
	}
}

// TestSchemaVersionRecorded 迁移版本应被正确记录。
func TestSchemaVersionRecorded(t *testing.T) {
	st := newTestStore(t)
	v, err := st.SchemaVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Errorf("数据库版本应为 %d，实际 %d", len(migrations), v)
	}
}

// TestBackupIsRestorable 备份出来的文件应当是可用的数据库。
func TestBackupIsRestorable(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.EnsureSettings(ctx, model.Settings{
		CompanyName: "SoulFound", Currency: "CNY", CurrencySymbol: "¥",
	}); err != nil {
		t.Fatal(err)
	}
	target, err := st.Backup(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	copy, err := Open(target)
	if err != nil {
		t.Fatalf("打开备份文件失败: %v", err)
	}
	defer copy.Close()
	got, err := copy.Settings(ctx)
	if err != nil {
		t.Fatalf("读取备份设置失败: %v", err)
	}
	if got.Currency != "CNY" {
		t.Errorf("备份内容不正确，币种 = %s", got.Currency)
	}
}

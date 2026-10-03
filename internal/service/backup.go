package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"icewine-erp/internal/model"
)

// bootID 本次进程的唯一标识，用来识别 total_changes 是否已经归零。
var bootIDSeq int64

func newBootID() string {
	bootIDSeq = time.Now().UnixNano()
	return fmt.Sprintf("%d-%d", os.Getpid(), bootIDSeq)
}

// BackupDir 备份目录；配置没给时退回数据目录下的 backups/。
func (s *Service) BackupDir() string {
	if dir := strings.TrimSpace(s.Cfg.BackupDir); dir != "" {
		return dir
	}
	return filepath.Join(s.Cfg.DataDir, "backups")
}

// BackupInfo 一份备份文件的概况。
type BackupInfo struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
}

// BackupSizeText 便于阅读的大小。
func (b BackupInfo) SizeText() string { return ImageSizeText(int(b.Size)) }

// TimeText 备份时间，本地时区。
func (b BackupInfo) TimeText() string { return b.ModTime.Format("2006-01-02 15:04") }

// ListBackups 列出备份目录里的备份，新的在前。
func (s *Service) ListBackups() ([]BackupInfo, error) {
	return s.listBackups()
}

func (s *Service) listBackups() ([]BackupInfo, error) {
	dir := s.BackupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]BackupInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sqlite3") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupInfo{
			Name:    e.Name(),
			Path:    filepath.Join(dir, e.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// BackupNow 立即备份一次，并按保留份数清理旧文件。
func (s *Service) BackupNow(ctx context.Context, actor *model.User) (*BackupInfo, error) {
	target, err := s.Store.Backup(ctx, s.BackupDir())
	if err != nil {
		return nil, err
	}
	info, err := s.backupInfo(target)
	if err != nil {
		return nil, err
	}

	s.pruneBackups(ctx)
	s.logBackup(ctx, actor, "手动备份", info.Name+"（"+info.SizeText()+"）")
	s.markBackupDone(ctx)
	return info, nil
}

// signaturePath 记录"上次备份完成时的数据时间戳"。
//
// 不能直接拿备份文件的 mtime 作比较基准：备份完成后还会写一条操作日志，
// 那次写入会把数据库的修改时间顶到备份文件之后，导致永远判定为"有变化"。
func (s *Service) signaturePath() string {
	return filepath.Join(s.Cfg.DataDir, "last-backup.txt")
}

// markBackupDone 记录"这次备份完成时"的数据状态，作为下次判断变化的基准。
//
// 记录三样东西：
//   - 启动标识：total_changes 会随进程重启归零，重启后不能拿旧计数比较
//   - 行变更计数：只在真正发生写入时增长，比文件 mtime 可靠
//   - 文件修改时间：兜底外部直接改库文件的情况
func (s *Service) markBackupDone(ctx context.Context) {
	count, err := s.Store.TotalChanges(ctx)
	if err != nil {
		count = -1
	}
	at := s.dataModifiedAt()
	if at.IsZero() {
		at = time.Now()
	}
	line := fmt.Sprintf("%s %d %s", s.bootID, count, at.Format(time.RFC3339Nano))
	_ = os.WriteFile(s.signaturePath(), []byte(line), 0o644)
}

// lastBackupSignature 读取上次备份完成时的数据状态。
func (s *Service) lastBackupSignature() (bootID string, count int64, at time.Time, ok bool) {
	raw, err := os.ReadFile(s.signaturePath())
	if err != nil {
		return "", 0, time.Time{}, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 3 {
		return "", 0, time.Time{}, false
	}
	count, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return "", 0, time.Time{}, false
	}
	at, err = time.Parse(time.RFC3339Nano, fields[2])
	if err != nil {
		return "", 0, time.Time{}, false
	}
	return fields[0], count, at, true
}

// dataChangedSinceBackup 上次备份之后数据有没有变化。
//
// 判定不了的时候一律返回 true —— 宁可多备一份，也不要漏掉变化。
func (s *Service) dataChangedSinceBackup(ctx context.Context) bool {
	bootID, count, at, ok := s.lastBackupSignature()
	if !ok {
		return true
	}
	// 进程重启过：计数已归零，无法比较，保守认为有变化
	if bootID != s.bootID {
		return true
	}
	if cur, err := s.Store.TotalChanges(ctx); err == nil && cur != count {
		return true
	}
	// 兜底：库文件被外部改动过（例如手工替换、从备份恢复）
	if modified := s.dataModifiedAt(); !modified.IsZero() && modified.After(at) {
		return true
	}
	return false
}

// dataModifiedAt 数据库最近一次被写入的时间。
//
// WAL 模式下写入先落到 -wal 文件，主库文件要等检查点才更新，
// 所以两个文件取较新的那个。
func (s *Service) dataModifiedAt() time.Time {
	latest := time.Time{}
	for _, name := range []string{s.Cfg.DBPath, s.Cfg.DBPath + "-wal"} {
		info, err := os.Stat(name)
		if err != nil {
			continue
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
	}
	return latest
}

// AutoBackupDecision 自动备份的判断结果，便于测试与界面展示。
type AutoBackupDecision struct {
	Should   bool
	Reason   string
	LastAt   time.Time
	HasAny   bool
	Changed  bool // 上次备份之后数据有没有变化
	Interval time.Duration
}

// DecideAutoBackup 判断现在要不要自动备份。
//
// 策略（用户可调）：
//   - 上次备份之后数据有变化 → 间隔达到「有变化间隔」就备份，默认每天
//   - 一直没有变化 → 间隔拉长到「无变化间隔」，默认每 7 天，避免堆积一堆一样的文件
func (s *Service) DecideAutoBackup(ctx context.Context, now time.Time) (AutoBackupDecision, error) {
	var d AutoBackupDecision

	st, err := s.Store.Settings(ctx)
	if err != nil {
		return d, err
	}
	if !st.AutoBackup {
		d.Reason = "自动备份已关闭"
		return d, nil
	}

	list, err := s.listBackups()
	if err != nil {
		return d, err
	}
	if len(list) == 0 {
		d.Should = true
		d.Reason = "还没有任何备份"
		d.Interval = st.BackupActiveInterval()
		return d, nil
	}

	d.HasAny = true
	d.LastAt = list[0].ModTime
	d.Changed = s.dataChangedSinceBackup(ctx)
	if d.Changed {
		d.Interval = st.BackupActiveInterval()
	} else {
		d.Interval = st.BackupIdleInterval()
	}

	elapsed := now.Sub(d.LastAt)
	if elapsed < d.Interval {
		d.Reason = fmt.Sprintf("距上次备份 %s，未到间隔 %s",
			humanDuration(elapsed), humanDuration(d.Interval))
		return d, nil
	}
	d.Should = true
	if d.Changed {
		d.Reason = "数据有变化且已到备份间隔"
	} else {
		d.Reason = "长时间未备份，做一次兜底备份"
	}
	return d, nil
}

// MaybeAutoBackup 后台定时调用：按策略决定要不要备份。
func (s *Service) MaybeAutoBackup(ctx context.Context, now time.Time) (*BackupInfo, error) {
	d, err := s.DecideAutoBackup(ctx, now)
	if err != nil || !d.Should {
		return nil, err
	}
	info, err := s.Store.Backup(ctx, s.BackupDir())
	if err != nil {
		return nil, err
	}
	s.pruneBackups(ctx)
	bi, err := s.backupInfo(info)
	if err != nil {
		return nil, err
	}
	s.logBackup(ctx, nil, "自动备份", bi.Name+"（"+bi.SizeText()+"，"+d.Reason+"）")
	s.markBackupDone(ctx)
	return bi, nil
}

// pruneBackups 只保留最近 N 份备份。
func (s *Service) pruneBackups(ctx context.Context) {
	st, err := s.Store.Settings(ctx)
	if err != nil {
		return
	}
	keep := st.BackupKeepCount()
	list, err := s.listBackups()
	if err != nil || len(list) <= keep {
		return
	}
	for _, old := range list[keep:] {
		_ = os.Remove(old.Path)
	}
}

// DeleteBackup 删除一份备份。
func (s *Service) DeleteBackup(ctx context.Context, name string, actor *model.User) error {
	info, err := s.findBackup(name)
	if err != nil {
		return err
	}
	if err := os.Remove(info.Path); err != nil {
		return fmt.Errorf("删除备份失败: %w", err)
	}
	s.logBackup(ctx, actor, "删除备份", info.Name)
	return nil
}

// logBackup 写一条操作日志。Store.Log 需要一个事务句柄，
// 这里包一个只读事务失败也无所谓——备份本身已经成功，不该因此报错。
func (s *Service) logBackup(ctx context.Context, actor *model.User, action, detail string) {
	_ = s.Store.Tx(ctx, func(tx *sql.Tx) error {
		return s.Store.Log(ctx, tx, actor, action, "system", nil, detail)
	})
}

// FindBackup 按文件名取备份（下载用），并保证不会跳出备份目录。
func (s *Service) FindBackup(name string) (*BackupInfo, error) {
	return s.findBackup(name)
}

func (s *Service) findBackup(name string) (*BackupInfo, error) {
	clean := filepath.Base(strings.TrimSpace(name))
	if clean == "" || clean == "." || clean == ".." || !strings.HasSuffix(clean, ".sqlite3") {
		return nil, UserErrf("备份文件不存在")
	}
	full := filepath.Join(s.BackupDir(), clean)
	// 双保险：确认最终路径仍在备份目录内
	if filepath.Dir(full) != filepath.Clean(s.BackupDir()) {
		return nil, UserErrf("备份文件不存在")
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return nil, UserErrf("备份文件不存在")
	}
	return &BackupInfo{Name: clean, Path: full, Size: info.Size(), ModTime: info.ModTime()}, nil
}

func (s *Service) backupInfo(path string) (*BackupInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &BackupInfo{
		Name: filepath.Base(path), Path: path, Size: info.Size(), ModTime: info.ModTime(),
	}, nil
}

// humanDuration 把时长写成"3 小时前 / 2 天"这类中文。
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "不到 1 分钟"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}

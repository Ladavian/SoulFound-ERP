package store

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoPoolQueriesInTxHelpers 接收事务的存储方法内部不许碰连接池。
//
// 连接池上限是 1（见 Open 里的 SetMaxOpenConns(1)）。
// 方法签名里带了 tx，说明调用方已经在事务里；这时内部如果再去走连接池，
// 唯一连接被事务占着，就会自死锁——表现是"页面卡到请求超时"，极难查。
// 已经踩过两次：SaveGroupOrder，以及 AddEcLink / DeleteEcLink。
//
// 检查分两步，因为死锁常常是**间接**发生的：
//  1. 找出"只用连接池、不接受事务"的方法（内部出现 s.db.）；
//  2. 带事务的方法里一旦调用这些方法，就说明它在事务里碰了连接池。
//
// 只查直接写 s.db 会漏掉 AddEcLink 那种"调了个内部用 s.db 的私有函数"。
func TestNoPoolQueriesInTxHelpers(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	headerRe := regexp.MustCompile(`func \(s \*Store\) (\w+)\(([^)]*)\)`)
	dbRefRe := regexp.MustCompile(`s\.db\b`)
	// 去掉注释再匹配，否则注释里写"s.db 会死锁"都会被当成违规
	stripComments := func(src string) string {
		src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
		return regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(src, "")
	}

	type method struct {
		file  string
		line  int
		name  string
		hasTx bool
		body  string
	}
	var methods []method
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(raw), "\n")
		for i := 0; i < len(lines); i++ {
			m := headerRe.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			depth := strings.Count(lines[i], "{") - strings.Count(lines[i], "}")
			j := i
			for j < len(lines) && depth > 0 {
				j++
				if j >= len(lines) {
					break
				}
				depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			}
			methods = append(methods, method{
				file: file, line: i + 1, name: m[1],
				hasTx: strings.Contains(m[2], "tx DBTX") || strings.Contains(m[2], "tx *sql.Tx"),
				body:  stripComments(strings.Join(lines[i:j], "\n")),
			})
		}
	}

	// 第一步：只用连接池、不接受事务的方法
	poolOnly := map[string]bool{}
	for _, m := range methods {
		// s.db 后面可能是 . 也可能是 , （作为参数传下去），统一按词匹配
		if !m.hasTx && dbRefRe.MatchString(m.body) {
			poolOnly[m.name] = true
		}
	}
	if len(poolOnly) == 0 {
		t.Fatal("没有识别出任何连接池方法，检查范围异常")
	}

	// 第二步：带事务的方法里不许直接写 s.db，也不许调用连接池方法
	checked := 0
	for _, m := range methods {
		if !m.hasTx || m.name == "Tx" {
			continue
		}
		checked++
		if dbRefRe.MatchString(m.body) {
			t.Errorf("%s:%d 的 %s 收了事务参数却直接用 s.db："+
				"连接池只有 1 个连接、事务占着它，会自死锁（卡到请求超时）",
				m.file, m.line, m.name)
		}
		for name := range poolOnly {
			if name == m.name {
				continue
			}
			if regexp.MustCompile(`\b(?:s|store)\.` + name + `\(`).MatchString(m.body) {
				t.Errorf("%s:%d 的 %s 在事务里调用了 %s()（该方法只走连接池）："+
					"会自死锁；请改用带 tx 的版本，或把查询挪到事务外",
					m.file, m.line, m.name, name)
			}
		}
	}
	if checked < 8 {
		t.Fatalf("只检查到 %d 个带事务的存储方法，范围异常", checked)
	}
	t.Logf("已检查 %d 个带事务的存储方法，识别出 %d 个连接池方法", checked, len(poolOnly))
}

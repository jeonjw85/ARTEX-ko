package traffic

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// openLegacyIndex builds the index exactly as the pre-reclamation Open did: a
// plain-path DSN, pragmas via the pool, and auto_vacuum left at its default 0.
func openLegacyIndex(t *testing.T, dir string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "_index"), 0o755); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", filepath.Join(dir, "_index", "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000"} {
		if _, err := old.Exec(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.Exec(indexSchema); err != nil {
		t.Fatal(err)
	}
	return old
}

// TestUpgradeFromOldInstall guards the upgrade path. Open now names the database
// through a file: URI so per-connection pragmas can ride in the DSN, and a
// driver that did not treat that as a URI would quietly open a file literally
// named "file:/…" — an empty index, with every recorded exchange apparently
// gone. The assertions below are what prove that does not happen.
func TestUpgradeFromOldInstall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "_index", "index.sqlite")
	old := openLegacyIndex(t, dir)
	if _, err := old.Exec(ftsSchema); err != nil {
		t.Fatal(err)
	}
	// 1개를 포함한 3개의 과거 트래픽 흐름 legacy path<>'' 행
	for i, row := range [][]any{
		{"1700000000-0001", "old.example.com", ""},
		{"1700000000-0002", "old.example.com", ""},
		{"1700000000-0003", "legacy.example.com", "legacy.example.com/GET/x"},
	} {
		if _, err := old.Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,'GET','/x','http://x/x',200,'text/html',0,9,?)`, row[0], 1700000000+i, row[1], row[2]); err != nil {
			t.Fatal(err)
		}
		if _, err := old.Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,resp_head,resp_body)
VALUES(?,'GET /x','','HTTP 200','이전 데이터 텍스트')`, row[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := old.Exec(`INSERT INTO ex_fts(rowid,content) VALUES(?,?)`, i+1, "이전 데이터 텍스트 secret-token"); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// ---- 새 버전이 인계됩니다.
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("새 버전에서는 이전 라이브러리를 열 수 없습니다: %v", err)
	}
	defer tr.Close()

	// 1. 동일한 파일이어야 하며, 빈 라이브러리를 새로 열 수는 없습니다.
	if st2, err := os.Stat(path); err != nil || st2.Size() == 0 {
		t.Fatalf("원본 인덱스 파일이 비정상입니다: size=%v err=%v", st2, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "_index")); len(entries) > 3 {
		for _, e := range entries {
			t.Logf("_아래 색인: %s", e.Name())
		}
		t.Fatal("_index 아래에 예상치 못한 파일이 나타납니다. DSN는 다른 라이브러리를 가리킬 수 있습니다.")
	}
	t.Logf("이전 라이브러리는 %d 바이트입니다. 새 버전이 적용된 후에도 여전히 동일한 파일입니다.", stat.Size())

	// 2. 모든 과거 데이터가 표시됩니다.
	n, err := tr.Count()
	if err != nil || n != 3 {
		t.Fatalf("Count=(%d,%v)는 (3,nil)여야 합니다. - 기록 트래픽이 손실됩니다.", n, err)
	}
	// 3. 기록 전체 텍스트 색인은 계속 검색 가능합니다.
	if tr.fts {
		rows, err := tr.query("old.example.com", "", "secret-token", 0, 10)
		if err != nil {
			t.Fatalf("기록 전체 텍스트 검색 실패: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("기록 전체 텍스트 검색은 2가 되어야 하는 %d를 적중합니다.", len(rows))
		}
	}
	// 4. 역사적 텍스트는 여전히 읽을 수 있습니다
	if _, resp, err := tr.Get("1700000000-0001"); err != nil {
		t.Fatalf("기록 텍스트를 읽지 못했습니다: %v", err)
	} else if resp == "" {
		t.Fatal("기록 응답이 비어 있습니다.")
	}
	// 5. 이전 라이브러리는 증분 재활용이 활성화된 것으로 잘못 판단되지 않습니다.
	if tr.incrementalVacuum {
		t.Fatal("기존 라이브러리가 증분 재활용이 활성화된 것으로 잘못 판단되었습니다.")
	}
	// 6. 삭제는 여전히 정상적으로 작동하며 재활용 프로세스는 이전 데이터베이스에 수렴될 수 있습니다.
	deleted, err := tr.DeleteHostsExact([]string{"old.example.com"})
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteHostsExact=(%d,%v)는 (2,nil)이어야 합니다.", deleted, err)
	}
	tr.reaping.Wait()
	if n, err := tr.Count(); err != nil || n != 1 {
		t.Fatalf("Count=(%d,%v)를 삭제하면 (1,nil)가 되어야 합니다.", n, err)
	}
	// 7. legacy path<>'' 라인은 연루되지 않았습니다
	var legacyPath string
	if err := tr.DB().QueryRow(`SELECT path FROM exchanges`).Scan(&legacyPath); err != nil {
		t.Fatal(err)
	}
	if legacyPath == "" {
		t.Fatal("legacy 행의 path가 지워졌습니다.")
	}
}

// TestDowngradeToOldBinary covers a rollback: a database created with
// auto_vacuum=incremental must stay readable and writable by a build that knows
// nothing about it. auto_vacuum only changes where SQLite tracks free pages, so
// the old binary simply goes back to never returning them.
func TestDowngradeToOldBinary(t *testing.T) {
	dir := t.TempDir()
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.incrementalVacuum {
		t.Fatal("새로운 라이브러리는 점진적인 재활용을 가능하게 해야 합니다.")
	}
	bulkRecord(tr, "keep.example.com", 5, 100*1024)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	old := openLegacyIndex(t, dir) // 이전 버전 바이너리가 대신합니다.
	defer old.Close()
	var n int
	if err := old.QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("이전 버전에서는 (%d,%v)로 읽는데, 이는 (5,nil)여야 합니다.", n, err)
	}
	if _, err := old.Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES('x',1,'new.example.com','GET','/x','http://x/x',200,'',0,0,'')`); err != nil {
		t.Fatalf("이전 버전을 쓰지 못했습니다: %v", err)
	}
	if _, err := old.Exec(`DELETE FROM exchanges WHERE host='keep.example.com'`); err != nil {
		t.Fatalf("이전 버전 삭제 실패: %v", err)
	}
}

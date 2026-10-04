package traffic

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bulkRecord fills the index with inline bodies — the ones that actually make
// index.sqlite grow. Binary content type keeps them out of the full-text index so
// the test stays fast; the FTS side is covered by TestReclaimMergesFTSTombstones.
func bulkRecord(tr *Traffic, host string, n, size int) {
	body := []byte(strings.Repeat("A", size))
	for i := 0; i < n; i++ {
		tr.record(newFlow(host, "GET", fmt.Sprintf("/blob/%d", i), nil, body,
			withRespType("application/octet-stream")))
	}
}

func TestNewIndexEnablesIncrementalVacuum(t *testing.T) {
	tr, _ := openTraffic(t)
	if !tr.incrementalVacuum {
		t.Fatal("새 인덱스 라이브러리는 증분 재활용을 활성화하지 않습니다.")
	}
	var mode int
	if err := tr.DB().QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != autoVacuumIncremental {
		t.Fatalf("auto_vacuum=%d, %d여야 합니다.", mode, autoVacuumIncremental)
	}
}

// TestDeleteReclaimsIndexSpace is the regression: deleting traffic used to leave
// index.sqlite at its high-water mark forever, because SQLite only chains freed
// pages onto its freelist and nothing ever returned them to the filesystem.
func TestDeleteReclaimsIndexSpace(t *testing.T) {
	tr, _ := openTraffic(t)
	const host = "bulk.example.com"
	// 30 × 200KB stays under maxInlineBody, so every body lands in the database
	// itself rather than the blob store — that is where the growth was invisible.
	bulkRecord(tr, host, 30, 200*1024)
	grown := tr.indexBytes()
	if grown < 5<<20 {
		t.Fatalf("인덱스는 %d 바이트에 불과하며 재활용을 검증하기에는 샘플이 충분하지 않습니다.", grown)
	}

	if n, err := tr.DeleteHostsExact([]string{host}); err != nil || n != 30 {
		t.Fatalf("DeleteHostsExact=(%d,%v)는 (30,nil)이어야 합니다.", n, err)
	}
	tr.reaping.Wait() // 재활용은 백그라운드에서 덩어리로 발생합니다.

	after := tr.indexBytes()
	if after > grown/4 {
		t.Fatalf("삭제 후에도 인덱스는 여전히 %d바이트(삭제 전 %d)를 차지하고 해당 공간은 파일 시스템에 반환되지 않습니다.", after, grown)
	}
	// A handful of pages incremental_vacuum could not move to the end of the file
	// is a normal residual; the ~1500 that the deletion freed must be gone.
	var free int
	if err := tr.DB().QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if free > 64 {
		t.Fatalf("아직 회수되지 않은 %d 무료 페이지가 있습니다.", free)
	}
}

// TestReclaimMergesFTSTombstones covers the second half of the leak: ex_fts is a
// contentless_delete index, so a DELETE only writes tombstones. Without a merge
// the index keeps growing on every deletion — deleting traffic made it bigger.
func TestReclaimMergesFTSTombstones(t *testing.T) {
	tr, _ := openTraffic(t)
	if !tr.fts {
		t.Skip("드라이버가 활성화되지 않음 FTS5")
	}
	// Deleted in batches, which is what leaves tombstones spread over many
	// segments rather than emptying the index in one shot.
	for round := 0; round < 4; round++ {
		host := fmt.Sprintf("fts%d.example.com", round)
		for i := 0; i < 20; i++ {
			tr.record(newFlow(host, "GET", fmt.Sprintf("/p/%d", i), nil,
				[]byte(strings.Repeat("secret token 한국어 텍스트 padding ", 200))))
		}
		if _, err := tr.DeleteHostsExact([]string{host}); err != nil {
			t.Fatal(err)
		}
		tr.reaping.Wait()
	}

	var exchanges, segments int
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM exchanges`).Scan(&exchanges); err != nil {
		t.Fatal(err)
	}
	if err := tr.DB().QueryRow(`SELECT COUNT(*) FROM ex_fts_data`).Scan(&segments); err != nil {
		t.Fatal(err)
	}
	if exchanges != 0 {
		t.Fatalf("%d 트래픽이 남아 있습니다.", exchanges)
	}
	// A fully merged, empty contentless index keeps only its structure rows.
	if segments > 8 {
		t.Fatalf("전체 텍스트 인덱스 잔여 %d 행 세그먼트 데이터, tombstone가 병합 및 재활용되지 않았습니다.", segments)
	}
}

// TestReclaimOnLegacyIndexIsHarmless covers installs created before
// auto_vacuum=incremental became the default: incremental_vacuum is a silent
// no-op there, so reclamation must report the situation and finish rather than
// spin or fail. Only a full compaction can convert such a file.
func TestReclaimOnLegacyIndexIsHarmless(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "_index"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create the tables first, with auto_vacuum left at its default 0 — exactly the
	// shape Open used to leave behind.
	legacy, err := sql.Open("sqlite", filepath.Join(dir, "_index", "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(indexSchema); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	if tr.incrementalVacuum {
		t.Fatal("이전 라이브러리는 증분 재활용이 활성화되었다고 보고하면 안 됩니다.")
	}

	const host = "legacy.example.com"
	bulkRecord(tr, host, 8, 200*1024)
	if n, err := tr.DeleteHostsExact([]string{host}); err != nil || n != 8 {
		t.Fatalf("DeleteHostsExact=(%d,%v)는 (8,nil)이어야 합니다.", n, err)
	}
	tr.reaping.Wait() // 자제해야 하며 예산에 얽매여서는 안 됩니다.

	// The freelist stays populated: that is the whole reason a compaction entry
	// point is needed for pre-existing databases.
	var free int
	if err := tr.DB().QueryRow(`PRAGMA freelist_count`).Scan(&free); err != nil {
		t.Fatal(err)
	}
	if free == 0 {
		t.Fatal("이전 라이브러리는 실제로 무료 페이지를 재활용했는데, 이는 테스트에서 실제로 이전 라이브러리를 구성하지 않았음을 나타냅니다.")
	}
}

// TestDeleteAllPurgesAndCompacts covers the page's clear-everything action: it
// must leave nothing behind — including host directories the index no longer
// knows about — and it must hand the index space back, since an emptied index is
// the one moment a full rewrite is cheap.
func TestDeleteAllPurgesAndCompacts(t *testing.T) {
	tr, dir := openTraffic(t)
	bulkRecord(tr, "a.example.com", 10, 200*1024)
	bulkRecord(tr, "b.example.com", 10, 200*1024)
	// A text body so the full-text index has real content, and a spilled one so a
	// blob exists to collect.
	tr.record(newFlow("c.example.com", "GET", "/page", nil, []byte(strings.Repeat("secret-token ", 500))))
	tr.record(newFlow("c.example.com", "GET", "/big", nil,
		[]byte(strings.Repeat("B", maxInlineBody+1024)), withRespType("application/sql")))
	// An orphaned legacy directory: no index row points at it, so only a
	// clear-everything should take it.
	orphan := filepath.Join(dir, "orphan.example.com")
	if err := os.MkdirAll(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	grown := tr.indexBytes()
	if grown < 5<<20 {
		t.Fatalf("인덱스는 %d 바이트에 불과하며 샘플이 부족합니다.", grown)
	}

	deleted, reclaimed, err := tr.DeleteAll()
	if err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if deleted != 22 {
		t.Fatalf("deleted=%d, 22여야 합니다.", deleted)
	}
	tr.reaping.Wait()

	if reclaimed < grown/2 {
		t.Fatalf("%d 바이트만 재활용되었습니다(삭제 전 인덱스 %d).", reclaimed, grown)
	}
	if after := tr.indexBytes(); after > grown/8 {
		t.Fatalf("삭제 후에도 인덱스는 여전히 %d 바이트를 차지합니다(삭제 전 %d).", after, grown)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM exchanges`,
		`SELECT COUNT(*) FROM exchange_bodies`,
		`SELECT COUNT(*) FROM blob_refs`,
	} {
		var c int
		if err := tr.DB().QueryRow(q).Scan(&c); err != nil {
			t.Fatal(err)
		}
		if c != 0 {
			t.Fatalf("%s = %d, 0이어야 합니다.", q, c)
		}
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("고아 기록 host 정리되지 않은 디렉터리: %v", err)
	}
	// Recording must keep working against the freshly rewritten file.
	tr.record(newFlow("d.example.com", "GET", "/after", nil, []byte("삭제 후에도 녹화 가능")))
	if n, err := tr.Count(); err != nil || n != 1 {
		t.Fatalf("삭제 후 Count=(%d,%v)는 (1,nil)이어야 합니다.", n, err)
	}
}

// TestDeleteAllConvertsLegacyIndex is why the purge compacts rather than just
// deleting: auto_vacuum cannot be switched on after the fact except through a
// VACUUM, and an emptied index is the cheapest place to pay for one. After this,
// ordinary deletions reclaim space on their own.
func TestDeleteAllConvertsLegacyIndex(t *testing.T) {
	dir := t.TempDir()
	old := openLegacyIndex(t, dir)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	tr, err := Open(dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	if tr.incrementalVacuum {
		t.Fatal("이전 라이브러리는 증분 재활용이 활성화되었다고 보고하면 안 됩니다.")
	}

	bulkRecord(tr, "legacy.example.com", 10, 200*1024)
	if _, _, err := tr.DeleteAll(); err != nil {
		t.Fatalf("DeleteAll: %v", err)
	}
	if !tr.incrementalVacuum {
		t.Fatal("기존 데이터베이스를 지운 후 증분 재활용 모드로 변환되지 않았습니다.")
	}

	// The converted database now reclaims on an ordinary host deletion.
	bulkRecord(tr, "again.example.com", 10, 200*1024)
	grown := tr.indexBytes()
	if _, err := tr.DeleteHostsExact([]string{"again.example.com"}); err != nil {
		t.Fatal(err)
	}
	tr.reaping.Wait()
	if after := tr.indexBytes(); after > grown/4 {
		t.Fatalf("변환 후 일반 삭제가 아직 복구되지 않은 경우: %d바이트(삭제 전 %d)", after, grown)
	}
}

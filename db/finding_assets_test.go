package db

import (
	"strconv"
	"strings"
	"testing"
)

// cleanupTreeFixtures 사용 사례에서 생성된 자산 및 결과를 삭제합니다. defer로 등록해야 합니다(대신
// t.Cleanup): t.Cleanup는 테스트 기능이 반환된 후 실행됩니다. 그 당시 defer d.Close()는 연결을 종료했습니다.
// 정리는 자동으로 실패하고 공유 개발 저장소에 더티 데이터를 남깁니다.
func cleanupTreeFixtures(d *DB, taskID int64, rootDomains ...string) {
	d.Exec(`DELETE FROM assets WHERE root_domain = ANY($1::text[])`, rootDomains) //nolint:errcheck
	d.DeleteFindingsByTask(taskID)                                                //nolint:errcheck
}

// seedTreeAsset inserts one asset row.
func seedTreeAsset(t *testing.T, d *DB, kind string, cols map[string]any) int64 {
	t.Helper()
	names := []string{"type"}
	values := []any{kind}
	placeholders := []string{"$1"}
	for k, v := range cols {
		values = append(values, v)
		names = append(names, k)
		placeholders = append(placeholders, "$"+strconv.Itoa(len(values)))
	}
	q := "INSERT INTO assets(" + strings.Join(names, ",") + ") VALUES (" +
		strings.Join(placeholders, ",") + ") RETURNING id"
	var id int64
	if err := d.QueryRow(q, values...).Scan(&id); err != nil {
		t.Fatalf("seed %s asset: %v", kind, err)
	}
	return id
}

func nodeByKey(tree *FindingAssetTree, key string) *FindingAssetNode {
	for i := range tree.Nodes {
		if tree.Nodes[i].Key == key {
			return &tree.Nodes[i]
		}
	}
	return nil
}

// TestBuildFindingAssetTree covers the whole shape of the 「자산별」tree: the
// root→subdomain→service→endpoint chain gets rebuilt from a finding that only
// points at the leaf, ancestors aggregate their subtree, assets without any
// finding stay out, and a finding whose asset row is gone lands in the
// unassigned bucket.
func TestBuildFindingAssetTree(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("자산 트리 테스트", "목표", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)

	const root = "tree-test.example"
	const sub = "api.tree-test.example"
	defer cleanupTreeFixtures(d, tk.ID, root)
	rootID := seedTreeAsset(t, d, "root_domain", map[string]any{"domain": root, "root_domain": root})
	subID := seedTreeAsset(t, d, "subdomain", map[string]any{"domain": sub, "root_domain": root})
	svcID := seedTreeAsset(t, d, "service", map[string]any{
		"domain": sub, "root_domain": root, "url": "https://" + sub, "port": 443, "service_type": "http",
	})
	epID := seedTreeAsset(t, d, "endpoint", map[string]any{
		"domain": sub, "root_domain": root, "url": "https://" + sub + "/admin", "port": 443, "method": "GET",
	})
	// 동일한 도메인 이름 아래의 다른 서비스는 검색되지 않으므로 트리에 표시되어서는 안 됩니다.
	seedTreeAsset(t, d, "service", map[string]any{
		"domain": sub, "root_domain": root, "url": "http://" + sub + ":8080", "port": 8080, "service_type": "http",
	})

	// 가장 깊은 endpoint에만 발견물을 걸고 조상 체인은 닥나무 자체로 구성되어야 합니다.
	if _, err := d.AddFinding(tk.ID, 0, "XSS", "반사형 XSS", "high", "s", "e", "w", []int64{epID}); err != nil {
		t.Fatal(err)
	}
	// Self와 Total의 차이점을 확인하기 위해 서비스에 직접 연결되는 것입니다.
	if _, err := d.AddFinding(tk.ID, 0, "Info", "정보 유출", "low", "s", "e", "w", []int64{svcID}); err != nil {
		t.Fatal(err)
	}
	// 자산 행이 존재하지 않습니다(자산 삭제됨) → 버킷이 연결되지 않았습니다.
	if _, err := d.AddFinding(tk.ID, 0, "Misc", "유아", "medium", "s", "e", "w", []int64{999000111}); err != nil {
		t.Fatal(err)
	}

	tree, err := d.BuildFindingAssetTree(FindingFilter{TaskID: strconv.FormatInt(tk.ID, 10)})
	if err != nil {
		t.Fatal(err)
	}
	if tree.FindingTotal != 3 {
		t.Fatalf("finding_total: want 3, got %d", tree.FindingTotal)
	}

	rootNode := nodeByKey(tree, assetKey(rootID))
	subNode := nodeByKey(tree, assetKey(subID))
	svcNode := nodeByKey(tree, assetKey(svcID))
	epNode := nodeByKey(tree, assetKey(epID))
	for name, n := range map[string]*FindingAssetNode{
		"root": rootNode, "subdomain": subNode, "service": svcNode, "endpoint": epNode,
	} {
		if n == nil {
			t.Fatalf("%s node missing from tree", name)
		}
	}

	// 아버지-자식 체인: endpoint → service → subdomain → root_domain.
	if epNode.Parent != svcNode.Key {
		t.Errorf("endpoint parent: want %s, got %s", svcNode.Key, epNode.Parent)
	}
	if svcNode.Parent != subNode.Key {
		t.Errorf("service parent: want %s, got %s", subNode.Key, svcNode.Parent)
	}
	if subNode.Parent != rootNode.Key {
		t.Errorf("subdomain parent: want %s, got %s", rootNode.Key, subNode.Parent)
	}
	if rootNode.Parent != "" {
		t.Errorf("root parent: want top level, got %s", rootNode.Parent)
	}

	// 집계: 2개의 루트 도메인 이름(endpoint의 high + service의 low), 하나는 자체용이고 2개의 하위 트리는 service용입니다.
	if rootNode.Total != 2 || rootNode.High != 1 || rootNode.Low != 1 {
		t.Errorf("root totals: want 2/high1/low1, got %d/high%d/low%d", rootNode.Total, rootNode.High, rootNode.Low)
	}
	if rootNode.Self != 0 {
		t.Errorf("root self: want 0(그냥 조상), got %d", rootNode.Self)
	}
	if svcNode.Total != 2 || svcNode.Self != 1 {
		t.Errorf("service total/self: want 2/1, got %d/%d", svcNode.Total, svcNode.Self)
	}
	if epNode.Total != 1 || epNode.Self != 1 {
		t.Errorf("endpoint total/self: want 1/1, got %d/%d", epNode.Total, epNode.Self)
	}

	// 발견되지 않은 형제 서비스는 트리에 들어가지 않습니다.
	for _, n := range tree.Nodes {
		if n.Label == "http://"+sub+":8080" {
			t.Errorf("asset without findings should be hidden: %+v", n)
		}
	}

	// 연결되지 않은 버킷은 삭제된 자산을 가리키는 검색을 수락합니다.
	none := nodeByKey(tree, FindingUnassignedAsset)
	if none == nil || none.Total != 1 || none.Medium != 1 {
		t.Fatalf("unassigned bucket: want 1 medium, got %+v", none)
	}
}

// TestFindingAssetScopeFilter는 노드 선택을 확인합니다. narrows the findings list to
// that node's whole subtree, and that the unassigned sentinel works too.
func TestFindingAssetScopeFilter(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()

	tk, err := d.CreateTask("자산 심사 테스트", "목표", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)

	const root = "scope-test.example"
	const sub = "api.scope-test.example"
	const other = "other-scope-test.example"
	defer cleanupTreeFixtures(d, tk.ID, root, other)
	rootID := seedTreeAsset(t, d, "root_domain", map[string]any{"domain": root, "root_domain": root})
	subID := seedTreeAsset(t, d, "subdomain", map[string]any{"domain": sub, "root_domain": root})
	otherID := seedTreeAsset(t, d, "root_domain", map[string]any{"domain": other, "root_domain": other})

	if _, err := d.AddFinding(tk.ID, 0, "A", "하위 도메인에", "high", "s", "e", "w", []int64{subID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddFinding(tk.ID, 0, "B", "다른 루트 도메인 이름", "high", "s", "e", "w", []int64{otherID}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddFinding(tk.ID, 0, "C", "자산 없음", "high", "s", "e", "w", nil); err != nil {
		t.Fatal(err)
	}
	// 삭제된 자산을 가리키는 검색은 빈 asset_ids처럼 "연결되지 않음"입니다. 즉, 트리의 버킷이 이를 허용합니다.
	// 목록 필터도 확인해야 합니다. 두 구경의 불일치로 인해 버킷을 클릭한 후 버킷의 숫자가 항목 수보다 커집니다.
	if _, err := d.AddFinding(tk.ID, 0, "D", "자산이 삭제되었습니다.", "high", "s", "e", "w", []int64{999000333}); err != nil {
		t.Fatal(err)
	}

	base := FindingFilter{TaskID: strconv.FormatInt(tk.ID, 10)}
	cases := []struct {
		name  string
		scope string
		want  int
	}{
		{"전체 하위 트리", assetKey(rootID), 1},      // 루트 도메인 이름 아래에는 하위 도메인 이름만 있습니다.
		{"리프 노드", assetKey(subID), 1},          // 하위 도메인 자체
		{"또 다른 나무", assetKey(otherID), 1},      // 서로의 취향을 넘지 마세요
		{"연결되지 않음", FindingUnassignedAsset, 2}, // asset_ids는 비어 있으며 삭제된 자산을 가리킵니다.
		{"존재하지 않는 노드", "a:999000222", 0},       // 현재 필터링 중인 노드가 없습니다. → 필터링이 아닌 결과가 비어 있습니다.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			f.AssetScope = tc.scope
			items, total, err := d.ListFindingsPage(f, 1, 50)
			if err != nil {
				t.Fatal(err)
			}
			if total != tc.want || len(items) != tc.want {
				t.Fatalf("scope %s: want %d findings, got total=%d items=%d", tc.scope, tc.want, total, len(items))
			}
		})
	}

	// 4개 모두 scope 없이 존재합니다.
	if _, total, err := d.ListFindingsPage(base, 1, 50); err != nil || total != 4 {
		t.Fatalf("unscoped: want 4, got %d (%v)", total, err)
	}

	// 트리에 있는 연관되지 않은 버킷의 수는 클릭한 후 발견된 수와 일치해야 합니다. 이는 두 구경이 분리될 때 붕괴되는 주장입니다.
	tree, err := d.BuildFindingAssetTree(base)
	if err != nil {
		t.Fatal(err)
	}
	none := nodeByKey(tree, FindingUnassignedAsset)
	if none == nil || none.Total != 2 {
		t.Fatalf("unassigned bucket count: want 2, got %+v", none)
	}
}

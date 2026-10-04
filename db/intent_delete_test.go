package db

import "testing"

// mustIntent / mustNode / mustLink are terse builders for the delete-cascade tests.
func mustIntent(t *testing.T, es *ExplorationStore, summary string) int64 {
	t.Helper()
	id, err := es.AddIntent(map[string]any{"summary": summary}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustNode(t *testing.T, es *ExplorationStore, kind, summary string) int64 {
	t.Helper()
	id, err := es.AddNode(kind, map[string]any{"summary": summary}, 1, "confirmed", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustLink(t *testing.T, es *ExplorationStore, from int64, rel string, to int64) {
	t.Helper()
	if err := es.Link(from, rel, to); err != nil {
		t.Fatal(err)
	}
}

func gone(t *testing.T, es *ExplorationStore, id int64) bool {
	t.Helper()
	n, err := es.GetNode(id)
	if err != nil {
		t.Fatal(err)
	}
	return n == nil
}

// TestSoftDeleteIntent가 잘못 삭제되어 deleted + delete_reason로 설정되고 노드가 유지됩니다.
func TestSoftDeleteIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("soft delete", "가짜 삭제")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	intent := mustIntent(t, es, "삭제 예정")
	if err := es.SetIntentState(intent, "paused"); err != nil {
		t.Fatal(err)
	}
	summary, err := es.SoftDeleteIntent(intent, "잘못된 방향 판단")
	if err != nil {
		t.Fatal(err)
	}
	if summary != "삭제 예정" {
		t.Fatalf("summary=%q, want 삭제 예정", summary)
	}
	n, err := es.GetNode(intent)
	if err != nil || n == nil {
		t.Fatalf("intent removed by soft delete: n=%+v err=%v", n, err)
	}
	if n.State != StateIntentDeleted || n.DeleteReason != "잘못된 방향 판단" {
		t.Fatalf("state=%q delete_reason=%q, want deleted/ 방향 판단 오류", n.State, n.DeleteReason)
	}
	// 보류 중인(open) 인텐트도 잘못된 삭제를 허용합니다.
	openIntent := mustIntent(t, es, "받을 의사")
	if _, err := es.SoftDeleteIntent(openIntent, "방향은 필요없어"); err != nil {
		t.Fatalf("soft delete open intent: %v", err)
	}
	if n, err := es.GetNode(openIntent); err != nil || n == nil || n.State != StateIntentDeleted {
		t.Fatalf("open intent not soft-deleted: n=%+v err=%v", n, err)
	}

	// 삭제됨(deleted) 및 기타 상태는 허위로 삭제할 수 없습니다.
	if _, err := es.SoftDeleteIntent(intent, "다시 삭제"); err == nil {
		t.Fatal("soft-deleting an already-deleted intent unexpectedly succeeded")
	}
}

// TestHardDeleteCascadesExclusiveDescendants 실제 삭제는 리프에 대한 링크를 따라 단독 하위 항목을 계단식으로 삭제합니다.
func TestHardDeleteCascadesExclusiveDescendants(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("hard cascade", "계단식 삭제")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	// intent1 --yields--> fact1 --derived_from--> intent2 --yields--> fact2(리프)
	intent1 := mustIntent(t, es, "근본 의도")
	fact1 := mustNode(t, es, KindFact, "사실 1")
	mustLink(t, es, intent1, RelYields, fact1)
	intent2 := mustIntent(t, es, "파생된 의도")
	mustLink(t, es, fact1, RelDerivedFrom, intent2)
	fact2 := mustNode(t, es, KindFact, "사실 2")
	mustLink(t, es, intent2, RelYields, fact2)

	cleanup, err := es.CancelIntent(intent1)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.Intents != 2 || cleanup.Facts != 2 {
		t.Fatalf("cleanup=%+v, want 2 intents / 2 facts", cleanup)
	}
	for _, id := range []int64{intent1, fact1, intent2, fact2} {
		if !gone(t, es, id) {
			t.Fatalf("node %d survived cascade", id)
		}
	}
}

// TestHardDeletePreservesSharedAndGoal True 삭제는 공유 하위 항목(및 기타 상위 항목)과 대상을 유지합니다.
func TestHardDeletePreservesSharedAndGoal(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	defer d.Close()
	expID, err := d.CreateExploration("hard preserve", "공유/대상 유지")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, expID)
	es := d.Exploration(expID)

	goal, err := es.AddGoal(map[string]any{"text": "무대 뒤에서"}, "human")
	if err != nil {
		t.Fatal(err)
	}
	// intent1는 finding(proves goal) 전용이고, intent1는 intentX 및 fact1와 공유됩니다(fact1는 intent2에서 파생됨).
	intent1 := mustIntent(t, es, "삭제 예정")
	intentX := mustIntent(t, es, "우회 의도")
	finding := mustNode(t, es, KindFinding, "취약점")
	mustLink(t, es, intent1, RelYields, finding)
	mustLink(t, es, finding, RelProves, goal)
	shared := mustNode(t, es, KindFact, "사실을 공유하다")
	mustLink(t, es, intent1, RelYields, shared)
	mustLink(t, es, intentX, RelYields, shared)
	intent2 := mustIntent(t, es, "공유된 사실에서 파생됨")
	mustLink(t, es, shared, RelDerivedFrom, intent2)

	cleanup, err := es.CancelIntent(intent1)
	if err != nil {
		t.Fatal(err)
	}
	// intent1 및 독점 finding만 삭제하세요. shared(intentX의 상위 항목) 및 해당 다운스트림 intent2, goal는 모두 유지됩니다.
	if cleanup.Intents != 1 || cleanup.Findings != 1 || cleanup.Facts != 0 {
		t.Fatalf("cleanup=%+v, want 1 intent / 1 finding / 0 fact", cleanup)
	}
	if !gone(t, es, intent1) || !gone(t, es, finding) {
		t.Fatal("intent1/finding should be removed")
	}
	for _, id := range []int64{goal, intentX, shared, intent2} {
		if gone(t, es, id) {
			t.Fatalf("node %d was wrongly cascaded", id)
		}
	}
}

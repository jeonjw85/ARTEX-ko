package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// 이 문서의 사용 사례는 모두 PostgreSQL에 연결됩니다(라이브러리가 없으면 건너뛰기). 이 SQL는 사용됩니다
// FOR UPDATE SKIP LOCKED, make_interval, JSONB, 다중 라인 IN(...) 자리 표시자 접합,
// 모두 "컴파일은 통과했지만 실행 시 오류가 발생할 수 있다"는 방식으로 작성되었으며, 실행을 해야 검증된 것으로 간주됩니다.

func notifyTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(testDSN(t))
	if err != nil {
		t.Skipf("postgres unavailable (%v) — skipping", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// newTestChannel 채널을 생성하고 테스트 후 자동으로 삭제합니다.
func newTestChannel(t *testing.T, d *DB, kind, mode string, filter string) *NotificationChannel {
	t.Helper()
	if filter == "" {
		filter = `{}`
	}
	ch := &NotificationChannel{
		Name:       "테스트 채널-" + t.Name(),
		Kind:       kind,
		Mode:       mode,
		Config:     json.RawMessage(`{"webhook":"https://example.com/hook"}`),
		Filter:     json.RawMessage(filter),
		RatePerMin: 100,
	}
	id, err := d.SaveNotificationChannel(context.Background(), ch)
	if err != nil {
		t.Fatalf("채널 생성 실패: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	ch.ID = id
	return ch
}

// addTestEvent는 발송 및 배송 테스트를 위해 finding를 거치지 않고 직접 이벤트를 작성합니다.
func addTestEvent(t *testing.T, d *DB, kind string, findingID int64, snap notify.Snapshot) int64 {
	t.Helper()
	snap.Kind = kind
	snap.FindingID = findingID
	id, err := d.AddNotificationEvent(context.Background(), kind, findingID, snap)
	if err != nil {
		t.Fatalf("이벤트 쓰기 실패: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE id=$1`, id) })
	return id
}

func TestNotificationAssetNamesResolvesAndPreservesOrder(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// 세 가지 유형의 자산 각각에는 도메인 이름, IP 및 URL라는 자체 디스플레이 구경이 있습니다.
	insertAsset := func(query, value string) int64 {
		t.Helper()
		var id int64
		if err := d.QueryRow(query, value).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	domID := insertAsset(`INSERT INTO assets(type, domain) VALUES('subdomain',$1) RETURNING id`, "a.example.com")
	ipID := insertAsset(`INSERT INTO assets(type, ip) VALUES('ip',$1) RETURNING id`, "10.1.2.3")
	svcID := insertAsset(`INSERT INTO assets(type, url) VALUES('service',$1) RETURNING id`, "https://a.example.com/admin")
	t.Cleanup(func() {
		d.Exec(`DELETE FROM assets WHERE id IN ($1,$2,$3)`, domID, ipID, svcID)
	})

	// 들어오는 주문이 의도적으로 순서가 잘못되어 존재하지 않는 id가 포함되어 있습니다.
	got, err := d.NotificationAssetNames(ctx, []int64{svcID, 999999999, domID, ipID, svcID})
	if err != nil {
		t.Fatalf("자산 이름 구문 분석 실패: %v", err)
	}
	want := []string{"https://a.example.com/admin", "a.example.com", "10.1.2.3"}
	if len(got) != len(want) {
		t.Fatalf("자산 이름 수가 일치하지 않습니다. %v를 기대하고 %v를 얻으세요.", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("순서/값이 일치하지 않습니다. %v를 예상하고 %v를 가져옵니다.", want, got)
		}
	}
}

// TestRecordNotificationEventTxUnwindsOnFailure는 저장점 메커니즘의 핵심 사용 사례입니다.
// 트랜잭션에서 먼저 notification_events 쓰기가 필연적으로 실패하도록 합니다(일시적으로 상수 false 제약 조건 추가).
// Assertion ① 함수는 false를 보고합니다. ② 트랜잭션은 aborted 상태에 진입하지 않으며 후속 명령문은 계속 실행될 수 있습니다.
//
// 저장 지점이 없으면 PostgreSQL는 전체 트랜잭션을 무효화하고 모든 후속 명령문은 다음으로 끝납니다.
// "current transaction is aborted"가 실패했습니다. 이는 정확히 "알림 테이블에 문제가 발생하여 발생했습니다.
// 결함 경로는 취약점을 데이터베이스에 저장할 수 없다는 것입니다.
//
// 여기서는 의도적으로 COMMIT** 대신 **ROLLBACK로 끝납니다. ALTER TABLE는 PG에서 트랜잭션입니다.
// 제출되면 임시 제약조건은 schema에 영구적으로 유지되어 모든 후속 사용 사례가 일시 중지됩니다.
// 롤백은 수동 정리 없이 DDL를 자동으로 실행 취소할 수 있습니다. 어설션에는 "트랜잭션이 활성화되어 있음"만 필요합니다.
// 실제 제출은 필요하지 않습니다.
func TestRecordNotificationEventTxUnwindsOnFailure(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// 방어적 정리: 과거 작업에 이 제약 조건이 남아 있는 경우 먼저 제거합니다.
	if _, err := d.Exec(`ALTER TABLE notification_events DROP CONSTRAINT IF EXISTS notify_test_never`); err != nil {
		t.Fatal(err)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // 임시 제약 조건 취소, 함수 설명 참조

	// NOT VALID: 이 이후에 작성된 행만 제한되며 이미 라이브러리에 있는 기록 이벤트는 확인되지 않습니다.
	// (그렇지 않으면 기존 행 위반으로 인해 제약 조건이 추가되지 않습니다.)
	if _, err := tx.ExecContext(ctx, `ALTER TABLE notification_events ADD CONSTRAINT notify_test_never CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("임시 제약 조건을 추가하지 못했습니다: %v", err)
	}
	if RecordNotificationEventTx(ctx, tx, notify.EventFindingCreated, 1, notify.Snapshot{Severity: "high"}) {
		t.Fatal("실패해야 한다는 제약에도 불구하고 쓰기 성공을 보고합니다.")
	}
	// 주요 주장: 거래가 여전히 가능합니다.
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("트랜잭션이 오염되었습니다(저장점이 적용되지 않음): %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("롤백 실패: %v", err)
	}
	// DDL가 롤백과 함께 취소되어 후속 사용 사례에 대한 기회가 없는지 확인합니다.
	var exists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='notify_test_never')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("임시 제약 조건은 롤백으로 취소되지 않으므로 후속 사용 사례가 오염됩니다.")
	}
}

func TestFanOutRoutesEventsByFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	all := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	onlyCritical := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"min_severity":"critical"}`)
	sqlOnly := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["SQL"]}`)

	highSQL := addTestEvent(t, d, notify.EventFindingCreated, 1001, notify.Snapshot{Severity: "high", VulnClass: "SQL 주입"})
	lowXSS := addTestEvent(t, d, notify.EventFindingCreated, 1002, notify.Snapshot{Severity: "low", VulnClass: "XSS"})
	criticalXSS := addTestEvent(t, d, notify.EventFindingCreated, 1003, notify.Snapshot{Severity: "critical", VulnClass: "XSS"})

	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatalf("발송 실패: %v", err)
	}

	cases := []struct {
		name    string
		eventID int64
		channel int64
		want    bool
	}{
		{"모든 채널을 통해 high 수신", highSQL, all.ID, true},
		{"모든 채널을 통해 low 수신", lowXSS, all.ID, true},
		{"중요한 채널만 high를 건너뜁니다.", highSQL, onlyCritical.ID, false},
		{"심각한 채널만 critical를 수신합니다.", criticalXSS, onlyCritical.ID, true},
		{"SQL 채널만 SQL를 수신합니다.", highSQL, sqlOnly.ID, true},
		{"SQL 채널만 XSS를 건너뛰었습니다.", lowXSS, sqlOnly.ID, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exists bool
			if err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE event_id=$1 AND channel_id=$2)`,
				tc.eventID, tc.channel).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != tc.want {
				t.Fatalf("배송 존재: %v 예상 %v 받기", tc.want, exists)
			}
		})
	}

	// 다시 디스패치하면 중복 전달이 발생하지 않습니다(fanned_out 멱등성).
	events, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if events != 0 || deliveries != 0 {
		t.Fatalf("디스패치된 이벤트는 다시 처리되어서는 안 됩니다. events=%d deliveries=%d", events, deliveries)
	}
}

// TestFanOutMarksEventsWithNoMatchingChannel는 "이벤트가 어떤 채널에도 도달하지 않는" 상황을 다룹니다.
// 이러한 유형의 이벤트는 여전히 전달된 것으로 표시되어야 합니다. 그렇지 않으면 전달될 세트에 영원히 남아 있으며 tick마다 다시 검색됩니다.
func TestFanOutMarksEventsWithNoMatchingChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	pick := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{"vulnclass_include":["유형이 일치하지 않음"]}`)
	_ = pick

	ev := addTestEvent(t, d, notify.EventFindingCreated, 2001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	_, deliveries, err := d.FanOutPendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatalf("배송이 발생하지 않아야 합니다. %d를 받았습니다.", deliveries)
	}
	var fanned bool
	if err := d.QueryRowContext(ctx, `SELECT fanned_out FROM notification_events WHERE id=$1`, ev).Scan(&fanned); err != nil {
		t.Fatal(err)
	}
	if !fanned {
		t.Fatal("채널을 놓친 이벤트도 전달된 것으로 표시되어야 하며, 그렇지 않으면 무한히 다시 검색됩니다.")
	}
}

func TestClaimRealtimeDeliveriesHonorsLeaseAndMode(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	realtime := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	digest := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)

	addTestEvent(t, d, notify.EventFindingCreated, 3001, notify.Snapshot{Severity: "high", VulnClass: "XSS"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// 실시간 결제를 위해서는 realtime 채널에서만 받으셔야 하며, digest 채널은 건드리지 마세요.
	got, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatalf("수신 실패: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("아이템 1개를 받고 %d를 받아야 합니다.", len(got))
	}
	if got[0].State != NotifyStateSending || got[0].Attempts != 1 {
		t.Fatalf("그것을 받은 후에는 sending 및 attempts=1이어야 하며 state=%s attempts=%d를 받아야 합니다.", got[0].State, got[0].Attempts)
	}
	// 연관된 로드된 렌더링 컨텍스트가 완료되어야 합니다(채널 구성 + 이벤트 스냅샷 + finding id).
	if got[0].Channel == nil || len(got[0].Channel.Config) == 0 {
		t.Fatal("결과를 받을 때 채널 구성이 누락되어 렌더링이 실패합니다.")
	}
	if got[0].FindingID != 3001 {
		t.Fatalf("finding id가 이벤트에서 나오지 않아 %d를 받았습니다.", got[0].FindingID)
	}

	// 임대가 만료되지 않았으며 두 번째 컬렉션은 비어 있어야 합니다. 이는 "동일한 라인은 두 개의 dispatcher에 의해 동시에 전달되지 않습니다"입니다.
	// 보장하다.
	again, err := d.ClaimRealtimeDeliveries(ctx, realtime.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("임대 기간 동안 반복적으로 수집해서는 안 되며, %d를 받게 됩니다.", len(again))
	}

	// digest 채널의 배송은 실시간 픽업으로 발생하지 않아야 합니다.
	left, err := d.ClaimRealtimeDeliveries(ctx, digest.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("실시간 수집은 digest 채널을 통해 전달되어서는 안되지만, %d를 통해 전달되어야 합니다.", len(left))
	}
}

// TestClaimExpiredLeaseRecovers 충돌 자가 치유를 포함합니다. 배송 중에 프로세스가 중단되면 sending가 남게 됩니다.
// 알겠습니다. 임대 기간이 만료된 후 다시 수령해야 합니다. 그렇지 않으면 배송이 영원히 중단됩니다.
func TestClaimExpiredLeaseRecovers(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 4001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	first, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("처음 수집 실패: %v(%d)", err, len(first))
	}
	// 임대를 수동으로 과거로 푸시하여 "임대가 만료되었습니다"를 시뮬레이션합니다.
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE id=$1`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	second, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("임대가 만료된 sending 행을 회수하고 %d 행을 가져와야 합니다.", len(second))
	}
	if second[0].Attempts != 2 {
		t.Fatalf("%d를 획득하려면 회수 시도 횟수가 누적되어야 합니다.", second[0].Attempts)
	}
}

func TestClaimSkipsDisabledChannel(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 5001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// 비활성화하면 배송 대기 중인 재고가 skipped로 표시됩니다.
	if err := d.SetNotificationChannelEnabled(ctx, ch.ID, false); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateSkipped {
		t.Fatalf("비활성화된 채널의 인벤토리는 skipped로 표시되어야 하며 결과적으로 %s가 됩니다.", state)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("비활성화된 채널은 소유권을 주장해서는 안 되며 %d 항목을 받아야 합니다.", len(got))
	}
}

func TestDigestBatchDueAndStableBatchID(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 3; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(6000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}

	// 배치가 방금 생성되었고 기간은 0이며 30분 주기 이내에 만료되어서는 안 됩니다.
	due, err := d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatalf("배치 만료 확인 실패: %v", err)
	}
	if due {
		t.Fatal("새로 생성된 배치는 즉시 만료되어서는 안 됩니다.")
	}

	// 누적 기간이 충분한 배치를 시뮬레이션하려면 세 배송의 생성 시간을 함께 미십시오.
	if _, err := d.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '40 minutes' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	due, err = d.DigestBatchDue(ctx, ch.ID, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !due {
		t.Fatal("주기를 초과하는 배치는 만료된 것으로 판단되어야 합니다.")
	}

	batch, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatalf("요약 배치 수신 실패: %v", err)
	}
	if len(batch) != 3 {
		t.Fatalf("정리하자면, 아이템 3개를 모두 한꺼번에 모아야 하며, %d 아이템을 획득해야 합니다.", len(batch))
	}
	if batch[0].BatchID == nil {
		t.Fatal("요약 배치는 batch_id로 작성되어야 하며, 그렇지 않으면 해당 항목이 함께 발행된 기록에 표시되지 않습니다.")
	}
	firstBatchID := *batch[0].BatchID
	for _, dl := range batch {
		if dl.BatchID == nil || *dl.BatchID != firstBatchID {
			t.Fatalf("동일한 배치는 batch_id를 공유하고 %v vs %d를 가져와야 합니다.", dl.BatchID, firstBatchID)
		}
	}

	// 이 **전체** 배치가 실패하고 이를 받기 전에 재배열되도록 하세요. batch_id는 원래 값을 유지해야 합니다(COALESCE의 역할).
	// 그렇지 않으면 한 번 재시도하면 "이 일괄 처리가 함께 전송되었습니다"라는 사실이 지워집니다.
	//
	// 하나가 아닌 전체 배치를 다시 정렬해야 합니다. 이는 요약 메시지를 보낼 때 전달 엔진이 배치를 처리하는 방식입니다.
	// (하나의 메시지는 전체 배치를 나타내며 성공 또는 실패가 공유됩니다.) 한 품목만 일정을 변경하더라도 나머지 품목은 여전히 ​​임대 기간 내에 있습니다.
	// 당연히 칭호를 다시 얻으면 그 아이템 하나만 얻게 됩니다.
	allIDs := make([]int64, 0, len(batch))
	for _, dl := range batch {
		allIDs = append(allIDs, dl.ID)
	}
	if err := d.RescheduleDeliveries(ctx, allIDs, time.Second, "시뮬레이션 실패"); err != nil {
		t.Fatal(err)
	}
	// 임대를 과거로 푸시하고 백오프 시간이 만료되었는지 시뮬레이션합니다.
	if _, err := d.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := d.ClaimDigestBatch(ctx, ch.ID, MaxDigestBatchSize, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(reclaimed) != 3 {
		t.Fatalf("회수는 3개 항목을 모두 얻고 %d를 받아야 합니다.", len(reclaimed))
	}
	if reclaimed[0].BatchID == nil || *reclaimed[0].BatchID != firstBatchID {
		t.Fatalf("재시도 후 batch_id는 원래 값 %d를 유지하고 %v를 가져와야 합니다.", firstBatchID, reclaimed[0].BatchID)
	}
}

func TestDeliveryStateTransitions(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 7001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	got, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute)
	if err != nil || len(got) != 1 {
		t.Fatalf("수집 실패: %v(%d)", err, len(got))
	}
	id := got[0].ID

	if err := d.RescheduleDeliveries(ctx, []int64{id}, time.Second, "네트워크 지터"); err != nil {
		t.Fatal(err)
	}
	var state, lastErr string
	if err := d.QueryRow(`SELECT state, last_error FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &lastErr); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || lastErr != "네트워크 지터" {
		t.Fatalf("재배열 후에는 pending여야 하며 state=%s err=%q를 얻는 이유를 기록해야 합니다.", state, lastErr)
	}

	if err := d.FailDeliveries(ctx, []int64{id}, "소진된 재시도"); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRow(`SELECT state FROM notification_deliveries WHERE id=$1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStateFailed {
		t.Fatalf("failed를 예상했지만 %s를 얻었습니다.", state)
	}

	// 수동 재전송은 재시도 횟수를 지우고 즉시 만료되어야 합니다. 그렇지 않으면 이전 실패 예산이 상속됩니다.
	if err := d.RetryNotificationDelivery(ctx, id); err != nil {
		t.Fatalf("재전송 실패: %v", err)
	}
	var attempts int
	var next time.Time
	if err := d.QueryRow(`SELECT state, attempts, next_attempt_at FROM notification_deliveries WHERE id=$1`, id).Scan(&state, &attempts, &next); err != nil {
		t.Fatal(err)
	}
	if state != NotifyStatePending || attempts != 0 {
		t.Fatalf("다시 보낸 후에는 pending 및 attempts=0이어야 하며 state=%s attempts=%d를 받아야 합니다.", state, attempts)
	}
	if next.After(time.Now().Add(time.Second)) {
		t.Fatal("재전송이 즉시 가능해야 합니다.")
	}

	// 배송된 배송물은 다시 보내서는 안 됩니다.
	if err := d.MarkDeliveriesSent(ctx, []int64{id}); err != nil {
		t.Fatal(err)
	}
	if err := d.RetryNotificationDelivery(ctx, id); err == nil {
		t.Fatal("배송된 상품은 재전송이 허용되지 않습니다.")
	}
}

func TestListNotificationDeliveriesPagingAndFilter(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	for i := 0; i < 5; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(8000+i), notify.Snapshot{Severity: "high", Name: "페이지 매김 테스트"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ClaimRealtimeDeliveries(ctx, ch.ID, 10, time.Minute); err != nil {
		t.Fatal(err)
	}

	page1, total, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 1, 2)
	if err != nil {
		t.Fatalf("쿼리 실패: %v", err)
	}
	if total != 5 {
		t.Fatalf("총합은 5가 되어야 %d가 됩니다.", total)
	}
	if len(page1) != 2 {
		t.Fatalf("페이지당 항목 2개, %d 받기", len(page1))
	}
	// 새로운 첫 번째: 첫 번째 페이지의 첫 번째 항목인 id는 두 번째 페이지의 첫 번째 항목보다 커야 합니다.
	page2, _, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStateSending}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 2 || page2[0].ID >= page1[0].ID {
		t.Fatalf("페이징 순서는 먼저 새로운 것이어야 하며 page1[0]=%d page2[0]=%d를 얻습니다.", page1[0].ID, page2[0].ID)
	}
	// 렌더링 컨텍스트는 기록과 함께 반환되어야 합니다. 그렇지 않으면 목록에 "푸시된 내용"이 표시될 수 없습니다.
	if page1[0].ChannelName == "" || page1[0].FindingID == 0 {
		t.Fatalf("기록 항목에 표시 필드가 없습니다: %+v", page1[0])
	}

	// 상태별 필터링: pending의 경우 없음.
	pending, totalPending, err := d.ListNotificationDeliveries(ctx, NotificationDeliveryFilter{ChannelID: ch.ID, State: NotifyStatePending}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if totalPending != 0 || len(pending) != 0 {
		t.Fatalf("게시된 pending가 없어야 하는데 %d를 획득했습니다. (total=%d)", len(pending), totalPending)
	}
}

func TestSetFindingStatusWithNotifyOnlyEmitsOnRealChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("알림 상태 변경 테스트", "목표", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL 주입", Name: "상태 변경 사용 사례",
		Severity: "high", Summary: "요약",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// finding_created 이벤트가 라이브러리에 놓였을 때 등록되었습니다. 먼저 이를 기준으로 계산합니다.
	var base int
	if err := d.QueryRow(`SELECT count(*) FROM notification_events WHERE finding_id=$1`, f.FindingID).Scan(&base); err != nil {
		t.Fatal(err)
	}
	if base < 1 {
		t.Fatal("취약점 데이터베이스는 동일한 트랜잭션에 푸시 이벤트를 등록해야 합니다.")
	}

	// 동일한 상태로 변경: 이벤트가 생성되지 않아야 합니다(반복적인 제출 및 푸시 노이즈를 방지하기 위해).
	from, found, notified, err := d.SetFindingStatusWithNotify(ctx, f.FindingID, "pending")
	if err != nil || !found {
		t.Fatalf("상태 설정 실패: found=%v err=%v", found, err)
	}
	if notified {
		t.Fatal("상태가 변경되지 않은 경우 푸시 이벤트를 등록하면 안 됩니다.")
	}
	if from != "pending" {
		t.Fatalf("변경 전 상태 pending로 돌아가서 %q를 얻어야 합니다.", from)
	}

	// 실제 변경: 이벤트가 from/to에 등록되고 기록되어야 합니다.
	from, found, notified, err = d.SetFindingStatusWithNotify(ctx, f.FindingID, "fixed")
	if err != nil || !found {
		t.Fatalf("상태 설정 실패: found=%v err=%v", found, err)
	}
	if !notified {
		t.Fatal("푸시 이벤트는 상태가 실제로 변경될 때 등록되어야 합니다.")
	}
	if from != "pending" {
		t.Fatalf("from는 pending여야 하며, %q를 얻으세요.", from)
	}
	var snapshot []byte
	if err := d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot); err != nil {
		t.Fatalf("상태 변경 이벤트를 찾을 수 없습니다: %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != "fixed" {
		t.Fatalf("스냅샷의 상태 흐름이 올바르지 않습니다: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// 스냅샷은 렌더링에 필요한 필드를 가져와야 합니다. 그렇지 않으면 상태 변경 메시지가 빈 셸이 됩니다.
	if snap.VulnClass != "SQL 주입" || snap.Severity != "high" || snap.Name != "상태 변경 사용 사례" {
		t.Fatalf("스냅샷에 렌더링 필드가 없습니다: %+v", snap)
	}
	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "fixed" {
		t.Fatalf("상태가 fixed로 업데이트되어야 합니다. %s를 얻으세요.", status)
	}

	// 존재하지 않는 취약점: found=false, 오류가 보고되지 않습니다.
	if _, found, _, err := d.SetFindingStatusWithNotify(ctx, 999999999, "fixed"); err != nil || found {
		t.Fatalf("존재하지 않는 취약점은 found=false를 반환하고 오류 없이 found=%v err=%v를 반환해야 합니다.", found, err)
	}
}

func TestNotificationStatsSnapshot(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	addTestEvent(t, d, notify.EventFindingCreated, 9001, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	stats, err := d.NotificationStatsSnapshot(ctx)
	if err != nil {
		t.Fatalf("통계 실패: %v", err)
	}
	if stats.Channels < 1 || stats.ChannelsOn < 1 {
		t.Fatalf("잘못된 채널 수: %+v", stats)
	}
	if stats.Pending < 1 {
		t.Fatalf("계산되어 배송 준비가 완료되어야 함: %+v", stats)
	}
	// 새로 생성된 배달 백로그 기간은 음수이거나 거대하지 않고 0에 가까워야 합니다.
	if stats.BacklogAgeMS < 0 || stats.BacklogAgeMS > int64(time.Hour/time.Millisecond) {
		t.Fatalf("백로그 기간이 잘못되었습니다: %d ms", stats.BacklogAgeMS)
	}
	_ = ch
}

func TestNotificationChannelCRUDRoundTrip(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	ch := &NotificationChannel{
		Name:       "CRUD 왕복",
		Kind:       notify.KindEmail,
		Mode:       NotifyModeDigest,
		Config:     json.RawMessage(`{"host":"smtp.example.com","port":587,"from":"a@b.c","to":["x@y.z"]}`),
		Filter:     json.RawMessage(`{"min_severity":"medium","on_status_change":true}`),
		RatePerMin: 42,
	}
	id, err := d.SaveNotificationChannel(ctx, ch)
	if err != nil {
		t.Fatalf("새로 생성 실패: %v", err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })

	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatalf("읽기 실패: %v", err)
	}
	if got.Mode != NotifyModeDigest || got.RatePerMin != 42 || got.Name != "CRUD 왕복" {
		t.Fatalf("왕복 필드 불일치: %+v", got)
	}
	if !got.IsEnabled() {
		t.Fatal("기본값은 활성화되어야 합니다.")
	}
	var cfg map[string]any
	if err := json.Unmarshal(got.Config, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["host"] != "smtp.example.com" {
		t.Fatalf("구성이 라이브러리에 올바르게 삭제되지 않았습니다: %v", cfg)
	}
	var filter notify.Filter
	if err := json.Unmarshal(got.Filter, &filter); err != nil {
		t.Fatal(err)
	}
	if filter.MinSeverity != "medium" || !filter.OnStatusChange {
		t.Fatalf("필터 조건이 라이브러리에 올바르게 입력되지 않았습니다: %+v", filter)
	}

	// 업데이트 후 다시 읽어보세요.
	got.Name = "이름이 변경됨"
	off := false
	got.Enabled = &off
	if _, err := d.SaveNotificationChannel(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "이름이 변경됨" || after.IsEnabled() {
		t.Fatalf("업데이트가 적용되지 않음: %+v", after)
	}

	// 삭제 후에는 자동 성공 대신 "존재하지 않음"이 보고되어야 합니다.
	if err := d.DeleteNotificationChannel(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.NotificationChannelByID(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("ErrNotificationChannelNotFound를 예상했지만 %v를 얻었습니다.", err)
	}
	if err := d.DeleteNotificationChannel(ctx, id); err != ErrNotificationChannelNotFound {
		t.Fatalf("중복삭제신고가 존재하지 않으며, %v를 취득하였습니다.", err)
	}
}

// TestSaveNotificationChannelKeepsExplicitZeroRate는 잘못 작성된 장소를 잠급니다.
// **0은 유효한 구성으로, "현재 제한 없음"을 의미하며 db 레이어에서 "지정되지 않음"으로 기본값으로 재정의될 수 없습니다**.
//
// 내역 bug: `if RatePerMin <= 0 { 기본값을 사용하세요. }`는 SaveNotificationChannel로 작성되었으며,
// 따라서 문서, UI 프롬프트, takeTokens는 모두 "0 = 현재 제한 없음"으로 해석되지만 쓰기 라이브러리 레이어는 조용히 다음으로 변경되었습니다.
// 20(DingTalk/Qiwei/Telegram) 또는 100(Feishu) - 운영자는 현재 제한이 해제된 것으로 생각했지만 실제로는 중단되었습니다.
// 그리고 힌트도 없습니다. "지정되지 않음"과 "명시적 0"의 차이는 요청 본문으로만 표현됩니다.
// 따라서 server 레이어(notifyCreateChannel 참조)에는 기본값이 채워지고, db 레이어는 스토리지만 관리하게 된다.
func TestSaveNotificationChannelKeepsExplicitZeroRate(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	// 명시적 0(현재 제한 없음): 있는 그대로 저장해야 합니다.
	unlimited := &NotificationChannel{
		Name: "현재 제한 없음", Kind: notify.KindDingTalk, RatePerMin: 0,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	id, err := d.SaveNotificationChannel(ctx, unlimited)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_channels WHERE id=$1`, id) })
	got, err := d.NotificationChannelByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RatePerMin != 0 {
		t.Fatalf("명시적 0은 전류 제한이 없음을 의미하며 그대로 저장해야 하므로 %d가 발생합니다.", got.RatePerMin)
	}
	if got.Mode != NotifyModeRealtime {
		t.Fatalf("기본 모드는 realtime여야 하며, %s를 가져옵니다.", got.Mode)
	}

	// 음수 값은 잘못된 입력이므로 조용히 다른 값으로 변경하기보다는 거부해야 합니다.
	bad := &NotificationChannel{
		Name: "음의 전류 제한", Kind: notify.KindDingTalk, RatePerMin: -1,
		Config: json.RawMessage(`{"webhook":"https://example.com/h"}`),
	}
	if _, err := d.SaveNotificationChannel(ctx, bad); err == nil {
		t.Fatal("음의 전류 제한은 거부되어야 합니다.")
	}
}

// TestDeleteChannelCascadesDeliveries 외래 키 잠금 동작: 채널이 삭제되면 전송 기록이 사라집니다.
// (구성이 없어져 기록을 해석할 수 없습니다.) 하지만 이벤트 자체는 남아 있어야 합니다. 다른 채널에서 인용될 수 있습니다.
func TestDeleteChannelCascadesDeliveries(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeRealtime, `{}`)
	ev := addTestEvent(t, d, notify.EventFindingCreated, 9101, notify.Snapshot{Severity: "high"})
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before == 0 {
		t.Fatal("전제조건이 충족되지 않음: 배송이 발생하지 않음")
	}
	if err := d.DeleteNotificationChannel(ctx, ch.ID); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := d.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, ch.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("채널이 삭제된 후 해당 전달은 캐스케이드에서 삭제되어야 하며 여전히 %d가 있습니다.", after)
	}
	var evExists bool
	if err := d.QueryRow(`SELECT EXISTS(SELECT 1 FROM notification_events WHERE id=$1)`, ev).Scan(&evExists); err != nil {
		t.Fatal(err)
	}
	if !evExists {
		t.Fatal("채널을 삭제해도 이벤트 자체도 삭제되어서는 안 됩니다.")
	}
}

// TestClaimDigestBatchHonorsCallerLimit 커버리지 감사에서 지적된 결함:
// 집계 채널은 이전에 토큰 버킷을 완전히 우회했습니다. allow는 takeTokens에 의해 공제되었지만 아무도 이를 사용하지 않았습니다.
// rate_per_min는 digest 모드에 영향을 주지 않습니다. 이제 limit도 제약 조건에 참여합니다.
func TestClaimDigestBatchHonorsCallerLimit(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()
	ch := newTestChannel(t, d, notify.KindDingTalk, NotifyModeDigest, `{}`)
	for i := 0; i < 10; i++ {
		addTestEvent(t, d, notify.EventFindingCreated, int64(7000+i), notify.Snapshot{Severity: "high"})
	}
	if _, _, err := d.FanOutPendingEvents(ctx, 100); err != nil {
		t.Fatal(err)
	}
	// limit=3을 사용하면 3개만 얻을 수 있고 나머지는 라이브러리에 보관할 수 있습니다.
	got, err := d.ClaimDigestBatch(ctx, ch.ID, 3, time.Minute)
	if err != nil {
		t.Fatalf("수신 실패: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("발신자의 현재 한도에 따라 아이템 3개만 받아야 하며, %d를 받아야 합니다.", len(got))
	}
	// limit=0은 이 라운드의 할당량이 소진되었음을 의미합니다. 누구도 요청해서는 안 되며 오류도 보고되어서는 안 됩니다.
	if got, err := d.ClaimDigestBatch(ctx, ch.ID, 0, time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("할당량이 0이면 0개 항목을 수집해야 하며 오류가 보고되지 않고 %d 항목을 얻습니다. err=%v", len(got), err)
	}
}

// TestFinishFindingRetestEmitsStatusChange 감사를 통해 확인된 무결성 격차 범위:
// 재테스트 결론이 "수정"되면 실제로 상태가 변경되었지만 UPDATE가 라이브러리에 직접 기록되었습니다.
// 알림이 포함된 버전 우회 - 따라서 on_status_change 채널은 이 상태를 순환하도록 구성됩니다.
// 푸시 알림도 전혀 받을 수 없었고, 인터페이스 상태도 조용히 바뀌었습니다. 운영과 유지보수는 플랫폼을 열어서 알아내야 했습니다.
//
// 이 사용 사례는 "상태를 변경하는 모든 경로는 상태 변경 이벤트를 등록해야 함"을 잠급니다.
func TestFinishFindingRetestEmitsStatusChange(t *testing.T) {
	d := notifyTestDB(t)
	ctx := context.Background()

	tk, err := d.CreateTask("푸시 테스트 다시 테스트", "목표", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(tk.ID)
	es := d.Exploration(tk.ExplorationID)
	f, err := es.RecordFinding(ctx, RecordFindingInput{
		TaskID: tk.ID, Worker: "test", VulnClass: "SQL 주입", Name: "재테스트 목표",
		Severity: "high", Summary: "요약",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(`DELETE FROM notification_events WHERE finding_id=$1`, f.FindingID) })

	// 재테스트 기록을 생성하고 완료 상태로 직접 푸시합니다.
	rt, _, _, err := d.CreateFindingRetest(ctx, f.FindingID, "검토")
	if err != nil {
		t.Fatal(err)
	}
	if rt.ConversationID == nil {
		t.Fatal("재테스트는 세션과 연결되어야 합니다.")
	}
	// 재테스트에서는 먼저 running를 입력하여 결론을 도출해야 합니다(실제 프로세스와 일치).
	if ok, err := d.StartFindingRetest(ctx, rt.ID); err != nil || !ok {
		t.Fatalf("재테스트 시작 실패: ok=%v err=%v", ok, err)
	}
	if err := d.RecordFindingRetestResult(ctx, *rt.ConversationID, "fixed", "수정됨", "증거"); err != nil {
		t.Fatal(err)
	}
	if err := d.FinishFindingRetest(rt.ID, "completed", ""); err != nil {
		t.Fatalf("재테스트 종료 실패: %v", err)
	}

	var status string
	if err := d.QueryRow(`SELECT status FROM findings WHERE id=$1`, f.FindingID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != FindingFixed {
		t.Fatalf("재테스트 결과 수리된 상태는 fixed여야 하며 %s를 획득한 것으로 확인되었습니다.", status)
	}

	// 핵심 주장: 상태 변경 이벤트가 있어야 하며 from/to가 정확합니다.
	var snapshot []byte
	err = d.QueryRow(`SELECT snapshot FROM notification_events WHERE finding_id=$1 AND kind=$2 ORDER BY id DESC LIMIT 1`,
		f.FindingID, notify.EventFindingStatusChanged).Scan(&snapshot)
	if err != nil {
		t.Fatalf("재테스트 결과 상태 변경 푸시 이벤트를 등록해야 하는 것으로 확인되었습니다. (그렇지 않으면 on_status_change가 장착된 채널은 이를 수신하지 않습니다.): %v", err)
	}
	var snap notify.Snapshot
	if err := json.Unmarshal(snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.FromStatus != "pending" || snap.ToStatus != FindingFixed {
		t.Fatalf("스냅샷의 상태 흐름이 올바르지 않습니다: %s → %s", snap.FromStatus, snap.ToStatus)
	}
	// 스냅샷에는 렌더링에 필요한 필드가 포함되어야 합니다. 그렇지 않으면 푸시될 때 빈 셸이 됩니다.
	if snap.Name != "재테스트 목표" || snap.Severity != "high" {
		t.Fatalf("스냅샷에 렌더링 필드가 없습니다: %+v", snap)
	}
}

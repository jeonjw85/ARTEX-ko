package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// 이 문서는 푸시 기능의 엔드투엔드 동작(취약성 로깅 → 이벤트 → 발송 → 실제 문제 HTTP)을 다룹니다.
//
// 보안 참고 사항: 이러한 사용 사례**글로벌로 전화하지 마세요 Notifier.step()**，직접 만든 사람에게만 해당
// 채널은 stepRealtime/stepDigest를 호출합니다. 그 이유는 step()가 라이브러리에서 활성화된 모든 채널을 통과하기 때문입니다.
// 실제 DingTalk/Qiwei 로봇이 장착된 개발 라이브러리에서 테스트를 실행합니다. 글로벌 step는
// 결과적인 허점은 실제로 해당 그룹으로 밀려납니다. 채널별 호출은 테스트로 생성된 가짜 수신 측에 미치는 영향을 엄격하게 제한합니다.
//
// 정리: 유스케이스가 종료되면 실제 채널에 대한 백로그가 남지 않도록 해당 유스케이스에서 생성된 이벤트(연속 삭제 및 전달)와 채널을 삭제합니다.
//
// Assertion 구경: stepRealtime/stepDigest는 값을 반환하지 않고 내부 로그를 기록하므로 여기서 주장하는 내용은 다음과 같습니다.
// **관찰 가능한 외부 동작**(가짜 수신자가 수신하는 내용, 전달 라인이 어떤 상태에 속하는지)
// 반환 값 - 반환 값을 스텁하는 것보다 실제 호출 경로에 더 가깝습니다.

// notifyFixture는 이 문서의 사용 사례에 대한 공용 장치입니다.
type notifyFixture struct {
	s       *Server
	pg      *db.DB
	request func(method, path, body string) *httptest.ResponseRecorder
	n       *Notifier
	// 자체 구축된 task/exploration: 사용 사례는 여기에 취약점을 기록하고 이를 다른 사용 사례의 데이터와 격리합니다.
	taskID int64
	expID  int64
	// cleanupMark 이후에 생성된 이벤트는 정리 중에 삭제됩니다.
	cleanupMark int64
}

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	// 이 파일의 모든 가짜 수신기는 127.0.0.1에서 실행되며 배달은 기본적으로 루프백 주소를 거부합니다.
	// (SSRF가 동일한 기계 서비스 및 클라우드 메타데이터에 도달하는 것을 방지하기 위해) 이 스위치를 명시적으로 켜서 테스트해 보세요.
	// 가드의 "기본 거부" 동작은 notify 패키지의 ssrf_test.go에 의해 재정의됩니다.
	t.Setenv(notify.AllowLocalTargetsEnv, "1")
	s, _, request := trafficEvidenceServer(t)
	pg := s.m.pg

	// 자체 제작 task 빌드: 공유 장치 trafficEvidenceServer에서 생성된 task는 exploration id를 가져올 수 없습니다.
	// 그리고 취약점을 문서화하여 이를 제공해야 합니다.
	task, err := s.m.CreateTask("알림 푸시 테스트", "푸시 동작 검증", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Exec(`DELETE FROM tasks WHERE id=$1`, taskID) })

	var mark int64
	if err := pg.QueryRow(`SELECT COALESCE(max(id),0) FROM notification_events`).Scan(&mark); err != nil {
		t.Fatal(err)
	}
	// 장치가 폐쇄 루프를 형성하도록 합니다. fixture 이전에 존재했던 이벤트를 즉시 전달된 것으로 표시합니다.
	//
	// 수행해야 하는 이유: FanOutPendingEvents는 **전역**이며 라이브러리에서 전달되지 않은 모든 이벤트를 수행합니다.
	// 일치하는 모든 채널로 확장합니다. 공유 장치 trafficEvidenceServer는 자체적으로 취약점을 기록합니다.
	// (정확히 초기 finding가 반환됨) 다른 사용 사례에도 남은 부분이 있을 수 있습니다. 격리가 되지 않는다면,
	// 이러한 가짜 이벤트는 이 사용 사례의 채널로 전달되어 "N 전달이 있어야 합니다"와 같은 주장을 허용합니다.
	// 때로는 옳을 때도 있고, 때로는 틀리기도 하며, 잘못된 방법은 사용 사례 실행 순서에 따라 달라지며 이는 직접적인 실패보다 감지하기가 더 어렵습니다.
	if _, err := pg.Exec(`UPDATE notification_events SET fanned_out = true WHERE id <= $1 AND NOT fanned_out`, mark); err != nil {
		t.Fatal(err)
	}

	f := &notifyFixture{s: s, pg: pg, request: request, n: newNotifier(s), taskID: taskID, expID: task.ExpID, cleanupMark: mark}
	t.Cleanup(func() {
		if _, err := pg.Exec(`DELETE FROM notification_events WHERE id > $1`, f.cleanupMark); err != nil {
			t.Logf("정리 알림 이벤트 실패: %v", err)
		}
	})
	// 마스터 스위치는 켜져 있어야 합니다(다른 사용 사례에서는 꺼질 수 있음).
	if err := pg.SetBool(settingNotifyEnabled, true); err != nil {
		t.Fatal(err)
	}
	return f
}

// record는 실제 증거 작성 경로를 사용하고 취약점에 직면하여 finding id를 반환합니다.
// 이 경로는 동일한 트랜잭션에 푸시 이벤트를 등록합니다. 이는 이 함수의 정지 지점입니다.
func (f *notifyFixture) record(t *testing.T, vulnclass, severity string) int64 {
	t.Helper()
	out, err := f.s.evidenceStore().Record(context.Background(), db.RecordFindingInput{
		TaskID:        f.taskID,
		ExplorationID: f.expID,
		Worker:        "test",
		VulnClass:     vulnclass,
		Name:          vulnclass,
		Severity:      severity,
		Summary:       vulnclass + " 요약",
		Evidence:      "poc",
	}, nil)
	if err != nil {
		t.Fatalf("취약점 기록 실패: %v", err)
	}
	return out.FindingID
}

// channel는 채널 구성을 다시 읽습니다(채널별로 stepX를 호출하여 사용).
func (f *notifyFixture) channel(t *testing.T, id int64) *db.NotificationChannel {
	t.Helper()
	ch, err := f.pg.NotificationChannelByID(context.Background(), id)
	if err != nil {
		t.Fatalf("채널 읽기 실패: %v", err)
	}
	return ch
}

// deliver는 이벤트를 전달하고 지정된 채널에 한 번의 전달만 실행합니다.
func (f *notifyFixture) deliver(t *testing.T, chID int64, baseURL string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatalf("발송 실패: %v", err)
	}
	f.n.stepRealtime(ctx, f.channel(t, chID), 50, baseURL)
}

// createChannel는 HTTP 인터페이스를 통해 채널을 구축하며, 인터페이스 자체의 검증 경로도 포함합니다.
func (f *notifyFixture) createChannel(t *testing.T, payload map[string]any) int64 {
	t.Helper()
	raw, _ := json.Marshal(payload)
	r := f.request("POST", "/api/notify/channels", string(raw))
	if r.Code != 200 {
		t.Fatalf("%d 채널을 생성하지 못했습니다: %s", r.Code, r.Body)
	}
	var res struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &res); err != nil || res.ID == 0 {
		t.Fatalf("채널 생성 반환 예외: %s(%v)", r.Body, err)
	}
	t.Cleanup(func() { f.pg.Exec(`DELETE FROM notification_channels WHERE id=$1`, res.ID) })
	return res.ID
}

// fakeWebhook는 수신된 요청 본문을 기록하는 가짜 수신기입니다.
type fakeWebhook struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newFakeWebhook(t *testing.T) *fakeWebhook {
	t.Helper()
	f := &fakeWebhook{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWebhook) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func (f *fakeWebhook) body(t *testing.T, i int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.bodies) {
		t.Fatalf("가짜 수신측에서는 %d 요청만 받았고 %d 요청은 받지 못했습니다.", len(f.bodies), i)
	}
	return f.bodies[i]
}

func (f *fakeWebhook) last(t *testing.T) map[string]any {
	t.Helper()
	if f.count() == 0 {
		t.Fatal("가짜 수신자는 요청을 받지 못했습니다.")
	}
	return f.body(t, f.count()-1)
}

// markdownText는 요청 본문에서 텍스트를 가져오고 각 회사의 필드 이름 차이와 호환됩니다.
// DingTalk markdown에는 `text`를, ActionCard에는 `text`를, Enterprise WeChat markdown에는 `content`를 사용하세요.
func markdownText(t *testing.T, body map[string]any) string {
	t.Helper()
	for _, key := range []string{"markdown", "actionCard"} {
		section, ok := body[key].(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"text", "content"} {
			if s, ok := section[field].(string); ok && s != "" {
				return s
			}
		}
	}
	t.Fatalf("요청 본문에 식별 가능한 텍스트가 없습니다: %v", body)
	return ""
}

// agePendingBatch는 이 채널의 보류 중인 전달을 만료하고 요약 배치의 만료를 테스트하는 데 사용됩니다.
func (f *notifyFixture) agePendingBatch(t *testing.T, chID int64) {
	t.Helper()
	if _, err := f.pg.Exec(`UPDATE notification_deliveries SET created_at = now() - interval '2 hours'
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyEndToEndRealtimeDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "실시간 푸시",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "SQL 주입", "high")
	f.deliver(t, chID, "")

	if hook.count() != 1 {
		t.Fatalf("1개의 메시지가 전송되어야 합니다. 실제 %d", hook.count())
	}
	text := markdownText(t, hook.last(t))
	for _, want := range []string{"SQL 주입", "높은 위험", "요약"} {
		if !strings.Contains(text, want) {
			t.Fatalf("메시지 본문에 %q:\n%s가 없습니다.", want, text)
		}
	}
	// 배송은 sent로 진행되어야 합니다.
	var pending int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state <> $2`,
		chID, db.NotifyStateSent).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("배송 후에도 여전히 %d 표시가 없는 sent가 있습니다.", pending)
	}
}

func TestNotifyChannelAPIMasksSecretsAndPreservesOnUpdate(t *testing.T) {
	f := newNotifyFixture(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "마스크 사용 사례",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "https://oapi.dingtalk.com/robot/send?access_token=abc123456", "secret": "SECabcdef123456"},
	})

	r := f.request("GET", "/api/notify/channels", "")
	if r.Code != 200 {
		t.Fatalf("열 채널 실패 %d: %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "abc123456") || strings.Contains(r.Body.String(), "SECabcdef123456") {
		t.Fatalf("인터페이스는 자격 증명을 반영합니다: %s", r.Body)
	}
	var listed struct {
		Channels []struct {
			ID         int64          `json:"id"`
			Config     map[string]any `json:"config"`
			SecretKeys []string       `json:"secret_keys"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var mine *struct {
		ID         int64          `json:"id"`
		Config     map[string]any `json:"config"`
		SecretKeys []string       `json:"secret_keys"`
	}
	for i := range listed.Channels {
		if listed.Channels[i].ID == chID {
			mine = &listed.Channels[i]
		}
	}
	if mine == nil {
		t.Fatal("새로 생성된 채널이 목록에 나타나지 않습니다.")
	}
	if !notify.IsMasked(fmt.Sprint(mine.Config["webhook"])) || !notify.IsMasked(fmt.Sprint(mine.Config["secret"])) {
		t.Fatalf("자격 증명 필드는 %v 값으로 마스킹되어야 합니다.", mine.Config)
	}
	if len(mine.SecretKeys) == 0 {
		t.Fatal("인터페이스는 어떤 필드가 자격 증명인지 프런트엔드에 알려야 합니다.")
	}

	// PATCH 이름 변경 + 마스킹된 자격 증명 반환만 가능합니다. 실제 자격 증명은 그대로 유지되어야 합니다.
	body, _ := json.Marshal(map[string]any{
		"name":   "이름 변경 후",
		"config": map[string]any{"webhook": fmt.Sprint(mine.Config["webhook"]), "secret": fmt.Sprint(mine.Config["secret"])},
	})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("업데이트 실패 %d: %s", r.Code, r.Body)
	}
	cfg := f.channelConfig(t, chID)
	if cfg["webhook"] != "https://oapi.dingtalk.com/robot/send?access_token=abc123456" {
		t.Fatalf("마스크된 포스트백은 실제 자격 증명을 포함합니다: %v", cfg["webhook"])
	}
	if cfg["secret"] != "SECabcdef123456" {
		t.Fatalf("마스킹된 패스백은 secret: %v를 포함합니다.", cfg["secret"])
	}
	if f.channel(t, chID).Name != "이름 변경 후" {
		t.Fatal("이름이 업데이트되지 않았습니다.")
	}

	// secret의 명시적 삭제가 적용됩니다("return_mask=remain Changing"과 구별됨).
	body, _ = json.Marshal(map[string]any{"config": map[string]any{"secret": ""}})
	if r := f.request("PATCH", fmt.Sprintf("/api/notify/channels/%d", chID), string(body)); r.Code != 200 {
		t.Fatalf("secret 지우기 실패 %d: %s", r.Code, r.Body)
	}
	if _, still := f.channelConfig(t, chID)["secret"]; still {
		t.Fatal("빈 문자열은 지워야 합니다. secret")
	}
}

func (f *notifyFixture) channelConfig(t *testing.T, id int64) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(f.channel(t, id).Config, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNotifyChannelAPICreateValidation(t *testing.T) {
	f := newNotifyFixture(t)
	cases := []struct {
		name    string
		payload map[string]any
		wantSub string
	}{
		{"잘못된 유형", map[string]any{"name": "x", "kind": "nope", "config": map[string]any{}}, "잘못된 채널 유형"},
		{"이름이 누락되었습니다.", map[string]any{"kind": notify.KindDingTalk, "config": map[string]any{"webhook": "https://e.com/h"}}, "채널 이름이 누락되었습니다."},
		{"webhook 누락", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{}}, "Webhook"},
		{"잘못된 webhook 프로토콜", map[string]any{"name": "x", "kind": notify.KindDingTalk, "config": map[string]any{"webhook": "file:///etc/passwd"}}, "Webhook 잘못된 주소"},
		{"잘못된 모드", map[string]any{"name": "x", "kind": notify.KindDingTalk, "mode": "sometimes", "config": map[string]any{"webhook": "https://e.com/h"}}, "푸시 모드가 유효하지 않습니다"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.payload)
			r := f.request("POST", "/api/notify/channels", string(raw))
			if r.Code != 400 {
				t.Fatalf("400을 반환하고 %d를 얻어야 합니다: %s", r.Code, r.Body)
			}
			if !strings.Contains(r.Body.String(), tc.wantSub) {
				t.Fatalf("오류 메시지에는 %q가 언급되어야 하며, %s를 얻으세요.", tc.wantSub, r.Body)
			}
		})
	}
	if r := f.request("DELETE", "/api/notify/channels/99999999", ""); r.Code != 404 {
		t.Fatalf("존재하지 않는 채널을 삭제하면 404가 표시되고 %d가 표시됩니다.", r.Code)
	}
}

func TestNotifyFilterBlocksBelowThreshold(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "단지 심한",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"min_severity": "critical"},
	})
	f.record(t, "위험도가 낮은 문제", "low")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("임계값 미만의 취약점은 전달되지 않아야 하며 %d를 받아야 합니다.", n)
	}
	f.n.stepRealtime(context.Background(), f.channel(t, chID), 50, "")
	if hook.count() != 0 {
		t.Fatal("필터링된 취약점은 메시지를 내보내서는 안 됩니다.")
	}
}

func TestNotifyDigestBatchesMultipleFindingsIntoOneMessage(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "요약 푸시",
		"kind":   notify.KindDingTalk,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	for i := 0; i < 3; i++ {
		f.record(t, fmt.Sprintf("취약점 요약 %d", i+1), "high")
	}
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)

	// 만료되지 않음: 발행되지 않았습니다.
	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count() != 0 {
		t.Fatal("요약 배치가 만료되기 전에 전송되었습니다.")
	}

	// 에이징 일괄 처리 후: 세 개의 메시지가 하나의 메시지로 결합됩니다.
	f.agePendingBatch(t, chID)
	f.n.stepDigest(ctx, ch, 50, "")
	if got := hook.count(); got != 1 {
		t.Fatalf("세 개의 메시지를 하나의 메시지로 요약해야 하는데 실제로는 %d 메시지가 전송되었습니다.", got)
	}
	text := markdownText(t, hook.last(t))
	if !strings.Contains(text, "최근") || !strings.Contains(text, "취약점 3개") {
		t.Fatalf("누락된 요약 메시지 수/시간 창 복사본: \n%s", text)
	}
	for i := 1; i <= 3; i++ {
		if !strings.Contains(text, fmt.Sprintf("취약점 요약 %d", i)) {
			t.Fatalf("요약 메시지에 항목 %d가 없습니다: \n%s", i, text)
		}
	}
	// batch_id는 동일한 배치에서 공유되어야 합니다.
	var distinct, total int
	if err := f.pg.QueryRow(`SELECT count(DISTINCT batch_id), count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&distinct, &total); err != nil {
		t.Fatal(err)
	}
	if total != 3 || distinct != 1 {
		t.Fatalf("3개의 배송은 하나의 batch_id를 공유해야 하며 distinct=%d total=%d를 얻어야 합니다.", distinct, total)
	}
}

func TestNotifyDisabledChannelDoesNotSend(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":    "채널 비활성화",
		"kind":    notify.KindDingTalk,
		"enabled": false,
		"config":  map[string]any{"webhook": hook.URL},
	})
	f.record(t, "다운타임 중 취약점", "critical")
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("비활성화된 채널은 배달을 생성하거나 %d 항목을 가져오면 안 됩니다.", n)
	}
}

func TestNotifyStatusChangeDelivery(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "상태 변경 구독",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
		"filter": map[string]any{"on_status_change": true},
	})
	finding := f.record(t, "상태 변경 사용 사례", "high")
	r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"fixed"}`)
	if r.Code != 200 {
		t.Fatalf("%d 상태 변경 실패: %s", r.Code, r.Body)
	}
	f.deliver(t, chID, "")

	// 두 가지가 있어야 합니다. 하나는 fixed이며 상태 변경입니다. finding_created도 같은 라운드에서 발행될 수 있습니다.
	// 상태 변경은 실제로 나중에 생성되지만 순서에 종속되지 않으며 전체가 발견됩니다.
	found := false
	for i := 0; i < hook.count(); i++ {
		text := markdownText(t, hook.body(t, i))
		if strings.Contains(text, "상태 변경") && strings.Contains(text, "수정됨") {
			found = true
		}
	}
	if !found {
		t.Fatalf("＂상태 변경 → 수정됨＂이 포함된 메시지가 수신되지 않았습니다(총 %d).", hook.count())
	}
}

func TestNotifyStatusChangeSuppressedByDefault(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "상태 변경을 구독하지 마세요",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "변경 사항을 구독하지 않음", "high")
	if r := f.request("PATCH", fmt.Sprintf("/api/exploration/findings/%d", finding), `{"status":"false_positive"}`); r.Code != 200 {
		t.Fatalf("%d 상태 변경 실패: %s", r.Code, r.Body)
	}
	if _, _, err := f.pg.FanOutPendingEvents(context.Background(), 500); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
WHERE d.channel_id=$1 AND e.kind=$2`, chID, notify.EventFindingStatusChanged).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("상태 변경을 구독하지 않은 채널은 상태 변경 전달을 받지 못하고 %d를 받을 수 없습니다.", n)
	}
}

func TestNotifyTestMessageEndpoint(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "테스트 전송",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", chID), ""); r.Code != 200 {
		t.Fatalf("테스트 전송 실패 %d: %s", r.Code, r.Body)
	}
	if hook.count() != 1 {
		t.Fatalf("가짜 수신측은 1개의 테스트 메시지를 수신하고 %d를 받아야 합니다.", hook.count())
	}
	// 테스트 메시지는 한눈에 테스트임을 명확하게 식별할 수 있어야 하며 실제 취약점으로 오인되어서는 안 됩니다.
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "테스트 메시지") {
		t.Fatalf("테스트 메시지는 test: %s로 표시되어야 합니다.", text)
	}
	// 구성이 손상되면 채널의 원래 오류가 사용자에게 진실되게 반환되어야 합니다.
	badID := f.createChannel(t, map[string]any{
		"name":   "잘못된 주소",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	if r := f.request("POST", fmt.Sprintf("/api/notify/channels/%d/test", badID), ""); r.Code != 502 {
		t.Fatalf("배송이 실패하면 502가 반환되고 %d가 획득됩니다: %s", r.Code, r.Body)
	}
}

func TestNotifyDeliveriesHistoryAndRetry(t *testing.T) {
	f := newNotifyFixture(t)
	// failed 배송이 실패해야 하는 주소를 가리킵니다.
	chID := f.createChannel(t, map[string]any{
		"name":   "실패 시 재시도",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": "http://127.0.0.1:1/hook"},
	})
	f.record(t, "푸시가 실패합니다", "high")
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	ch := f.channel(t, chID)
	// 재시도 예산이 소진될 때까지 계속합니다.
	for i := 0; i < db.MaxNotifyAttempts; i++ {
		f.n.stepRealtime(ctx, ch, 50, "")
		if _, err := f.pg.Exec(`UPDATE notification_deliveries SET next_attempt_at = now() - interval '1 minute' WHERE channel_id=$1`, chID); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err := f.pg.QueryRow(`SELECT state FROM notification_deliveries WHERE channel_id=$1`, chID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != db.NotifyStateFailed {
		t.Fatalf("재시도가 끝나면 failed가 되어야 하며 %s를 받아야 합니다.", state)
	}

	r := f.request("GET", fmt.Sprintf("/api/notify/deliveries?channel_id=%d&state=failed", chID), "")
	if r.Code != 200 {
		t.Fatalf("이력 확인 실패 %d: %s", r.Code, r.Body)
	}
	var hist struct {
		Deliveries []struct {
			ID        int64  `json:"id"`
			State     string `json:"state"`
			LastError string `json:"last_error"`
			Attempts  int    `json:"attempts"`
			Title     string `json:"title"`
		} `json:"deliveries"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &hist); err != nil {
		t.Fatal(err)
	}
	if hist.Total != 1 || len(hist.Deliveries) != 1 {
		t.Fatalf("1개의 실패한 배달이 발견되어야 하며 total=%d len=%d", hist.Total, len(hist.Deliveries))
	}
	if hist.Deliveries[0].LastError == "" {
		t.Fatal("실패 이유가 기록에 포함되어야 합니다. 그렇지 않으면 사용자가 문제를 해결할 수 없습니다.")
	}
	if hist.Deliveries[0].Attempts < db.MaxNotifyAttempts {
		t.Fatalf("시도 횟수를 기록해야 하며 그 결과는 %d입니다.", hist.Deliveries[0].Attempts)
	}
	if hist.Deliveries[0].Title != "푸시가 실패합니다" {
		t.Fatalf("기록에 취약점 제목이 표시되어야 합니다. %q를 얻으세요.", hist.Deliveries[0].Title)
	}

	// 수동 재전송: pending로 돌아가서 카운트를 지워야 합니다.
	if r := f.request("POST", fmt.Sprintf("/api/notify/deliveries/%d/retry", hist.Deliveries[0].ID), ""); r.Code != 200 {
		t.Fatalf("재전송 실패 %d: %s", r.Code, r.Body)
	}
	var attempts int
	if err := f.pg.QueryRow(`SELECT state, attempts FROM notification_deliveries WHERE id=$1`, hist.Deliveries[0].ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != db.NotifyStatePending || attempts != 0 {
		t.Fatalf("다시 보낸 후에는 pending 및 attempts=0이어야 하며 %s/%d를 가져옵니다.", state, attempts)
	}
}

func TestNotifyMetaAndSettingsRoundTrip(t *testing.T) {
	f := newNotifyFixture(t)
	r := f.request("GET", "/api/notify/meta", "")
	if r.Code != 200 {
		t.Fatalf("meta 실패: %s", r.Body)
	}
	var meta struct {
		Kinds []struct {
			Kind       string   `json:"kind"`
			SecretKeys []string `json:"secret_keys"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.Kinds) != len(notify.Kinds()) {
		t.Fatalf("meta는 모든 %d 채널을 나열하고 %d를 가져와야 합니다.", len(notify.Kinds()), len(meta.Kinds))
	}
	for _, k := range meta.Kinds {
		if len(k.SecretKeys) == 0 {
			t.Errorf("채널 %s 자격 증명 필드가 보고되지 않음", k.Kind)
		}
	}

	// 세 가지 전역 설정 왕복. 후행 슬래시는 정규화되어야 합니다. 그렇지 않으면 링크백에 "//function/..."가 표시됩니다.
	if r := f.request("PUT", "/api/settings", `{"notify_public_base_url":"https://artex.example.com/","notify_digest_interval_min":15,"notify_enabled":true}`); r.Code != 200 {
		t.Fatalf("설정 %d 쓰기 실패: %s", r.Code, r.Body)
	}
	t.Cleanup(func() {
		f.pg.Exec(`DELETE FROM settings WHERE key IN ($1,$2)`, settingNotifyPublicBaseURL, settingNotifyDigestMinutes)
	})
	payload := f.s.settingsPayload()
	if payload["notify_public_base_url"] != "https://artex.example.com" {
		t.Fatalf("반환 링크 주소가 표준화되지 않았습니다: %v", payload["notify_public_base_url"])
	}
	if payload["notify_digest_interval_min"] != 15 {
		t.Fatalf("요약 주기가 유효하지 않습니다: %v", payload["notify_digest_interval_min"])
	}

	// 불법적인 값은 거부되어야 합니다.
	for _, body := range []string{
		`{"notify_public_base_url":"ftp://x"}`,
		`{"notify_digest_interval_min":0}`,
		`{"notify_digest_interval_min":99999}`,
	} {
		if r := f.request("PUT", "/api/settings", body); r.Code != 400 {
			t.Errorf("%s는 400을 반환하고 %d를 가져와야 합니다.", body, r.Code)
		}
	}
}

// TestNotifyDeepLinkUsesPublicBaseURL는 백링크 접합을 커버합니다: public_base_url 장착 시
// 단일 메시지는 버튼이 있는 ActionCard를 사용해야 하며 링크는 취약점 세부 정보 페이지를 가리킵니다.
func TestNotifyDeepLinkUsesPublicBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "뒤로 링크",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	finding := f.record(t, "체인 취약성을 되살리세요", "high")
	f.deliver(t, chID, "https://artex.example.com")

	body := hook.last(t)
	card, _ := body["actionCard"].(map[string]any)
	if card == nil {
		t.Fatalf("백링크가 있는 경우 ActionCard를 적용하여 msgtype=%v를 얻습니다.", body["msgtype"])
	}
	want := fmt.Sprintf("https://artex.example.com/function/findings/detail?id=%d", finding)
	if card["singleURL"] != want {
		t.Fatalf("백링크가 잘못되었습니다. \n는 %s\n를 예상했지만 %v를 얻었습니다.", want, card["singleURL"])
	}
}

// TestNotifyNoDeepLinkWithoutBaseURL 역방향 커버리지: 외부 주소가 할당되지 않은 경우 잘못된 링크가 생성되어서는 안 됩니다.
// (예: localhost 또는 상대 경로를 가리키는 경우) 순수 markdown가 반환되어야 합니다.
func TestNotifyNoDeepLinkWithoutBaseURL(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	chID := f.createChannel(t, map[string]any{
		"name":   "백링크 없음",
		"kind":   notify.KindDingTalk,
		"config": map[string]any{"webhook": hook.URL},
	})
	f.record(t, "백링크 취약점 없음", "high")
	f.deliver(t, chID, "")

	body := hook.last(t)
	if body["msgtype"] != "markdown" {
		t.Fatalf("외부 주소가 할당되지 않은 경우 markdown를 보내야 하며 %v를 얻습니다.", body["msgtype"])
	}
	if text := markdownText(t, body); strings.Contains(text, "세부 사항을 확인하세요") {
		t.Fatalf("외부 주소가 할당되지 않은 경우 상세 링크가 표시되지 않아야 합니다. \n%s", text)
	}
}

// TestNotifyDigestSegmentsAndDefersRemainder는 "자동 손실" 복구의 엔드투엔드 증거입니다.
//
// 요약 메시지에는 채널 길이의 상한(Qiwei의 경우 4096바이트)이 적용됩니다. 일괄 처리를 로드할 수 없으면 전체 메시지로 분할해야 합니다.
// 이 항목에 로드된 태그는 전달되었으며 나머지는 대기열로 반환되어 다음 항목을 기다립니다. 이전 구현은 전체 배치를 표시하는 것이었습니다.
// 성공 - 잘린 내용은 메시지나 실패 목록에 없으며 전송 기록에는 여전히 성공으로 표시됩니다.
// 허점이 사라졌습니다.
//
// 4가지를 주장합니다. ① 실제로 로드된 항목 수만 표시됩니다. ② 나머지는 아직 보류 중입니다. ③ 연기된 항목입니다.
// **재시도 횟수는 소모되지 않습니다** ④ 또 다른 라운드를 사용하여 나머지를 보낼 수 있습니다(막히지 않음).
func TestNotifyDigestSegmentsAndDefersRemainder(t *testing.T) {
	f := newNotifyFixture(t)
	hook := newFakeWebhook(t)
	// 엔터프라이즈 위챗(markdown)을 사용하세요. 상한은 4096바이트로 6개 채널 중 가장 빡빡하다.
	chID := f.createChannel(t, map[string]any{
		"name":   "세그먼트 요약",
		"kind":   notify.KindWeCom,
		"mode":   db.NotifyModeDigest,
		"config": map[string]any{"webhook": hook.URL},
	})
	const total = 60
	// 제목을 더 길게 만들어 60개의 항목이 4096바이트보다 훨씬 크므로 분할해야 합니다.
	longName := strings.Repeat("매우 긴 취약점 이름", 6)
	for i := 0; i < total; i++ {
		f.record(t, longName+strconv.Itoa(i+1), "high")
	}
	ctx := context.Background()
	if _, _, err := f.pg.FanOutPendingEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	f.agePendingBatch(t, chID)
	ch := f.channel(t, chID)

	f.n.stepDigest(ctx, ch, 50, "")
	if hook.count() != 1 {
		t.Fatalf("하나의 메시지만 보내야 하며 %d를 받으세요.", hook.count())
	}

	var sent, pending int
	if err := f.pg.QueryRow(`SELECT
    count(*) FILTER (WHERE state=$2),
    count(*) FILTER (WHERE state=$3)
  FROM notification_deliveries WHERE channel_id=$1`, chID, db.NotifyStateSent, db.NotifyStatePending).
		Scan(&sent, &pending); err != nil {
		t.Fatal(err)
	}
	if sent == 0 {
		t.Fatal("상품이 배송됨으로 표시되어야 합니다.")
	}
	if pending == 0 {
		t.Fatalf("%d 스트립 배치를 4096바이트로 완전히 로드하는 것은 불가능하며 전송해야 할 일부가 남아 있어야 합니다. sent=%d", total, sent)
	}
	if sent+pending != total {
		t.Fatalf("항목 수가 일치하지 않습니다. sent=%d pending=%d total=%d(전달되지도 않고 보류되지도 않음 = 손실됨)", sent, pending, total)
	}
	// 메시지 텍스트에는 이 기사에 포함되지 않은 메시지 수를 사실대로 명시해야 합니다.
	if text := markdownText(t, hook.last(t)); !strings.Contains(text, "나머지") {
		t.Fatalf("메시지에는 이 문서에 포함되지 않은 항목이 있음이 표시되어야 합니다. \n%.400s", text)
	}

	// 연기된 항목은 재시도 예산을 소비해서는 안 됩니다. attempts는 청구 시 이미 낙관적 +1이며 연기되면 다시 줄여야 합니다.
	var maxAttempts int
	if err := f.pg.QueryRow(`SELECT COALESCE(max(attempts),0) FROM notification_deliveries
WHERE channel_id=$1 AND state=$2`, chID, db.NotifyStatePending).Scan(&maxAttempts); err != nil {
		t.Fatal(err)
	}
	if maxAttempts > 0 {
		t.Fatalf("연기된 항목은 재시도 횟수를 소모해서는 안 됩니다(그렇지 않으면 몇 번의 항목 이후 실패한 것으로 간주됩니다). 결과적으로 attempts=%d가 발생합니다.", maxAttempts)
	}

	// 수렴될 때까지 반복합니다. 주장되는 바는 **드디어 모두 전달**되었으며 그 과정에서 여러 라운드가 걸렸다는 것입니다.
	// 이는 "두 번째 라운드가 배포됩니다"보다 강력합니다. 이는 세그먼트가 정체되지 않고 나머지 항목이 손실되지 않음을 증명합니다.
	rounds := 0
	for {
		var undelivered int
		if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries
WHERE channel_id=$1 AND state <> $2 AND state <> $3`, chID, db.NotifyStateSent, db.NotifyStateFailed).
			Scan(&undelivered); err != nil {
			t.Fatal(err)
		}
		if undelivered == 0 {
			break
		}
		rounds++
		if rounds > total+5 {
			t.Fatalf("분할된 전달이 수렴되지 않습니다. %d 라운드를 실행한 후에도 여전히 %d 항목이 보류 중입니다.", rounds, undelivered)
		}
		before := hook.count()
		f.n.stepDigest(ctx, ch, 50, "")
		if hook.count() == before {
			t.Fatalf("%d 라운드에 진전이 없으며 나머지 %d 항목은 영구적으로 중단됩니다.", rounds, undelivered)
		}
	}
	if rounds < 2 {
		t.Fatalf("4096바이트 메시지는 %d 긴 헤더 취약점에 맞지 않습니다. 여러 차례에 걸쳐 보내야 합니다. 실제로는 %d탄만 사용되었습니다.", total, rounds)
	}
	// 첫 번째 라운드 이후의 각 라운드는 채널에서 거부된 항목 없이 **순수히 발행**되어야 합니다.
	var failed int
	if err := f.pg.QueryRow(`SELECT count(*) FROM notification_deliveries WHERE channel_id=$1 AND state=$2`,
		chID, db.NotifyStateFailed).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if failed != 0 {
		t.Fatalf("가짜 수신 측이 항상 성공을 반환한다면 실패한 항목이 없어야 하며 %d를 받게 됩니다.", failed)
	}
}

// TestNotifyBackoffTableMatchesAttemptBudget는 드리프트 방지 어설션입니다.
//
// 재시도 예산(db.MaxNotifyAttempts)과 백오프 시퀀스 목록(notifyBackoff)은 두 개의 패키지로 구분됩니다.
// 전자는 상태 머신의 전략이고 후자는 엔진의 실행 리듬입니다. 그 중 하나만 변경되는 경우(예: 예산이 5로 증가)
// 백오프 기어를 한 번 추가하는 것을 잊어버렸습니다. 코드는 오류를 보고하지 않지만 4번째와 5번째 재시도에서는 마지막 기어 간격을 사용하도록 합니다.
// 성능은 "재시도 리듬이 설명할 수 없을 정도로 느려진다"는 것으로, 문제 해결 시 이를 생각하기 어렵습니다.
// 이 드리프트가 CI에 노출되도록 두 길이가 동일하다고 가정합니다.
func TestNotifyBackoffTableMatchesAttemptBudget(t *testing.T) {
	if len(notifyBackoff) != db.MaxNotifyAttempts {
		t.Fatalf("백업 기어 수(%d)가 최대 시도 횟수(%d)와 일치하지 않습니다. 하나를 변경하면 다른 것도 동시에 변경해야 합니다.",
			len(notifyBackoff), db.MaxNotifyAttempts)
	}
	// 백오프 간격은 단조로워야 합니다. 그렇지 않으면 재시도가 점점 더 긴급해져서 현재 제한이 강화됩니다.
	for i := 1; i < len(notifyBackoff); i++ {
		if notifyBackoff[i] < notifyBackoff[i-1] {
			t.Fatalf("백오프 간격은 단조로워야 합니다. %d 기어 %v < %d 기어 %v",
				i, notifyBackoff[i], i-1, notifyBackoff[i-1])
		}
	}
}

// TestNotifyRateLimitDoesNotConsumeRetryBudget는 "토큰을 먼저 얻은 다음 받는다"라는 순서를 잠급니다.
// 역전된 경우(먼저 받은 후 포기한 경우) 현재 제한으로 인해 차단된 배송은 attempts 1회로 계산되며,
// 순수한 기다림에 예산이 소진되어 결국 failed에 빠지게 됩니다.
func TestNotifyRateLimitDoesNotConsumeRetryBudget(t *testing.T) {
	// 토큰 버킷 자체만 테스트하세요. Server는 필요하지 않습니다(또한 토큰 버킷을 구축해서는 안 됩니다).
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// 분당 1개: 버킷이 가득 차면 최대 1개입니다.
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now); got != 1 {
		t.Fatalf("버킷이 가득 차면 1분당 1개의 토큰을 가져와야 하며, %d를 획득합니다.", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("토큰이 소진된 후 즉시 0을 반환해야 하며 결과적으로 %d가 발생합니다.", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(30*time.Second)); got != 0 {
		t.Fatalf("토큰은 중간에 채워서는 안 되며 %d를 획득해야 합니다.", got)
	}
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Minute)); got != 1 {
		t.Fatalf("전체 기간이 지나면 토큰 1개가 보충되고 %d를 획득해야 합니다.", got)
	}
	// 무제한 채널은 제한된 상한을 사용하여 단일 주기에서 무제한 백로그로 인해 제한되는 것을 방지합니다.
	if got := n.takeTokens(2, 0, notifyUnlimitedBurstPerTick+10, now); got != notifyUnlimitedBurstPerTick {
		t.Fatalf("전류가 제한되지 않으면 각 라운드의 상한값을 %d로 반환해야 %d를 얻습니다.", notifyUnlimitedBurstPerTick, got)
	}
	// 채널 간 토큰 버킷은 서로 독립적입니다.
	if got := n.takeTokens(1, 1, notifyMaxSendsPerChannelPerTick, now.Add(time.Millisecond)); got != 0 {
		t.Fatalf("채널 1의 버킷은 여전히 ​​비어 있어야 하며 %d를 가져옵니다.", got)
	}
}

// TestNotifyTakeTokensKeepsUnusedTokens는 "want만 가져옵니다"라는 의미를 잠급니다.
//
// 이전 구현에서는 전체 버킷을 비운 다음 호출자에 의해 잘랐으므로 rate=100/min 채널이 버킷을 채웠습니다.
// 한 라운드에는 5개의 토큰만 사용되며 나머지 95개의 토큰은 직접 폐기됩니다. 이번 라운드에서 채널에 보류 중인 배송이 없으면 해당 금액도 차감됩니다.
// 결과적으로 "rate_per_min 바는 백로그 중에 한꺼번에 청구될 수 있다"는 댓글의 주장은 어떠한 경우에도 성립될 수 없다는 것입니다.
func TestNotifyTakeTokensKeepsUnusedTokens(t *testing.T) {
	n := &Notifier{buckets: map[int64]*notifyBucket{}}
	now := time.Now()
	// 버킷은 처음에 가득 차 있으며(100) 이번 라운드에는 5개만 있습니다.
	if got := n.takeTokens(1, 100, 5, now); got != 5 {
		t.Fatalf("want=5인 경우 %d를 얻으려면 정확히 5개의 토큰을 가져와야 합니다.", got)
	}
	// 핵심 주장: 나머지 95개는 여전히 버킷에 있어야 하며 펌핑하거나 폐기해서는 안 됩니다.
	// 시간을 앞당기지 않으면 재고를 보충하지 않고 재고에서만 얻을 수 있습니다.
	if got := n.takeTokens(1, 100, 95, now); got != 95 {
		t.Fatalf("남은 토큰은 계속 사용할 수 있습니다(95개 예상). %d를 얻으세요. 버킷은 전체 라운드 동안 비워집니다.", got)
	}
	if got := n.takeTokens(1, 100, 1, now); got != 0 {
		t.Fatalf("버킷이 소진되었으므로 0이 반환되어야 하며 %d를 얻습니다.", got)
	}
	// want<=0 토큰이 차감되어서는 안 됩니다(빈 라운드에 대해서는 비용이 부과되지 않음).
	n2 := &Notifier{buckets: map[int64]*notifyBucket{}}
	if got := n2.takeTokens(1, 20, 0, now); got != 0 {
		t.Fatalf("want=0은 0을 반환하고 %d를 가져와야 합니다.", got)
	}
	if got := n2.takeTokens(1, 20, 20, now); got != 20 {
		t.Fatalf("want=0일 때 토큰을 소비해서는 안 되며, 여전히 20을 얻을 수 있어야 하며 %d를 얻을 수 있어야 합니다.", got)
	}
}

// TestDigestTickPlanDecouplesBatchSizeFromSendBudget는 요약 모드의 두 가지 차원을 고정합니다.
//
// 요약 배치의 크기가 각 라운드의 요청 예산에 연결되면 rate_per_min=20인 채널은
// 1개의 토큰이 3초 안에 tick에 추가되므로 각 요약 메시지에는 1개의 취약점만 포함됩니다. 기능적으로는 없음과 동일합니다.
// 요약 및 메시지 헤더에는 "지난 30분 동안 1개의 새로운 취약점이 추가되었습니다."라고 표시됩니다. 이 성능 저하로 인해 오류가 보고되지는 않습니다.
// 기존 엔드 투 엔드 사용 사례를 볼 수 없습니다(수동으로 충분히 큰 limit를 stepDigest에 전달합니다.
// step의 할당량 계산을 우회하므로 여기서 결정 자체가 직접적으로 주장됩니다.
func TestDigestTickPlanDecouplesBatchSizeFromSendBudget(t *testing.T) {
	tokens, claimLimit := digestTickPlan()
	// 일괄 처리 1개 = 메시지 1개 = 요청 1개 = 토큰 1개. 토큰 단위는 취약점이 아닌 메시지입니다.
	if tokens != 1 {
		t.Fatalf("배치를 요약하고 하나의 메시지만 보내면 정확히 1개의 토큰을 소비하고 %d를 얻어야 합니다.", tokens)
	}
	if claimLimit != db.MaxDigestBatchSize {
		t.Fatalf("요약 배치 크기는 메모리 상한 db.MaxDigestBatchSize=%d여야 하며 결과적으로 %d가 됩니다.",
			db.MaxDigestBatchSize, claimLimit)
	}
	// 주요 관계: 배치 크기는 라운드당 요청 예산보다 훨씬 커야 합니다. 일단 둘의 크기가 같다면,
	// 설명은 '여러 개의 메시지를 보낸다'와 '일괄적으로 여러 취약점을 설치한다'를 하나의 숫자로 혼동한 것이다.
	if claimLimit <= notifyMaxSendsPerChannelPerTick {
		t.Fatalf("총 배치 크기 %d는 라운드당 요청 예산 %d에 의해 제한되어서는 안 됩니다."+
			"요청 예산은 임대에서 역으로 계산됩니다. ＂전송되는 요청 수＂와 ＂한 배치에 설치된 취약점 수＂는 두 가지 차원입니다.",
			claimLimit, notifyMaxSendsPerChannelPerTick)
	}
}

// TestNotifyTickBudgetFitsWithinLease는 또 다른 드리프트 방지 주장입니다.
//
// 단일 채널(notifyMaxSendsPerChannelPerTick)에 대한 라운드당 배송 항목 수의 상한은 임대 기간에서 추론됩니다.
// 한 라운드에서 직렬 전달에 대한 최악의 경우 시간은 < 임대여야 합니다. 그렇지 않으면 다음 몇 임대에 대한 임대가 전송되기 전에 만료됩니다.
// 여러 인스턴스를 배포할 때 피어는 인스턴스를 다시 가져와 반복적으로 보냅니다. 이 세 가지 상수는 서로 다른 위치에 있습니다.
// 둘 중 하나를 변경하면 오류 없이 관계가 깨집니다. 따라서 관에 못이 박히게 됩니다.
func TestNotifyTickBudgetFitsWithinLease(t *testing.T) {
	worst := time.Duration(notifyMaxSendsPerChannelPerTick) * notifySendTimeout
	if worst >= notifyLease {
		t.Fatalf("단일 채널 라운드 %v의 최악의 경과 시간은 임대 %v에 도달하거나 초과해서는 안 됩니다."+
			"（notifyMaxSendsPerChannelPerTick=%d × notifySendTimeout=%v）——"+
			"이 세 가지 상수 중 하나를 변경하려면 나머지 두 가지를 동시에 확인해야 합니다.",
			worst, notifyLease, notifyMaxSendsPerChannelPerTick, notifySendTimeout)
	}
}

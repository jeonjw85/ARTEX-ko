package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// 전역 설정 키(settings 키 값 테이블에 존재하며 테이블을 생성할 필요가 없음)
const (
	// settingNotifyEnabled는 푸시 마스터 스위치입니다. 기본적으로 켜짐: 유지 관리 기간 동안 한 번의 클릭으로 출혈을 멈추는 데 사용됩니다.
	// 기능의 활성화 조건보다는 - 실제 활성화 조건은 "유통채널이 있는지 여부"이다.
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL는 취약점 세부정보에 대한 링크를 생성하는 외부 액세스 주소입니다.
	// (https://artex.example.com）。를 공백으로 두면 메시지에 링크 버튼이 표시되지 않습니다.
	// 프로젝트에는 재사용 가능한 외부 주소 구성이 없으므로 여기에 새 구성이 추가됩니다.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes는 요약 모드의 기간(분)입니다.
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick는 배달 엔진의 폴링 간격입니다. 3초는 이 엔진의 실시간 성능의 상한선이다.
	// 이는 또한 "취약점이 기록되었습니다"와 "메시지가 IM에 도착합니다" 사이의 지연의 주요 원인이기도 합니다.
	notifyTick = 3 * time.Second
	// notifyLease는 배송물 픽업 시 임대 시간입니다. 단일 배송에 대한 최악의 경과 시간보다 훨씬 커야 합니다.
	// (notify 패키지의 HTTP 클라이언트는 15초 동안 시간 초과됩니다.) 그렇지 않으면 동일한 줄이 두 번 표시됩니다.
	// dispatcher가 동시에 배송되었습니다.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick는 각 라운드에서 전달되는 이벤트 수를 제한하여 다음을 방지합니다.
	// 전체 이력 백로그를 납품 업무로 한번에 확장합니다.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes는 집계 기간의 기본값입니다.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick는 채널이 전류 제한을 설정하지 않은 경우 각 전달 라운드의 상한입니다.
	// 존재의미는 "무한 트래픽 + 수천개의 허점을 한번에 쓸어내는 채널"을 막는 것
	// 단일 사이클이 오랫동안 지속됩니다.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick는 각 라운드에서 단일 채널로 배달되는 최대 아이템 수입니다.
	//
	// 이 상한은 **대여 기간**에서 추론됩니다. 픽업 시 은행에 제공되는 임대 번호는 (notifyLease = 3분)이며,
	// 한 라운드의 연속 배송 항목 수가 너무 많아서 최악의 경우 임대보다 오래 걸리는 경우 다음 몇 가지 항목에 대한 임대는 배송되기 전에 만료됩니다.
	// 단일 프로세스 내에서는 중요하지 않지만(Run는 단일 goroutine 직렬 실행이며 tick는 다시 입력되지 않습니다) ** 2
	// 프로세스가 라이브러리에 연결되면 피어는 임대가 만료된 행을 다시 획득하여 반복적으로 보냅니다.
	// attempts는 두 배로 증가하며 원래 프로세스가 계속 전달되는 동안 실패한 것으로 판단됩니다.
	//
	// 값: 3분 임대 / 30초 단일 제한 시간 = 6은 **완전 임대**, 마진 없음,
	// 복용할 수 없습니다. 최악의 경우인 150초에 대해 30초의 여유를 허용하려면 5를 사용하십시오. 이 관계는 다음으로 인해 발생합니다.
	// TestNotifyTickBudgetFitsWithinLease 십자가형 - notifyLease로 변경됨,
	// notifySendTimeout 또는 이 값으로 인해 해당 어설션이 실패하게 됩니다.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout는 단일 전달에 대한 시간 초과입니다. 또한 이전 상수의 값을 결정합니다.
	// 둘의 곱셈은 notifyLease를 초과할 수 없습니다. TestNotifyTickBudgetFitsWithinLease를 참조하세요.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff는 실패한 재시도에 대한 백오프 시퀀스이고 아래 첨자는 시도 횟수입니다.
// 3번의 기회(첫 번째 포함)는 db.MaxNotifyAttempts에 해당하며, 둘 다 함께 변경해야 합니다.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier는 취약점 푸시 전달 엔진입니다.
//
// Scheduler와 병렬로 독립적인 goroutine로 작동합니다(server.New 참조). 의도적으로 재사용하지 않음
// Scheduler의 tick: 푸시의 실시간 요구 사항(3초)은 트리거의 비즈니스 리듬과 다릅니다.
// 그리고 둘의 실패는 서로 관련이 없습니다. 푸시가 멈췄더라도 agent의 트리거링에 영향을 주어서는 안 됩니다.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu는 buckets를 보호합니다. 채널 수가 적고 경쟁이 낮으며 뮤텍스 잠금이면 충분합니다.
	// 더 세밀한 구조를 도입하는 것은 가치가 없습니다.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket는 단일 채널 토큰 버킷입니다.
//
// "매분 지우기" 슬라이딩 창 대신 토큰 버킷을 사용하는 이유는 후자가 끔찍한 가장자리 효과를 갖기 때문입니다.
// 기간이 끝나면 20개의 메시지가 전송되고 다음 순간에 또 다른 20개의 메시지가 전송됩니다. 플랫폼의 경우 1초에 40개의 메시지입니다.
// 현재 제한적입니다. 토큰 버킷은 이러한 폭발을 자연스럽게 방지하기 위해 일정한 속도로 보충됩니다.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run는 ctx가 끝날 때까지 반복됩니다. server.New에 의해 한 번 시작되었습니다.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step 한 라운드 실행: 새 이벤트가 먼저 전달된 다음 정해진 작업이 전달됩니다.
//
// 단계가 실패하면 기록만 되고 주기는 중단되지 않습니다. 알림 시스템의 실패가 프로세스 수준 문제로 확대되어서는 안 됩니다.
// 각 tick는 독립적이며 다음 라운드에서 자연스럽게 재시도됩니다.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] 디스패치 이벤트 실패: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] 채널을 읽지 못했습니다.: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// 토큰 버킷의 측정 단위는 취약점 수가 아닌 메시지 수(HTTP 요청 수와 동일)입니다.
		// 실시간 모드에서는 두 가지가 동일합니다(취약성 1개, 메시지 1개). 요약 모드에서는 전체 취약점 배치가 종합됩니다.
		// 하나의 메시지이므로 하나의 토큰만 사용됩니다.
		//
		// 두 모드 모두 먼저 토큰 버킷을 요청한 다음 금액에 따라 요청합니다. 순서는 되돌릴 수 없으며 그렇지 않으면 현재 한도에 의해 차단됩니다.
		// 배달 재시도 횟수가 소진되었습니다.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan는 요약 채널의 현재 라운드의 토큰 소비 및 배치 크기 상한을 반환합니다.
//
// 두 개의 반환 값은 **두 개의 서로 다른 차원**이므로 독립적인 함수입니다.
//
//   - tokens는 메시지 개수입니다. 일련의 취약점이 하나의 메시지로 결합되어 HTTP 요청이 전송되므로 항상 1입니다.
//     따라서 rate_per_min는 digest에 대해 계속 적용됩니다(분당 최대 집계 메시지 수).
//   - claimLimit는 이 배치에 설치된 최대 취약점 수입니다. 이는 메모리 상한에 의해서만 제한되며 요청 예산과는 아무 관련이 없습니다.
//
// rate_per_min를 digest에 유효하게 만들기 위해 각 라운드의 요청 예산은
// (임대에서 파생된 notifyMaxSendsPerChannelPerTick)는 배치 크기로 직접 전달됩니다.
// 결과적으로 rate_per_min=20인 채널은 tick의 3초에 토큰 1개만 보충하므로 각 요약은
// 메시지에는 하나의 취약점만 포함되어 있습니다. digest는 "요약 복사본이 포함된 실시간 푸시"로 변질되며 독자는 일련의 취약점을 수신합니다.
// "지난 30분 동안 새로운 취약점이 추가되었습니다", db.MaxDigestBatchSize에 접근할 수 없습니다.
//
// 이 증상은 엔드투엔드 테스트에서 찾기가 쉽지 않습니다(기존 사용 사례에서는 충분히 큰 limit를 수동으로 전달하여
// stepDigest, step의 할당량 계산을 우회하므로 여기에 결정이 포함됩니다.
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget가 직접 고정되었습니다.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime는 하나의 취약점과 하나의 메시지로 특정 채널로부터 실시간 작업을 수신하고 전달합니다.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] 실시간 배송을 받지 못했습니다. channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("채널 유형 %q 등록되지 않음", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// 렌더링 실패는 로컬 데이터 문제이므로 다시 시도해도 문제가 개선되지 않습니다.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest는 특정 채널의 보류 중인 전달을 하나의 메시지로 집계하고 배치가 만료되면 이를 보냅니다.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] 배치 요약 실패 확인 channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] 요약 배치를 수신하지 못했습니다. channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("채널 유형 %q 등록되지 않음", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// 스냅샷이 손상되어 메시지를 수신할 수 없는 전송은 명시적으로 실패해야 합니다. 그렇지 않으면 그들은 남을 것이다
	// included를 제외하고는 메시지나 실패 목록이 입력되지 않으며 전송이 성공하면 상태가 변경됩니다.
	// 후속 배치 표시는 누락되며 임대가 만료되고 반복적으로 청구될 때까지 항상 sending에 유지됩니다.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "이벤트 스냅샷을 구문 분석할 수 없으며 이 취약점을 메시지로 렌더링할 수 없습니다."
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] 잘못된 스냅샷 전송 실패로 표시 channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] 뛰어넘다 %d 게시물 스냅샷을 파싱할 수 없습니다. channel=%d", len(skipped), ch.ID)
	}
	// 메시지를 입력한 사람만 send에 제공됩니다. included[i]는 msg.Items[i]와 엄격하게 일치합니다.
	// send는 이 서신을 사용하여 "첫 번째 K 품목을 포함하는 채널 반품"을 올바른 배송 라인에 배치합니다.
	n.send(ctx, channel, cfg, msg, included)
}

// send가 전달되고 그 결과에 따라 상태가 전송됩니다.
//
// 동일한 전달 배치(요약 모드에서는 수십 개일 수 있음)는 전달 결과를 공유합니다. 전달되거나 전체 배치가 재시도됩니다.
// 하나씩 재시도하지 마세요. 요약 메시지는 하나이며, 일부를 다시 보내면 배치 의미가 엉망이 됩니다.
//
// 유일한 예외는 채널 길이의 상한으로 인해 발생하는 분할입니다. 채널 반환에는 실제로 첫 번째 K 항목만 포함됩니다.
// 그런 다음 K+1의 항목은 성공적으로 표시되는 대신 다음 배치를 위해 예약되어야 합니다. 그렇지 않으면 잘립니다.
// 이러한 취약점은 메시지나 실패 목록에도 없으며 완전히 사라졌습니다.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// 단일 전달에 대한 상한을 설정하여 이번 라운드에서 하나의 채널이 정체되어 나머지 모든 채널이 아래로 끌리는 것을 방지합니다.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// 채널에서 보고된 항목 수는 배송 수를 초과할 수 없습니다. 이런 일이 발생하면 렌더링 레이어가 계산을 잘못한 것입니다.
			// 지저분하게 기록을 작성하는 것보다 다 전달된 것으로 취급하고 문제를 적어 두는 것이 더 좋습니다.
			log.Printf("[notify] 채널신고 전달번호 %d 배송 개수 초과 %d channel=%s，모두 배송된 것으로 취급",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] 전달 실패로 표시 channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// 이 메시지는 채널 길이의 상한에 도달했습니다. 나머지는 즉시 대기열로 반환되고 다음 tick에 의해 계속됩니다.
			// RescheduleDeliveries 대신 DeferDeliveries를 사용하세요. 이는 실패가 아닙니다.
			// 재시도 예산은 소비되어서는 안 됩니다. (수신 당시 이미 낙관적 +1이었으며 그곳에서 감소될 것입니다.)
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("이 메시지는 채널의 최대 길이에 도달했습니다. 첫 번째 %d 메시지만 전달되고 나머지는 다음 배치를 위해 예약됩니다.", delivered)); err != nil {
				log.Printf("[notify] 분할된 재전송 대기열에 실패했습니다. channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// 채널에서는 오류를 보고하지 않았으며 메시지가 얼마나 전달되었는지도 밝히지 않았습니다. 실패로 간주(뒤로 물러서),
		// 이렇게 하면 이 배달이 반복적으로 수집되지만 표시되지는 않습니다.
		err = fmt.Errorf("채널에서 배송된 항목 수를 보고하지 않았습니다(delivered=%d).", delivered)
	}

	// 실패 처리는 전체 배치의 최대 시도 횟수를 기준으로 하는 것이 아니라 항목별로 결정됩니다.
	//
	// 예전에는 `if maxAttempts(deliveries) >= MaxNotifyAttempts`였습니다. 배치 전체가 죽을 지경이라는 비판을 받았지만 배치에서는
	// 각 항목에 대한 시도 횟수는 동일하지 않습니다. 두 번 재시도된 이전 전달(attempts=2)은 동일한 배치에 포함됩니다.
	// 새로운 전달(attempts=1)을 failed로 함께 드래그하세요. 새로운 취약점은 단 한 번의 재시도 없이 영원히 사라집니다.
	// '기존 산업이 신산업을 끌어내리지 않는다'는 당초 취지와 정반대다.
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] 표시 실패 상태 오류 channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("%d 횟수 재시도 후 실패: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] 표시 실패 상태 오류 channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// 지연된 그룹화에 의한 재정렬: 백오프 수준은 3개뿐이며 그룹 수는 당연히 매우 적습니다. 각 항목을 별도로 보낼 필요가 없습니다.
	// UPDATE(500개 품목 배치에 대해 500회 왕복이 발생함).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] 재주문 배송 실패 channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] 배송실패 channel=%d kind=%s 영구적인 실패=%d 소진된 재시도=%d 재시도 예정=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries는 keep에 없는 all의 항목을 반환합니다(포인터 ID로 비교).
// "실패한" 전달을 찾는 데 사용됩니다. 전달은 명시적으로 처리되어야 하며 회색 영역에 남겨둘 수 없습니다.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt는 채널 구현을 가져오고 해당 구성을 구문 분석합니다.
// ok=false를 반환한다는 것은 해당 타입이 등록되지 않았음을 의미하며, 무한 재시도가 아닌 바로 전달 실패로 판단해야 한다는 뜻이다.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// 구성 구문 분석이 실패하면 빈 map가 제공됩니다. 채널 자체 Validate는 "어떤 필드가 누락되었나요?"라고 보고합니다.
		// 해당 오류는 JSON 구문 분석 오류보다 더 나은 사용자 수정 지침을 제공합니다.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle는 단일 취약점 메시지를 렌더링합니다.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch 렌더링 요약 메시지입니다. 스냅샷을 하나씩 분석하세요. 스냅샷 중 하나가 손상된 경우 해당 스냅샷을 건너뛰세요.
// 전체 배치 요약을 아래로 끌어내리지 마십시오.
//
// 반환 값 included는 msg.Items(i번째 전달 ← i번째 항목)와 엄격한 일대일 대응을 갖습니다.
// 이 서신은 엄격한 요구 사항입니다. 호출자는 "첫 번째 K 항목이 로드된 채널 보고서"를 기반으로 첫 번째 K 배달을 결정합니다.
// 마크가 배달되었습니다. 여기서 잘못된 스냅샷을 건너뛰었지만 건너뛴 전달이 included에서 제거되지 않은 경우,
// 아래 첨자가 잘못 정렬됩니다. 실패했어야 하는 잘못된 항목은 배송된 것으로 표시되고, 좋은 항목은 배송되지 않은 것으로 잘못 표시됩니다.
// 깨진 것들은 호출자에 의해 명시적으로 실패로 표시됩니다. stepDigest를 참조하세요.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// 잘못된 스냅샷은 메시지나 included를 전달하지 않습니다. 폐기는 호출자의 책임입니다.
			// (실패를 "전달됨"으로 처리하지 않고 명시적으로 표시합니다.)
			log.Printf("[notify] 롤업 일괄 처리에서 해결되지 않은 스냅샷 건너뛰기 delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("요약 배치 %d의 모든 납품을 구문 분석할 수 없습니다.", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor는 이벤트 스냅샷을 푸시할 항목으로 렌더링하고 자산 이름과 세부 정보를 다시 링크로 구문 분석합니다.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// 자산 이름을 확인하지 못해도 푸시가 차단되어서는 안 됩니다. 이름을 읽을 수 없는 것은 알림을 받지 못하는 것보다 훨씬 덜 심각합니다.
		// 메시지에는 한 줄의 자산이 누락되어 있습니다.
		log.Printf("[notify] 자산 이름을 구문 분석하지 못했습니다. finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// 세부정보 페이지 라우팅은 web/src/app/(main)/function/findings/detail/page.tsx를 참조하세요.
		// query 매개변수 id에서 id 취약점을 읽습니다.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens는 채널 토큰 버킷에서 ** 최대 want ** 토큰을 가져와서 가져온 실제 숫자를 반환합니다.
//
// 토큰 1개 = 메시지 1개(HTTP 요청 1개) 실시간 모드에서는 발신자가 원하는 만큼 메시지를 보낼 수 있습니다.
// 요약 모드에서는 전체 취약점 배치에 대해 하나의 메시지만 전송되며 1이 전달됩니다.
//
// 버킷 용량은 분당 채널의 상한이며 일정한 속도로 보충됩니다. ratePerMin<=0은 전류 제한이 없음을 의미합니다.
// 무한한 백로그로 인해 루프의 단일 라운드가 중단되는 것을 방지할 수 있을 만큼 유한하지만 충분히 큰 값을 반환합니다.
//
// want 이 상한은 필요합니다. 이것이 없으면 전체 버킷을 비울 수 있으며 호출자 자체에는 라운드당 상한이 있습니다.
// 추가 토큰은 사용할 수 없으며 다음 보충 전에 사라집니다. 저장된 버스트 용량에는 절대 도달하지 않습니다.
// "이번 라운드에는 보류 중인 배송이 없습니다"도 차감됩니다.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// 실시간 경과 시간에 추가되는 속도는 초당 ratePerMin/60입니다.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// 아주 작은 epsilon를 추가하고 반올림합니다. 토큰 수는 부동 소수점으로 누적되며 두 번에 채워집니다.
	// 0.5 + 0.5는 0.9999999999를 얻을 수 있으며 직접 int()는 0으로 잘립니다.
	// 수학적으로 가득 찬 버킷에서는 토큰을 꺼낼 수 없습니다. 1e-9는 토큰보다 훨씬 작으며 실제 빚진 금액을 버리지 않습니다.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled는 메인 스위치를 읽습니다.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL는 후행 슬래시를 제거하여 다시 연결하는 데 사용되는 외부 주소를 반환합니다.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval는 요약 기간을 반환하며, 불법이거나 구성되지 않은 경우 기본값으로 돌아갑니다.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot는 해당 이벤트의 스냅샷을 구문 분석하고 전달합니다.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("%d 전송에 대한 이벤트 스냅샷이 비어 있습니다.", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("%d의 이벤트 스냅샷을 구문 분석하고 전달하지 못했습니다: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// 이벤트 유형은 이벤트 동작에 따라 달라지며, 스냅샷에 있는 이벤트는 이전 버전에서 작성되었을 수 있습니다.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

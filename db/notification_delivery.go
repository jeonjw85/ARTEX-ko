package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// 본 문서는 배송 업무의 수집 및 상태 이전에 관한 문서입니다.
//
// 긴 트랜잭션 대신 "임대"를 사용합니다. 행을 sending로 설정하고 next_attempt_at를 다음과 같이 미래로 푸시합니다.
// 임대 만료 시간은 거래 제출 후 네트워크 전달을 수행합니다. 이런 방식으로 배송 중에는 데이터베이스 잠금이 유지되지 않습니다.
// 네트워크 요청은 몇 초 정도 걸릴 수 있으며(클라이언트 시간 초과 15초) 행 잠금을 유지하면 동일한 데이터베이스의 다른 쓰기 작업이 중단됩니다.
//
// 비용은 배송 중에 프로세스가 중단되면 라인이 sending에서 중지된다는 것입니다. **자가 복구**: 임대가 만료된 후
// next_attempt_at가 과거에 빠지면 다음 수집 라운드에서 동일한 라인이 다시 선택됩니다. (수집 조건 참조)
// state IN('pending','sending')). 요청 시 재시도 횟수는 이미 +1이므로 충돌이 발생하지 않습니다.
// 무한 재시도——MaxNotifyAttempts 에 빠지다 failed 수동 처리를 기다리는 중。

// MaxNotifyAttempts는 최대 배송 시도 횟수(첫 번째 포함)입니다.
// 이는 전달 엔진이 아닌 여기에 정의됩니다. 이는 상태 머신 자체의 전략이며 엔진은 단지 실행자일 뿐입니다.
const MaxNotifyAttempts = 3

// MaxDigestBatchSize는 한 번에 단일 요약 배치로 결합할 수 있는 최대 배달 수입니다.
//
// 존재 이유는 자원이다: 요약 주기로 수만 개의 취약점을 스캔한다면(완전히 가능 - 전체 스캔
// 가능), 상한이 없으면 컬렉션은 모든 줄을 메모리로 읽어 들여 매우 긴 메시지로 렌더링합니다.
// 그런 다음 대부분은 채널의 최대 길이에 의해 잘립니다. 이로 인해 메모리가 낭비되고 잘린 허점이 **조용히 손실**됩니다.
// 상한 설정 후 초과된 부분은 라이브러리에 보관되어 다음 배치가 되며, 손실 없이 자연스럽게 다음 사이클에 발송됩니다.
//
// 500을 선택하는 기준: 메시지로 렌더링한 후에도 Qiwei의 상한 4096바이트 내에 여전히 "읽을 수 있는 콘텐츠"가 있는 크기입니다.
// 아무리 크더라도 잘림이 더 뒤로 발생하게 됩니다.
const MaxDigestBatchSize = 500

// NotificationDelivery는 렌더링에 필요한 채널 구성 및 이벤트 스냅샷을 포함한 전달 작업입니다.
type NotificationDelivery struct {
	ID            int64           `json:"id"`
	EventID       int64           `json:"event_id"`
	ChannelID     int64           `json:"channel_id"`
	State         string          `json:"state"`
	Attempts      int             `json:"attempts"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error"`
	BatchID       *int64          `json:"batch_id,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	Snapshot      json.RawMessage `json:"snapshot,omitempty"`
	// JSON(server 레이어에서 조립된 DTO)가 아닌 유니온 로드 렌더링 컨텍스트입니다.
	Channel *NotificationChannel `json:"-"`
	// FindingID/EventKind는 기록 목록에 대한 이벤트에서 가져와 취약점 세부 정보로 직접 이동합니다.
	FindingID int64  `json:"finding_id,string"`
	EventKind string `json:"event_kind"`
	// ChannelName/ChannelKind는 목록 표시에 사용되는 중복 필드이므로 프런트 엔드에서 보조 쿼리가 필요하지 않습니다.
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
}

const notificationDeliveryCols = `d.id, d.event_id, d.channel_id, d.state, d.attempts, d.next_attempt_at,
       d.last_error, d.batch_id, d.created_at, d.sent_at`

// joinedDeliveryQuery는 전달 행의 통합 읽기 형태(전달 + 이벤트 스냅샷 + 채널 구성)입니다.
// 세 가지 모두 메시지를 렌더링하는 데 필수적이며 이를 별도로 확인하려면 세 번의 왕복이 필요합니다.
const joinedDeliveryQuery = `SELECT ` + notificationDeliveryCols + `,
       e.snapshot, e.kind, e.finding_id,
       c.id, c.name, c.kind, c.enabled, c.config, c.mode, c.filter, c.rate_per_min
FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id
JOIN notification_channels c ON c.id = d.channel_id`

func scanNotificationDelivery(sc interface{ Scan(...any) error }) (*NotificationDelivery, error) {
	var (
		dl        NotificationDelivery
		lastErr   sql.NullString
		batchID   sql.NullInt64
		sentAt    sql.NullTime
		snapshot  []byte
		eventKind string
		channel   NotificationChannel
		chEnabled bool
	)
	if err := sc.Scan(&dl.ID, &dl.EventID, &dl.ChannelID, &dl.State, &dl.Attempts, &dl.NextAttemptAt,
		&lastErr, &batchID, &dl.CreatedAt, &sentAt,
		&snapshot, &eventKind, &dl.FindingID,
		&channel.ID, &channel.Name, &channel.Kind, &chEnabled, &channel.Config, &channel.Mode, &channel.Filter, &channel.RatePerMin); err != nil {
		return nil, err
	}
	dl.LastError = lastErr.String
	if batchID.Valid {
		dl.BatchID = &batchID.Int64
	}
	if sentAt.Valid {
		dl.SentAt = &sentAt.Time
	}
	dl.Snapshot = json.RawMessage(snapshot)
	dl.EventKind = eventKind
	dl.ChannelName = channel.Name
	dl.ChannelKind = channel.Kind
	channel.Enabled = &chEnabled
	dl.Channel = &channel
	return &dl, nil
}

// claimQuery는 일회성 수집을 설명합니다. 먼저 sel를 눌러 후보를 선택하고 잠근 다음 sending로 설정하고
// 임대를 연장하세요. sel의 lease 위치는 $n를 사용하는 호출자가 차지하고 자체적으로 매개변수를 전달합니다.
type claimQuery struct {
	sql  string
	args []any
}

// ClaimRealtimeDeliveries 특정 채널에서 최대 limit까지 실시간 일괄 전송을 받습니다.
//
// "전역적으로 배치를 수집한 다음 배포"하는 대신 **단일 채널**에 따라 의도적으로 수집합니다. 현재 제한기는 전달 엔진의 채널을 기반으로 합니다.
// 유지 관리, 먼저 이 채널이 이번 라운드에 보낼 수 있는 라인 수를 파악한 다음 동일한 수의 라인을 수집해야만 현재 제한이 소비되지 않습니다.
// 재시도 횟수. 먼저 리드로 반전되었다가 포기하는 경우 전류 제한에 의해 차단된 행은 attempts로 한 번 계산되었으며,
// 순수한 기다림으로 인해 예산의 3배가 소모되어 결국 failed에 빠지게 됩니다.
//
// 조건에는 "리스가 만료된 sending"가 포함되어 있으며 이것이 충돌 복구 지점입니다. lease는 단일보다 훨씬 커야 합니다.
// 최악의 배달 시간(채널 HTTP 클라이언트 시간 초과 15초), 그렇지 않으면 동일한 라인이 두 개의 dispatcher에 의해 전송됩니다
// 동시에 배송됩니다. 동시에 비활성화된 채널을 차단합니다. 비활성화 작업으로 인해 인벤토리 전달이 skipped로 표시되었습니다.
// 비활성화와 수집이 동시에 진행되는 경우 누출을 방지하기 위한 또 다른 블록이 있습니다.
func (d *DB) ClaimRealtimeDeliveries(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now()
  AND c.enabled AND c.mode = $4
ORDER BY dd.next_attempt_at, dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $5`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, NotifyModeRealtime, limit},
	}, nil)
}

// DigestBatchDue는 채널에 만료된 배치가 충분히 축적되었는지 보고합니다. 보류 중인 전달이 있고 **가장 오래된 것**이 있습니다.
// 연령이 집계 기간에 도달했습니다.
//
// 판단은 벽시계가 아닌 가장 오래된 배달 날짜를 기준으로 합니다. 이렇게 하면 새로 구축된 채널이 시간에 맞춰 정렬되어도 영향을 받지 않습니다.
// 단 하나의 항목에 대한 "요약"을 즉시 뱉어 내면 오랫동안 밀린 배치는 다음 라운드를 헛되이 기다리지 않을 것입니다.
//
// 의미론이 다르기 때문에 ClaimDigestBatch와 구분됩니다. 이 함수는 "보내야 할까요?"에만 응답합니다.
// 이를 받으려면 출시될 채널의 **모든** 채널을 제거해야 합니다(아직 해당 연령에 도달하지 않은 채널도 포함). 그렇지 않은 경우 1사이클
// 여러 개의 메시지로 분할되어 요약 내용이 의미가 없게 됩니다.
func (d *DB) DigestBatchDue(ctx context.Context, channelID int64, minAge time.Duration) (bool, error) {
	var due bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM notification_deliveries d
  JOIN notification_channels c ON c.id = d.channel_id
  WHERE d.channel_id = $1 AND d.state IN ($2,$3) AND c.enabled
  GROUP BY d.channel_id
  HAVING min(d.created_at) <= now() - make_interval(secs => $4)
)`, channelID, NotifyStatePending, NotifyStateSending, int64(minAge.Seconds())).Scan(&due)
	return due, err
}

// ClaimDigestBatch는 특정 채널의 현재 예정된 전달을 요약 배치로 수신합니다.
// 단일 배치의 최대 항목 수는 MaxDigestBatchSize입니다.
//
// 동일한 배치의 모든 배송은 batch_id를 공유하고 세트에서 가장 작은 id를 배치 번호(안정적, 읽기 가능,
// 추가 순서가 필요하지 않습니다). 재시도할 때 COALESCE를 사용하여 원래 배치 번호를 유지하여 "이 N 품목 배치가 함께 배송됩니다."
// 여러 번 재시도한 후에도 계속 유지됩니다.
//
// 첫 번째 N 항목은 무작위가 아닌 id의 오름차순으로 가져옵니다. 가장 빠른 배송이 먼저 발송되고 백로그에 표시되지 않습니다.
// "새로운 취약점이 먼저 나타나고 오래된 취약점은 항상 마지막에 나타납니다."
func (d *DB) ClaimDigestBatch(ctx context.Context, channelID int64, limit int, lease time.Duration) ([]*NotificationDelivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	// limit는 **메모리 상한**이며, 호출자는 MaxDigestBatchSize를 전달합니다. 여기 또 다른 링크가 있습니다.
	// 호출자가 더 큰 값을 전달하는 것을 방지합니다.
	//
	// 의도적으로 "현재 제한 할당량"을 배치 크기로 받아들이지 마십시오. 현재 제한의 단위는 메시지 수입니다. 하나의 배치만 전송됩니다.
	// 하나의 메시지는 하나의 토큰을 소비하며 이는 server 레이어의 takeTokens에서 공제됩니다. 이는 "여러 메시지를 하나의 일괄 처리로 패키지화"하는 것과 동일합니다.
	// "취약점"은 두 가지 다른 차원입니다. rate_per_min를 digest에 유효하게 만들기 위해 각 라운드마다
	// 요청 예산은 배치 크기로 전달됩니다. 결과적으로 rate=20/min인 채널은 각 배치마다 1개의 취약점만 설치합니다.
	// digest는 요약 복사를 통한 실시간 푸시로 변질됩니다. 전류 제한을 변경하려면 takeTokens를 want로 변경하십시오.
	// 여기서 움직이지 마세요.
	if limit > MaxDigestBatchSize {
		limit = MaxDigestBatchSize
	}
	out, err := d.claimDeliveries(ctx, lease, claimQuery{
		sql: `SELECT dd.id FROM notification_deliveries dd
JOIN notification_channels c ON c.id = dd.channel_id
WHERE dd.channel_id = $1 AND dd.state IN ($2,$3) AND dd.next_attempt_at <= now() AND c.enabled
ORDER BY dd.id
FOR UPDATE OF dd SKIP LOCKED
LIMIT $4`,
		args: []any{channelID, NotifyStatePending, NotifyStateSending, limit},
	}, func(tx *sql.Tx, ids []int64) error {
		batchID := ids[0]
		for _, id := range ids {
			if id < batchID {
				batchID = id
			}
		}
		ph, idArgs := placeholders(2, ids)
		_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET batch_id = COALESCE(batch_id, $1)
WHERE id IN (`+ph+`)`, append([]any{batchID}, idArgs...)...)
		return err
	})
	return out, err
}

// claimDeliveries는 "선택 + sending 임대 연장 + 전체 행 읽기 설정"을 모두 하나의 트랜잭션으로 수행합니다.
// postClaim는 선택적 추가 단계입니다(배치 요약을 위해 batch_id 작성).
func (d *DB) claimDeliveries(ctx context.Context, lease time.Duration, cq claimQuery, postClaim func(*sql.Tx, []int64) error) ([]*NotificationDelivery, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // 성공적으로 제출되면 no-op입니다.

	ids, err := selectForClaim(ctx, tx, cq.sql, cq.args...)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, tx.Commit()
	}
	// sending를 설정하고 next_attempt_at를 미래로 푸시합니다. 이 미래의 순간은 임대 만료 시간입니다.
	// "임대가 만료되지 않았습니다"와 "재시도 시간이 아직 만료되지 않았습니다"는 동일한 조건식을 공유하므로 새 열을 추가할 필요가 없습니다.
	ph, idArgs := placeholders(3, ids)
	if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=attempts+1, next_attempt_at=now()+make_interval(secs => $2)
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateSending, lease.Seconds()}, idArgs...)...); err != nil {
		return nil, err
	}
	if postClaim != nil {
		if err := postClaim(tx, ids); err != nil {
			return nil, err
		}
	}
	out, err := loadDeliveriesTx(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

func selectForClaim(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func loadDeliveriesTx(ctx context.Context, tx *sql.Tx, ids []int64) ([]*NotificationDelivery, error) {
	ph, args := placeholders(1, ids)
	rows, err := tx.QueryContext(ctx, joinedDeliveryQuery+` WHERE d.id IN (`+ph+`) ORDER BY d.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// MarkDeliveriesSent 일괄 배송을 배송된 것으로 표시합니다.
func (d *DB) MarkDeliveriesSent(ctx context.Context, ids []int64) error {
	ph, args := placeholders(2, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, sent_at=now(), last_error='' WHERE id IN (`+ph+`)`, append([]any{NotifyStateSent}, args...)...)
	return err
}

// RescheduleDeliveries는 배치 배달을 pending로 반환하고 재시도 시간을 연기합니다.
//
// 새로운 중간 상태를 도입하는 대신 pending를 반환하는 것은 "남은 기회 수"가 한 곳에서만 나오도록 하는 것입니다.
// 재시도 전략으로 인해 상태 머신의 분기가 확장되지 않도록 하는 표현식(MaxNotifyAttempts)입니다.
func (d *DB) RescheduleDeliveries(ctx context.Context, ids []int64, delay time.Duration, errMsg string) error {
	ph, args := placeholders(4, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, next_attempt_at=now()+make_interval(secs => $2), last_error=$3
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, delay.Seconds(), truncateNotifyError(errMsg)}, args...)...)
	return err
}

// DeferDeliveries는 즉시 상환 가능한 일괄 배송을 pending로 반환하고 **시계 청구 시도를 취소**합니다.
//
// 목적은 단 하나입니다. 요약 메시지가 채널 길이의 상한에 따라 세그먼트로 전송될 때 이 문서에 포함되지 않은 항목은 다음 일괄 처리를 위해 예약되어야 합니다.
// 이는 실패가 아니므로 재시도 예산을 소모해서는 안 됩니다. attempts는 청구 시 이미 낙관적으로 +1입니다.
// 여기서는 줄여야 합니다. 그렇지 않으면 500개 항목의 백로그가 세그먼트당 20개 항목을 기준으로 25개 세그먼트로 나뉩니다.
// tail 항목은 3항의 MaxNotifyAttempts에서 failed로 확인되었으며, 어떠한 실수도 하지 않았습니다.
//
// GREATEST(...,0)는 "누군가 attempts를 수동으로 재설정한 다음 여기에 오는" 상황을 방지합니다.
// 카운트가 음수가 되도록 두지 마십시오.
func (d *DB) DeferDeliveries(ctx context.Context, ids []int64, reason string) error {
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$1, attempts=GREATEST(attempts-1, 0), next_attempt_at=now(), last_error=$2
WHERE id IN (`+ph+`)`,
		append([]any{NotifyStatePending, truncateNotifyError(reason)}, args...)...)
	return err
}

// FailDeliveries는 일괄 배송을 최종 실패로 표시하고 배송 내역에서 수동 재전송을 기다리고 있습니다.
func (d *DB) FailDeliveries(ctx context.Context, ids []int64, errMsg string) error {
	// 자리 표시자는 $3부터 시작합니다. $1은 state이고 $2는 last_error입니다.
	ph, args := placeholders(3, ids)
	if len(args) == 0 {
		return nil
	}
	_, err := d.ExecContext(ctx, `UPDATE notification_deliveries SET state=$1, last_error=$2 WHERE id IN (`+ph+`)`,
		append([]any{NotifyStateFailed, truncateNotifyError(errMsg)}, args...)...)
	return err
}

// RetryNotificationDelivery 수동으로 배달 재전송: pending로 재설정, 재시도 횟수 지우기,
// 즉시 만료됩니다. 카운트를 지우는 것은 의도적인 것입니다. 수동으로 "재전송"을 클릭하면 이전 실패의 원인이 처리되었음을 의미합니다.
// 이전 카운트를 취하고 제한하는 것은 의미가 없습니다.
func (d *DB) RetryNotificationDelivery(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `UPDATE notification_deliveries
SET state=$2, attempts=0, next_attempt_at=now(), last_error=''
WHERE id=$1 AND state IN ($3,$4)`, id, NotifyStatePending, NotifyStateFailed, NotifyStateSkipped)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("배송 %d가 존재하지 않거나 현재 상태로 인해 재전송이 허용되지 않습니다.", id)
	}
	return nil
}

// NotificationDeliveryFilter는 배송이력 조회 조건입니다.
type NotificationDeliveryFilter struct {
	ChannelID int64
	State     string
	EventKind string
}

func (f NotificationDeliveryFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelID > 0 {
		args = append(args, f.ChannelID)
		conds = append(conds, fmt.Sprintf("d.channel_id=$%d", len(args)))
	}
	if f.State != "" {
		args = append(args, f.State)
		conds = append(conds, fmt.Sprintf("d.state=$%d", len(args)))
	}
	if f.EventKind != "" {
		args = append(args, f.EventKind)
		conds = append(conds, fmt.Sprintf("e.kind=$%d", len(args)))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListNotificationDeliveries 페이징의 배달 내역을 최신 항목부터 반환합니다.
func (d *DB) ListNotificationDeliveries(ctx context.Context, f NotificationDeliveryFilter, page, pageSize int) ([]*NotificationDelivery, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 50
	}
	where, args := f.where()

	var total int
	if err := d.QueryRowContext(ctx, `SELECT count(*) FROM notification_deliveries d
JOIN notification_events e ON e.id = d.event_id`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := fmt.Sprintf("%s%s ORDER BY d.id DESC LIMIT $%d OFFSET $%d",
		joinedDeliveryQuery, where, len(args)+1, len(args)+2)
	rows, err := d.QueryContext(ctx, q, append(args, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*NotificationDelivery{}
	for rows.Next() {
		dl, err := scanNotificationDelivery(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, dl)
	}
	return out, total, rows.Err()
}

// truncateNotifyError 오류 메시지를 허용 가능한 열 길이로 자릅니다. 채널에서 반환된 응답 본문이 매우 길 수 있습니다.
// (특히 일반 Webhook가 자체 구축 서비스에 부딪힐 때 그렇습니다.) 잘림에 실패하면 기록 목록의 로드가 확장됩니다.
func truncateNotifyError(msg string) string {
	const max = 500
	if len(msg) <= max {
		return msg
	}
	// UTF-8 문자의 절반을 남기고 프런트 엔드에 잘못된 문자가 표시되는 것을 방지하려면 문자 경계에 따라 되감습니다.
	cut := max
	for cut > 0 && !isUTF8Start(msg[cut]) {
		cut--
	}
	return msg[:cut] + "…"
}

func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// placeholders는 IN(...)에서 사용할 수 있도록 start부터 시작하는 $n 자리 표시자 문자열과 해당 매개 변수를 생성합니다.
// 예를 들어 start=3, ids=[7,8] → "$3,$4", [7,8]입니다.
func placeholders(start int, ids []int64) (string, []any) {
	ph := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for i, id := range ids {
		ph = append(ph, fmt.Sprintf("$%d", start+i))
		args = append(args, id)
	}
	return strings.Join(ph, ","), args
}

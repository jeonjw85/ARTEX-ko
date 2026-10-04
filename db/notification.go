package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Autumn-27/artex/notify"
)

// 이 파일은 IM에서 푸시한 채널 구성 및 이벤트 레이어입니다. 전달 작업 수집 및 상태 흐름
// db/notification_delivery.go。
//
// 이 파일을 변경할 때 두 가지 불변성을 유지해야 합니다.
//
//  1. 취약점(RecordFindingTx)을 작성하는 트랜잭션은 InsertNotificationEventTx만 호출하여 블라인드 삽입을 수행합니다.
//     알림 관련 테이블을 읽거나 필터링 및 매칭을 수행하지 마십시오. 여기에 소개된 모든 읽기 작업은 다음으로 인해 발생할 수 있습니다.
//     사용자는 필터 조건과 일치하지 않아 취약한 쓰기 트랜잭션을 오염시키거나 심지어 중단합니다.
//  2. 필터 일치는 오류를 보고하지 않습니다. 구성 오류는 "적중"으로 처리됩니다(notify.Match 참조). 차라리 더 밀어붙이는게 나을듯
//     놓치지 마세요.

// ErrNotificationChannelNotFound 채널이 존재하지 않습니다.
var ErrNotificationChannelNotFound = errors.New("알림 채널이 존재하지 않습니다")

// 배송상태.
const (
	NotifyStatePending = "pending" // 갈 준비가 됐어요
	NotifyStateSending = "sending" // 특정 dispatcher에 의해 청구되었으며 임대가 만료되지 않았습니다.
	NotifyStateSent    = "sent"    // 전달됨
	NotifyStateFailed  = "failed"  // 재시도 횟수가 소진되었거나 영구적으로 실패했습니다. 수동으로 다시 보낼 수 있습니다.
	NotifyStateSkipped = "skipped" // 채널이 비활성화되어 더 이상 전송되지 않습니다.
)

// 푸시 모드.
const (
	NotifyModeRealtime = "realtime"
	NotifyModeDigest   = "digest"
)

// ValidNotifyMode 화이트리스트 확인 푸시 모드(findings.status와 동일: DB CHECK를 사용하지 마세요.
// 후속 확장을 용이하게 하기 위해).
func ValidNotifyMode(m string) bool {
	return m == NotifyModeRealtime || m == NotifyModeDigest
}

// NotificationChannel는 채널 인스턴스 구성입니다. Config 및 Filter는 원래 JSON로 유지됩니다.
// 구문 분석은 notify 패키지에 맡겨져 있습니다. db 레이어는 해당 필드의 의미를 이해하지 못합니다.
type NotificationChannel struct {
	ID     int64           `json:"id"`
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Mode   string          `json:"mode"`
	Config json.RawMessage `json:"config"`
	Filter json.RawMessage `json:"filter"`
	// Enabled는 포인터를 사용하여 "이 필드를 전달하지 않음"과 "false를 명시적으로 전달"을 구별합니다.
	// 프런트 엔드 스위치 컨트롤은 변경된 필드만 제출합니다.
	Enabled    *bool     `json:"enabled,omitempty"`
	RatePerMin int       `json:"rate_per_min"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// IsEnabled는 채널이 활성화되었는지 여부를 반환합니다. Enabled가 nil(로드되지 않음)인 경우 활성화된 것으로 처리됩니다.
func (c *NotificationChannel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// NotificationEvent는 이벤트 사실입니다.
type NotificationEvent struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	FindingID int64           `json:"finding_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedAt time.Time       `json:"created_at"`
}

const notificationChannelCols = `id, name, kind, enabled, config, mode, filter, rate_per_min, created_at, updated_at`

func scanNotificationChannel(sc interface{ Scan(...any) error }) (*NotificationChannel, error) {
	var c NotificationChannel
	var enabled bool
	if err := sc.Scan(&c.ID, &c.Name, &c.Kind, &enabled, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = &enabled
	return &c, nil
}

// ListNotificationChannels는 모든 채널 인스턴스를 반환합니다. 활성화된 것이 가장 먼저 순위가 지정되며 동일한 레벨의 것은 id입니다.
// UI 및 dispatcher가 동일한 안정적인 순서를 볼 수 있도록 정렬은 SQL에 배치됩니다.
func (d *DB) ListNotificationChannels(ctx context.Context) ([]*NotificationChannel, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels
ORDER BY enabled DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotificationChannelByID는 단일 채널을 사용합니다.
func (d *DB) NotificationChannelByID(ctx context.Context, id int64) (*NotificationChannel, error) {
	row := d.QueryRowContext(ctx, `SELECT `+notificationChannelCols+` FROM notification_channels WHERE id=$1`, id)
	c, err := scanNotificationChannel(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotificationChannelNotFound
	}
	return c, err
}

// SaveNotificationChannel 채널을 생성하거나 업데이트합니다.
//
// 업데이트 시 호출자가 명시적으로 제공한 필드(nil 아님/비어 있지 않음)만 덮어쓰므로 프런트 엔드에서 부분적으로 제출할 수 있습니다.
// 수정된 서랍 양식은 표시되지 않는 config의 필드를 반환할 필요가 없습니다.
// '마스크 값이 실제 키를 덮어쓰는' 사고.
func (d *DB) SaveNotificationChannel(ctx context.Context, c *NotificationChannel) (int64, error) {
	if c.Mode == "" {
		c.Mode = NotifyModeRealtime
	}
	// 여기서는 의도적으로 0:0에 대한 처리를 수행하지 않습니다. 이는 "현재 제한 없음"을 의미하는 합법적인 구성입니다.
	//
	// `if c.RatePerMin <= 0 { c.RatePerMin = 기본값 }`라고 쓰면 원래 의미는 '지정되지 않은 시간'이다.
	// 안전한 기본값을 제공하세요." 그러나 "명시적으로 0으로 설정"도 무시되었습니다. 문서, UI 프롬프트 및
	// takeTokens는 모두 0을 전류 제한이 없는 것으로 해석하지만 여기서는 조용히 20으로 변경됩니다(DingTalk/Qiwei/Telegram).
	// 또는 100(Feishu), 운영자는 현재 제한이 해제된 것으로 생각했지만 실제로는 아무런 메시지도 없이 분당 20으로 멈춰 있었습니다.
	//
	// "지정되지 않음"과 "명시적 0"의 차이는 호출자만 알 수 있습니다(요청 본문의 필드는 기본적으로 vs로 설정되어 있으며 명시적으로 0을 전달합니다).
	// 따라서 필드가 기본값으로 설정되면 server 레이어에 의해 기본값이 채워집니다. notifyCreateChannel를 참조하세요.
	if c.RatePerMin < 0 {
		return 0, errors.New("전류 제한 값은 음수일 수 없습니다.")
	}
	if c.Config == nil {
		c.Config = json.RawMessage(`{}`)
	}
	if c.Filter == nil {
		c.Filter = json.RawMessage(`{}`)
	}
	enabled := c.IsEnabled()

	if c.ID == 0 {
		var id int64
		err := d.QueryRowContext(ctx, `INSERT INTO notification_channels(name,kind,enabled,config,mode,filter,rate_per_min)
VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
			c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin).Scan(&id)
		return id, err
	}
	res, err := d.ExecContext(ctx, `UPDATE notification_channels
SET name=$2, kind=$3, enabled=$4, config=$5, mode=$6, filter=$7, rate_per_min=$8
WHERE id=$1`,
		c.ID, c.Name, c.Kind, enabled, string(c.Config), c.Mode, string(c.Filter), c.RatePerMin)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotificationChannelNotFound
	}
	return c.ID, nil
}

// SetNotificationChannelEnabled 스위치가 시작 및 중지됩니다.
//
// 채널을 비활성화할 때 전송되지 않은 전달을 skipped로 표시합니다. 그렇지 않으면 다시 활성화한 후
// 만료되어 새로운 것으로 쉽게 오인되는 "비활성화 기간 동안 백로그된" 오래된 취약점 묶음을 갑자기 받게 됩니다.
func (d *DB) SetNotificationChannelEnabled(ctx context.Context, id int64, enabled bool) error {
	return d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE notification_channels SET enabled=$2 WHERE id=$1`, id, enabled)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotificationChannelNotFound
		}
		if !enabled {
			if _, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET state=$2, last_error=$3
WHERE channel_id=$1 AND state IN ($4,$5)`,
				id, NotifyStateSkipped, "채널이 비활성화되었습니다", NotifyStatePending, NotifyStateSending); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteNotificationChannel 채널 삭제. 전달 내역은 외래 키 캐스케이드와 함께 삭제됩니다.
// (채널 구성이 사라져서 기록을 해석할 수 없습니다.)
func (d *DB) DeleteNotificationChannel(ctx context.Context, id int64) error {
	res, err := d.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotificationChannelNotFound
	}
	return nil
}

// RecordNotificationEventTx 호출자의 트랜잭션에서 **최선의 노력**에 대한 푸시 이벤트를 작성합니다.
//
// 취약점 작성 경로에서 유일한 알림 관련 변경 사항은 다음과 같습니다. INSERT 1회, 테이블이 읽히지 않음, 채널이 인식되지 않음,
// 필터링이 수행되지 않습니다. 트랜잭션 제출은 "취약성이 기록됨" 및 "푸시 작업 존재"가 원자적으로 일관되도록 보장합니다.
// 제출이 성공했지만 대기열에 추가되지 않고 메시지가 영구적으로 손실되는 창이 없습니다.
//
// 두 가지 주요 디자인은 아무렇게나 작성되지 않았습니다.
//
//  1. **SAVEPOINT를 사용하는 이유**: PostgreSQL의 거래 내역에 오류가 있으면 전체 거래가 입력됩니다.
//     aborted 상태이면 모든 후속 명령문(COMMIT 포함)이 실패합니다. 그래서 "이 INSERT를 무시하세요.
//     "오류, 호출자가 계속 제출하도록 허용"은 PG에서 불가능합니다. - 오류를 격리하기 위해 저장 지점을 사용하지 않는 한
//     이 진술에. 저장 지점이 없으면 남은 유일한 옵션은 "전체 롤백"입니다.
//
//  2. **전체 롤백이 잘못된 이유**: 푸시는 편의 기능인 반면, 취약점 기록은 제품 자체입니다. 통지서
//     테이블 문제(이전 데이터베이스가 마이그레이션되지 않음, 일시적인 디스크 오류)로 인해 고위험 취약점이 데이터베이스에 저장되는 것을 방해해서는 안 됩니다. 그래서 여기에 고립이 있습니다
//     오류, 기록, false 반환 및 평소와 같이 취약점 쓰기 제출을 허용합니다. 단, 이 푸시가 손실됩니다.
//     error 대신 bool를 반환하는 것은 의도적인 것입니다. 호출자는 이를 쓰기 성공 또는 실패에 영향을 미치는 오류로 처리해서는 안 됩니다.
func RecordNotificationEventTx(ctx context.Context, tx *sql.Tx, kind string, findingID int64, snap notify.Snapshot) bool {
	raw, err := json.Marshal(snap)
	if err != nil {
		log.Printf("[notify] 직렬화 푸시 이벤트 실패 finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT notify_event`); err != nil {
		log.Printf("[notify] 저장점을 생성하지 못했습니다. finding=%d: %v", findingID, err)
		return false
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3)`,
		kind, findingID, string(raw)); err != nil {
		log.Printf("[notify] 푸시 이벤트 작성 실패 finding=%d（취약점 기록은 영향을 받지 않습니다.）: %v", findingID, err)
		// 저장 지점으로 롤백하고 aborted 상태에서 트랜잭션을 복구합니다.
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT notify_event`); rbErr != nil {
			log.Printf("[notify] 저장점으로 롤백하지 못했습니다. finding=%d: %v", findingID, rbErr)
		}
		return false
	}
	// 긴 트랜잭션에서 쓸모 없는 저장 포인트가 누적되는 것을 방지하려면 저장 포인트를 해제하세요.
	_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT notify_event`)
	return true
}

// AddNotificationEvent는 InsertNotificationEventTx의 독립 트랜잭션 버전이며 다음에는 사용할 수 없습니다.
// 기존 트랜잭션의 호출 지점이 사용됩니다(예: 실제 finding가 없는 채널의 "테스트 메시지 보내기").
func (d *DB) AddNotificationEvent(ctx context.Context, kind string, findingID int64, snap notify.Snapshot) (int64, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("직렬화된 알림 이벤트 스냅샷 실패: %w", err)
	}
	var id int64
	err = d.QueryRowContext(ctx, `INSERT INTO notification_events(kind,finding_id,snapshot) VALUES($1,$2,$3) RETURNING id`,
		kind, findingID, string(raw)).Scan(&id)
	return id, err
}

// FanOutPendingEvents 현재 활성화된 채널에 따라 분산되지 않은 취약점 이벤트를 전달 작업으로 확장합니다.
// 이번 라운드에 처리된 이벤트 수와 새로운 전달 수를 반환합니다.
//
// 전체 작업 라운드가 하나의 트랜잭션에 있습니다. 이벤트는 FOR UPDATE SKIP LOCKED로 수신되며 여러 프로세스가 동시에 실행됩니다.
// 그들은 또한 다른 행을 받았습니다(프로젝트의 아카이브 대기열은 동일한 방법을 사용합니다. 참조).
// db/task_archives.go ~의 completeNextArchiveJob）。
//
// 필터 일치는 의도적으로 SQL 대신 Go 측에 배치됩니다. 채널의 필터 조건은 선택적 필드 세트인 JSONB입니다.
// SQL를 사용하여 6개 조합의 매칭을 표현하면 쿼리를 유지하기가 어려워지고 채널 수가 "몇 개 수동으로 할당"됩니다.
// 완전히 로드한 후에는 메모리에서 항목을 하나씩 비교하는 것이 더 빠르고 테스트하기 쉽습니다.
//
// 어떤 채널에도 도달하지 않은 이벤트는 fanned_out로 표시됩니다. 그렇지 않으면 영원히 전달될 세트에 남아 있게 됩니다.
// 각 tick가 다시 스캔됩니다.
func (d *DB) FanOutPendingEvents(ctx context.Context, limit int) (eventCount, deliveryCount int, err error) {
	if limit <= 0 {
		limit = 200
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback() //nolint:errcheck // 성공적으로 제출되면 no-op입니다.

	channels, err := listEnabledNotificationChannelsTx(ctx, tx)
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, finding_id, snapshot FROM notification_events
WHERE NOT fanned_out ORDER BY id FOR UPDATE SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, 0, err
	}
	var (
		events      []NotificationEvent
		parsedSnaps []notify.Snapshot
	)
	for rows.Next() {
		var ev NotificationEvent
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.FindingID, &ev.Snapshot); err != nil {
			rows.Close()
			return 0, 0, err
		}
		var snap notify.Snapshot
		// 스냅샷은 우리가 직접 작성했으며 이론적으로 분석이 가능해야 합니다. 구문 분석에 실패해도 전달 프로세스가 차단되지는 않습니다.
		// 하지만 필드가 모두 비어 있기 때문에 필터 조건이 있는 모든 채널에서 이 이벤트를 건너뜁니다. 하나를 덜 푸시하는 것이 좋습니다.
		// 하나의 잘못된 행이 전체 대기열을 차단하도록 두지 마십시오.
		_ = json.Unmarshal(ev.Snapshot, &snap)
		// kind에는 인라인 값이 적용됩니다. 스냅샷의 복사본은 렌더링에 사용된 복사본이며 이전 버전에서 작성되었을 수 있습니다.
		snap.Kind = ev.Kind
		events = append(events, ev)
		parsedSnaps = append(parsedSnaps, snap)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	if len(events) == 0 {
		return 0, 0, tx.Commit()
	}

	type pending struct {
		eventID   int64
		channelID int64
	}
	var toInsert []pending
	for i, snap := range parsedSnaps {
		for _, ch := range channels {
			if !notify.Match(notify.ParseFilter(ch.Filter), snap) {
				continue
			}
			toInsert = append(toInsert, pending{eventID: events[i].ID, channelID: ch.ID})
		}
	}
	if len(toInsert) > 0 {
		var (
			vals []string
			args []any
		)
		for _, p := range toInsert {
			vals = append(vals, fmt.Sprintf("($%d,$%d)", len(args)+1, len(args)+2))
			args = append(args, p.eventID, p.channelID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_deliveries(event_id,channel_id) VALUES `+strings.Join(vals, ","), args...); err != nil {
			return 0, 0, err
		}
	}

	// 이 이벤트 라운드를 전달됨으로 표시합니다. 어떤 채널에도 도달하지 않은 이벤트도 함께 표시됩니다(함수 설명 참조).
	ids := make([]string, 0, len(events))
	markArgs := make([]any, 0, len(events))
	for _, ev := range events {
		markArgs = append(markArgs, ev.ID)
		ids = append(ids, fmt.Sprintf("$%d", len(markArgs)))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_events SET fanned_out=true WHERE id IN (`+strings.Join(ids, ",")+`)`, markArgs...); err != nil {
		return 0, 0, err
	}
	return len(events), len(toInsert), tx.Commit()
}

// listEnabledNotificationChannelsTx는 트랜잭션의 활성 채널을 나타냅니다. 양이 매우 적고,
// 페이징 또는 캐싱 없음 - 캐싱은 "변경된 구성이 언제 적용됩니까?"라는 추가적인 타이밍 문제를 발생시킵니다.
func listEnabledNotificationChannelsTx(ctx context.Context, tx *sql.Tx) ([]*NotificationChannel, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, kind, config, mode, filter, rate_per_min
FROM notification_channels WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*NotificationChannel{}
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.Config, &c.Mode, &c.Filter, &c.RatePerMin); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// NotificationAssetNames 푸시 메시지에 사용하기 위해 id 자산을 짧은 표시 이름으로 구문 분석합니다.
//
// 반환 순서는 입력 매개변수와 일치하며 길이는 입력 매개변수보다 작을 수 있습니다(존재하지 않는 id는 건너뜁니다). 입력 매개변수의 순서를 다음과 같이 유지합니다.
// 동일한 취약점 메시지의 자산 순서가 여러 번 전달될 때 안정적인지 확인하기 위해 - 그렇지 않으면 재시도 후 수신된 메시지에서
// 자산의 순서가 바뀌었다면 '자산이 바뀌었다'로 오해하게 됩니다.
func (d *DB) NotificationAssetNames(ctx context.Context, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := placeholders(1, ids)
	rows, err := d.QueryContext(ctx, `SELECT id, type, domain, ip, url, app_name, bundle_id FROM assets WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := map[int64]string{}
	for rows.Next() {
		var (
			id                int64
			typ               string
			domain, ip, url   sql.NullString
			appName, bundleID sql.NullString
		)
		if err := rows.Scan(&id, &typ, &domain, &ip, &url, &appName, &bundleID); err != nil {
			return nil, err
		}
		labels[id] = assetDisplayName(typ, domain.String, ip.String, url.String, appName.String, bundleID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if label, ok := labels[id]; ok && label != "" {
			out = append(out, label)
		}
	}
	return out, nil
}

// assetDisplayName 자산 유형별로 가장 인지도가 높은 로고를 선택하세요.
// 빈 문자열이 반환되고 호출자는 "이름을 검색할 수 없는 자산"을 표시하는 방법을 결정합니다. 이 함수는 자리 표시자를 생성하지 않습니다.
// 그렇지 않으면 "Asset #42" 같은 노이즈가 푸시 메시지에 섞여 독자들이 실제 도메인 이름이라고 생각할 것입니다.
func assetDisplayName(typ, domain, ip, url, appName, bundleID string) string {
	pick := func(vals ...string) string {
		for _, v := range vals {
			if strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
	switch typ {
	case "root_domain", "subdomain":
		return domain
	case "ip":
		return ip
	case "app":
		return pick(appName, bundleID)
	case "service", "endpoint":
		return pick(url, domain, ip)
	default:
		return pick(domain, ip, url, appName)
	}
}

// SetFindingStatusWithNotify는 취약점 처리 상태를 업데이트하고 동일한 트랜잭션에 상태 변경을 등록합니다.
// 푸시 이벤트.
//
// 변경 전 from=상태를 반환합니다. found=취약점이 존재하는지 여부; notified=이벤트 등록 성공 여부.
//
// 세 가지 의도적인 조치:
//   - 실제로 상태가 변경되지 않은 경우에는 이벤트가 등록되지 않습니다. 프런트엔드 서랍이 동일한 값이나 자동화된 스크립트를 반복적으로 제출합니다.
//     멱등성 재생은 푸시 노이즈를 생성해서는 안 됩니다.
//   - 취약점이 존재하지 않는 경우에는 found=false가 아무런 쓰기 없이 반환되며 호출자에 의해 404로 변환된다.
//   - 이벤트 등록 실패는 상태 업데이트에 영향을 미치지 않습니다(RecordNotificationEventTx의 저장 포인트 설명 참조).
//     따라서 notified=false일 때 상태가 성공적으로 변경된 것이므로 호출자는 이로 인해 오류를 보고해서는 안 됩니다.
func (d *DB) SetFindingStatusWithNotify(ctx context.Context, id int64, status string) (from string, found bool, notified bool, err error) {
	err = d.WithEvidenceTx(ctx, func(tx *sql.Tx) error {
		var txErr error
		from, found, _, notified, txErr = SetFindingStatusTx(ctx, tx, id, status)
		return txErr
	})
	return from, found, notified, err
}

// SetFindingStatusTx는 호출자의 트랜잭션 내의 취약점 상태를 업데이트하고 상태 변경 푸시 이벤트를 등록합니다.
//
// 트랜잭션 수준 함수를 추출하는 목적은 상태를 변경하는 모든 경로가 동일한 의미 집합을 공유하도록 만드는 것입니다. 이전에는
// patchFinding 버전의 테이프 전송 알림 및 **재테스트 결론은 "수정됨"**(finding_retests
// 여기에 있는 것(`UPDATE findings SET status=...`)은 라이브러리에 직접 기록되므로 구성됩니다.
// `on_status_change`의 채널은 이러한 유형의 상태 전송에 대해 푸시 알림을 전혀 받을 수 없습니다. 인터페이스의 상태가 조용히 변경되었습니다.
// 플랫폼이 열릴 때까지 운영 및 유지 관리는 발견되지 않습니다.
//
// Return from=변경 전 상태, found=취약점이 존재하는지 여부, changed=상태가 실제로 변경되었는지 여부,
// notified=이벤트가 성공적으로 등록되었는지 여부(등록 실패는 상태 업데이트에 영향을 미치지 않습니다. RecordNotificationEventTx 참조).
func SetFindingStatusTx(ctx context.Context, tx *sql.Tx, id int64, status string) (from string, found bool, changed bool, notified bool, err error) {
	var (
		vulnclass, name, severity, summary string
		taskID                             sql.NullInt64
		assetIDs                           []byte
	)
	scanErr := tx.QueryRowContext(ctx, `SELECT vulnclass, name, severity, summary, task_id, asset_ids, status
FROM findings WHERE id=$1 FOR UPDATE`, id).
		Scan(&vulnclass, &name, &severity, &summary, &taskID, &assetIDs, &from)
	if scanErr == sql.ErrNoRows {
		return "", false, false, false, nil
	}
	if scanErr != nil {
		return "", false, false, false, scanErr
	}
	found = true
	if from == status {
		// 상태가 실제로 변경되지 않은 경우 이벤트가 등록되지 않습니다. 동일한 값을 반복적으로 제출하고 멱등성 재생이 허용되지 않습니다.
		// 누르는 소음이 발생합니다.
		return from, true, false, false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE findings SET status=$2 WHERE id=$1`, id, status); err != nil {
		return from, true, false, false, err
	}
	var assets []int64
	_ = json.Unmarshal(assetIDs, &assets)
	notified = RecordNotificationEventTx(ctx, tx, notify.EventFindingStatusChanged, id, notify.Snapshot{
		Kind:       notify.EventFindingStatusChanged,
		FindingID:  id,
		TaskID:     taskID.Int64,
		VulnClass:  vulnclass,
		Name:       name,
		Severity:   severity,
		Summary:    summary,
		AssetIDs:   assets,
		FromStatus: from,
		ToStatus:   status,
	})
	return from, true, true, notified, nil
}

// NotificationStats는 알림 페이지 상단의 개요 개수입니다.
type NotificationStats struct {
	Channels     int   `json:"channels"`
	ChannelsOn   int   `json:"channels_on"`
	Pending      int   `json:"pending"`
	Failed       int   `json:"failed"`
	SentToday    int   `json:"sent_today"`
	BacklogAgeMS int64 `json:"backlog_age_ms"` // 가장 오래된 보류 중인 배달 이후 경과된 시간(밀리초)
}

// NotificationStatsSnapshot는 알림 시스템의 상태를 요약합니다.
// BacklogAgeMS는 "푸시가 멈췄는지 여부"를 가장 직접적으로 나타내는 지표로, pending 카운트보다 훨씬 더 유용합니다.
// 왜냐하면 3개 항목의 백로그와 3개 항목의 백로그의 차이는 3초에서 3시간까지 될 수 있기 때문입니다.
func (d *DB) NotificationStatsSnapshot(ctx context.Context) (*NotificationStats, error) {
	var s NotificationStats
	if err := d.QueryRowContext(ctx, `SELECT
    (SELECT count(*) FROM notification_channels),
    (SELECT count(*) FROM notification_channels WHERE enabled),
    (SELECT count(*) FROM notification_deliveries WHERE state IN ($1,$2)),
    (SELECT count(*) FROM notification_deliveries WHERE state=$3),
    (SELECT count(*) FROM notification_deliveries WHERE state=$4 AND sent_at >= date_trunc('day', now())),
    COALESCE((SELECT EXTRACT(EPOCH FROM (now() - min(created_at))) * 1000 FROM notification_deliveries WHERE state=$1), 0)::bigint`,
		NotifyStatePending, NotifyStateSending, NotifyStateFailed, NotifyStateSent).
		Scan(&s.Channels, &s.ChannelsOn, &s.Pending, &s.Failed, &s.SentToday, &s.BacklogAgeMS); err != nil {
		return nil, err
	}
	return &s, nil
}

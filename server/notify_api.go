package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// 이 파일은 푸시 기능의 HTTP 인터페이스입니다. 모든 경로는 requireAuth 이후에 중단됩니다(Handler() 참조).
// 다른 관리 인터페이스와 일관됩니다.

// notifyChannelDTO는 채널의 외부 표현입니다.
//
// Config는 **마스크** 구성입니다. 자격 증명 필드는 notify.MaskedPrefix로 시작하는 값으로 대체됩니다.
// 프런트 엔드에서 마스크 값을 그대로 다시 제출하면 "이 필드는 변경되지 않았습니다"라는 의미이며 서버는 그에 따라 원래 값을 라이브러리에 유지합니다.
// (notify.MergeConfig 참조).
type notifyChannelDTO struct {
	ID         int64          `json:"id"`
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	Enabled    bool           `json:"enabled"`
	Mode       string         `json:"mode"`
	Config     map[string]any `json:"config"`
	Filter     notify.Filter  `json:"filter"`
	RatePerMin int            `json:"rate_per_min"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	// SecretKeys는 어떤 필드가 자격 증명인지 프런트 엔드에 알려주고 그에 따라 비밀번호 상자와 "비워두고 변경하지 마세요"라는 프롬프트를 렌더링합니다.
	// 채널 자체(notify.Channel.SecretKeys)에서 선언된 프런트 엔드는 채널 지식을 하드 코딩하지 않습니다.
	SecretKeys []string `json:"secret_keys"`
}

// notifyDeliveryDTO는 배송 내역의 외부 표현입니다.
type notifyDeliveryDTO struct {
	ID          int64      `json:"id"`
	FindingID   int64      `json:"finding_id,string"`
	EventKind   string     `json:"event_kind"`
	ChannelID   int64      `json:"channel_id"`
	ChannelName string     `json:"channel_name"`
	ChannelKind string     `json:"channel_kind"`
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error"`
	BatchID     *int64     `json:"batch_id,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	SentAt      *time.Time `json:"sent_at,omitempty"`
	NextAttempt time.Time  `json:"next_attempt_at"`
	// 메시지 제목 요약을 사용하면 기록 목록을 확장하지 않고도 이 트윗이 무엇인지 확인할 수 있습니다.
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

func toNotifyChannelDTO(ch *db.NotificationChannel) notifyChannelDTO {
	var cfg map[string]any
	if len(ch.Config) > 0 {
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	secrets := []string{}
	if c, ok := notify.Get(ch.Kind); ok {
		secrets = c.SecretKeys()
	}
	return notifyChannelDTO{
		ID:         ch.ID,
		Name:       ch.Name,
		Kind:       ch.Kind,
		Enabled:    ch.IsEnabled(),
		Mode:       ch.Mode,
		Config:     notify.MaskConfig(ch.Kind, cfg),
		Filter:     notify.ParseFilter(ch.Filter),
		RatePerMin: ch.RatePerMin,
		CreatedAt:  ch.CreatedAt,
		UpdatedAt:  ch.UpdatedAt,
		SecretKeys: secrets,
	}
}

func toNotifyDeliveryDTO(dl *db.NotificationDelivery) notifyDeliveryDTO {
	snap, _ := parseSnapshot(dl)
	dto := notifyDeliveryDTO{
		ID:          dl.ID,
		FindingID:   dl.FindingID,
		EventKind:   dl.EventKind,
		ChannelID:   dl.ChannelID,
		ChannelName: dl.ChannelName,
		ChannelKind: dl.ChannelKind,
		State:       dl.State,
		Attempts:    dl.Attempts,
		LastError:   dl.LastError,
		BatchID:     dl.BatchID,
		CreatedAt:   dl.CreatedAt,
		SentAt:      dl.SentAt,
		NextAttempt: dl.NextAttemptAt,
		Severity:    snap.Severity,
	}
	if snap.Name != "" {
		dto.Title = snap.Name
	} else {
		dto.Title = snap.VulnClass
	}
	return dto
}

// notifyMeta는 알림 페이지에 필요한 정적 메타데이터와 전역 설정을 반환하고 이를 모두 한 번의 요청으로 가져옵니다.
// 드롭다운 상자를 렌더링하기 위해 프런트 엔드에서 3개의 요청을 보내지 마세요.
func (s *Server) notifyMeta(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	kinds := make([]map[string]any, 0, len(notify.Kinds()))
	for _, k := range notify.Kinds() {
		ch, _ := notify.Get(k)
		kinds = append(kinds, map[string]any{
			"kind":                 k,
			"default_rate_per_min": ch.DefaultRatePerMin(),
			"secret_keys":          ch.SecretKeys(),
		})
	}
	baseURL, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	digest, _, _ := pg.GetSetting(settingNotifyDigestMinutes)
	stats, err := pg.NotificationStatsSnapshot(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"kinds":               kinds,
		"enabled":             pg.GetBool(settingNotifyEnabled, true),
		"public_base_url":     baseURL,
		"digest_interval_min": digest,
		"defaults": map[string]any{
			"digest_interval_min": notifyDefaultDigestMinutes,
		},
		"stats": stats,
	})
}

func (s *Server) notifyListChannels(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	channels, err := pg.ListNotificationChannels(r.Context())
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]notifyChannelDTO, 0, len(channels))
	for _, ch := range channels {
		out = append(out, toNotifyChannelDTO(ch))
	}
	writeJSON(w, 200, map[string]any{"channels": out})
}

// notifyChannelRequest는 신규/업데이트된 채널의 요청 본문입니다.
//
// 모든 비즈니스 필드는 포인터를 사용하여 "통과되지 않음"과 "0 값이 전달됨"을 구별합니다. PATCH 의미 체계,
// 전달되지 않은 필드는 데이터베이스에 원래 값을 유지해야 합니다.
type notifyChannelRequest struct {
	Name       *string        `json:"name"`
	Kind       *string        `json:"kind"`
	Enabled    *bool          `json:"enabled"`
	Mode       *string        `json:"mode"`
	Config     map[string]any `json:"config"`
	Filter     *notify.Filter `json:"filter"`
	RatePerMin *int           `json:"rate_per_min"`
}

func (s *Server) notifyCreateChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req notifyChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "요청 본문이 유효하지 않습니다. JSON: "+err.Error())
		return
	}
	if req.Kind == nil || !notify.ValidKind(*req.Kind) {
		writeErr(w, 400, fmt.Sprintf("잘못된 채널 유형, 선택 사항: %s", strings.Join(notify.Kinds(), " / ")))
		return
	}
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if name == "" {
		writeErr(w, 400, "채널 이름이 누락되었습니다.")
		return
	}
	channel, _ := notify.Get(*req.Kind)
	if err := channel.Validate(req.Config); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ch := &db.NotificationChannel{
		Name:       name,
		Kind:       *req.Kind,
		Enabled:    req.Enabled,
		Mode:       db.NotifyModeRealtime,
		RatePerMin: channel.DefaultRatePerMin(),
	}
	if req.Mode != nil {
		if !db.ValidNotifyMode(*req.Mode) {
			writeErr(w, 400, "푸시 모드가 유효하지 않습니다. 선택 사항: realtime / digest")
			return
		}
		ch.Mode = *req.Mode
	}
	if req.RatePerMin != nil {
		// 값이 명시적으로 제공되면 "현재 제한 없음"을 의미하고 합법적인 구성인 0을 포함하여 해당 값이 사용됩니다.
		if *req.RatePerMin < 0 {
			writeErr(w, 400, "전류 제한 값은 음수일 수 없습니다.")
			return
		}
		ch.RatePerMin = *req.RatePerMin
	}
	// "필드 기본값"만 채널 기본값을 적용합니다. 기본값은 db 레이어가 아니라 여기에서 결정되어야 합니다.
	// 요청 본문만이 "이 필드가 전달되지 않음"과 "0이 명시적으로 전달됨"을 구별할 수 있으며 둘의 의미는 완전히 다릅니다.
	// (전자 = 기본값 사용, 후자 = 현재 제한 없음) db 레이어는 0을 지정되지 않은 것으로 처리하므로 무제한 구성에 도달할 수 없습니다.
	if req.RatePerMin == nil {
		ch.RatePerMin = channel.DefaultRatePerMin()
	}
	if req.Filter != nil {
		// 쓰기 시 제한된 값으로 필터 필드를 확인합니다(예: min_severity). 자세한 내용은 notify.Filter.Validate를 참조하세요.
		// 임계값에 오타가 있으면 필터가 자동으로 실패하고 전체 푸시가 되어 입구에서 차단되어야 합니다.
		if err := req.Filter.Validate(); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		raw, _ := json.Marshal(req.Filter)
		ch.Filter = raw
	}
	rawCfg, _ := json.Marshal(req.Config)
	ch.Config = rawCfg

	id, err := pg.SaveNotificationChannel(r.Context(), ch)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) notifyUpdateChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "채널 id가 잘못되었습니다.")
		return
	}
	current, err := pg.NotificationChannelByID(r.Context(), id)
	if err != nil {
		notifyChannelLookupErr(w, err)
		return
	}
	var req notifyChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "요청 본문이 유효하지 않습니다. JSON: "+err.Error())
		return
	}

	// kind에서는 수정이 허용되지만 유형을 변경하면 전체 자격 증명 필드 세트가 바뀌며 이전 구성과 병합될 수 없습니다.
	kind := current.Kind
	if req.Kind != nil {
		if !notify.ValidKind(*req.Kind) {
			writeErr(w, 400, fmt.Sprintf("잘못된 채널 유형, 선택 사항: %s", strings.Join(notify.Kinds(), " / ")))
			return
		}
		kind = *req.Kind
	}
	channel, _ := notify.Get(kind)

	var stored map[string]any
	if kind == current.Kind {
		if len(current.Config) > 0 {
			_ = json.Unmarshal(current.Config, &stored)
		}
	}
	if stored == nil {
		stored = map[string]any{}
	}
	// 기본 MergeConfig 대신 PrepareConfigUpdate 사용: 대상 주소가 변경되면 운영자에게 알려야 합니다.
	// 자격 증명 필드를 다시 지정합니다. 그렇지 않으면 "주소만 변경하고 자격 증명은 유지"하면 라이브러리의 실제 자격 증명이 새 주소로 전송됩니다.
	merged, err := notify.PrepareConfigUpdate(kind, stored, req.Config)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := channel.Validate(merged); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	rawCfg, _ := json.Marshal(merged)

	ch := &db.NotificationChannel{
		ID:         id,
		Name:       current.Name,
		Kind:       kind,
		Enabled:    current.Enabled,
		Mode:       current.Mode,
		Config:     rawCfg,
		Filter:     current.Filter,
		RatePerMin: current.RatePerMin,
	}
	if req.Name != nil {
		if ch.Name = strings.TrimSpace(*req.Name); ch.Name == "" {
			writeErr(w, 400, "채널 이름은 비워둘 수 없습니다.")
			return
		}
	}
	if req.Enabled != nil {
		ch.Enabled = req.Enabled
	}
	if req.Mode != nil {
		if !db.ValidNotifyMode(*req.Mode) {
			writeErr(w, 400, "푸시 모드가 유효하지 않습니다. 선택 사항: realtime / digest")
			return
		}
		ch.Mode = *req.Mode
	}
	if req.RatePerMin != nil {
		if *req.RatePerMin < 0 {
			writeErr(w, 400, "전류 제한 값은 음수일 수 없습니다.")
			return
		}
		ch.RatePerMin = *req.RatePerMin
	}
	if req.Filter != nil {
		if err := req.Filter.Validate(); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		raw, _ := json.Marshal(req.Filter)
		ch.Filter = raw
	}

	// SaveNotificationChannel 대신 SetNotificationChannelEnabled의 경로를 선택하세요.
	// 이는 "비활성화"를 통해 배송 대기 중인 재고를 동시에 skipped로 표시하여 재활성화 시 오류가 발생하지 않도록 하기 위한 것입니다.
	// 오래된 메시지의 백로그입니다.
	enabledChanged := ch.Enabled != nil && current.Enabled != nil && *ch.Enabled != *current.Enabled
	if enabledChanged {
		// 먼저 구성을 업데이트하고 라이브러리에 놓습니다(이때 enabled는 건너뛰기 논리가 미리 트리거되는 것을 방지하기 위해 이전 값을 사용합니다).
		// 그런 다음 스위치를 별도로 끄십시오. 두 단계 사이에는 동시성 창이 없습니다. 이 인터페이스는 이 두 필드를 변경하는 유일한 항목입니다.
		prev := ch.Enabled
		ch.Enabled = current.Enabled
		if _, err := pg.SaveNotificationChannel(r.Context(), ch); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if err := pg.SetNotificationChannelEnabled(r.Context(), id, *prev); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"id": id})
		return
	}
	if _, err := pg.SaveNotificationChannel(r.Context(), ch); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"id": id})
}

func (s *Server) notifyDeleteChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "채널 id가 잘못되었습니다.")
		return
	}
	if err := pg.DeleteNotificationChannel(r.Context(), id); err != nil {
		notifyChannelLookupErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// notifyTestChannel 현재 저장된 구성을 사용하여 테스트 메시지를 보냅니다.
//
// 전달 대기열을 거치지 않고 Send 채널을 직접 호출합니다. 테스트 목적은 사용자에게 "이 구성이 가능합니까?"라고 즉시 알리는 것입니다.
// "보내기"를 선택하면 대기열은 전송 내역에서 결과를 숨기며 사용자는 성공 여부를 확인하기 위해 다시 확인해야 합니다.
// 따라서 이 인터페이스는 **동기식**이며 시간 초과의 상한은 notify 패키지의 HTTP 클라이언트에 의해 결정됩니다(15초).
func (s *Server) notifyTestChannel(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "채널 id가 잘못되었습니다.")
		return
	}
	ch, err := pg.NotificationChannelByID(r.Context(), id)
	if err != nil {
		notifyChannelLookupErr(w, err)
		return
	}
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		writeErr(w, 400, fmt.Sprintf("채널 유형 %q 등록되지 않음", ch.Kind))
		return
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if err := channel.Validate(cfg); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	msg := notifyTestMessage(s.notifierBaseURL(pg))
	start := time.Now()
	// 테스트 메시지는 하나만 있으며 여기서 전달 메시지 수는 필요하지 않습니다. 채널 길이의 상한은 단일 메시지에 대한 것입니다.
	// 하단을 자르면 분할이 발생하지 않습니다.)
	if _, err := channel.Send(r.Context(), cfg, msg); err != nil {
		// 채널에서 반환된 원래 오류를 사용자에게 충실하게 반환합니다. 이는 구성을 디버깅할 수 있는 유일한 단서입니다.
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"ok":         true,
		"latency_ms": time.Since(start).Milliseconds(),
	})
}

// notifyTestMessage는 테스트 메시지를 구성합니다. 한눈에 알 수 있는 내용을 의도적으로 테스트로 활용하세요.
// 수신자는 이를 실제 취약점으로 착각해서는 안 됩니다.
func notifyTestMessage(baseURL string) notify.Message {
	return notify.Message{
		Items: []notify.Item{{
			FindingID: 0,
			Name:      "테스트 메시지 · 채널 구성이 정상입니다",
			VulnClass: "연결 테스트",
			Severity:  "low",
			Summary:   "ARTEX 푸시 채널에 대한 테스트 메시지입니다. 이를 수신한다는 것은 채널 구성이 가능하다는 의미입니다.",
			Assets:    []string{"artex.example.com"},
			DetailURL: baseURL,
		}},
		HomeURL: baseURL,
	}
}

// notifierBaseURL 링크를 다시 읽는 데 사용되는 외부 주소입니다.
func (s *Server) notifierBaseURL(pg *db.DB) string {
	v, _, _ := pg.GetSetting(settingNotifyPublicBaseURL)
	return trimTrailingSlash(v)
}

func (s *Server) notifyListDeliveries(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	f := db.NotificationDeliveryFilter{
		State:     r.URL.Query().Get("state"),
		EventKind: r.URL.Query().Get("event_kind"),
	}
	if v := r.URL.Query().Get("channel_id"); v != "" {
		f.ChannelID = int64(atoiDefault(v, 0))
	}
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 50)
	items, total, err := pg.ListNotificationDeliveries(r.Context(), f, page, pageSize)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	out := make([]notifyDeliveryDTO, 0, len(items))
	for _, dl := range items {
		out = append(out, toNotifyDeliveryDTO(dl))
	}
	writeJSON(w, 200, map[string]any{"deliveries": out, "total": total, "page": page, "page_size": pageSize})
}

func (s *Server) notifyRetryDelivery(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "배송 id가 유효하지 않습니다.")
		return
	}
	if err := pg.RetryNotificationDelivery(r.Context(), id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// notifyChannelLookupErr는 "채널이 존재하지 않습니다"를 404로 변환하고 기타 오류는 500입니다.
func notifyChannelLookupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrNotificationChannelNotFound) {
		writeErr(w, 404, "알림 채널이 존재하지 않습니다")
		return
	}
	writeErr(w, 500, err.Error())
}

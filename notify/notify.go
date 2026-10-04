// Package notify는 취약점 검색 IM/이메일 푸시 채널 적응 계층을 구현합니다.
//
// 계층화: 이 패키지는 **리프 패키지**이며 표준 라이브러리에만 의존합니다. 데이터베이스도 모르고 server도 모릅니다. 채널 구성
// map[string]any(JSONB 열 notification_channels.config에 해당)를 전달하고,
// 푸시할 콘텐츠는 Message로 전달됩니다. 이러한 분리의 장점은 서명 계산, UTF-8 잘림, 필터링 및 일치 등입니다.
// 실제로 오류가 발생하기 쉬운 영역은 PostgreSQL 단일 테스트에서 분리할 수 있으며, 호스트는 server 측에서만 오케스트레이션을 수행하면 됩니다.
//
// 동시성 계약: Channel의 구현은 **상태 비저장**이어야 합니다. 동일한 Channel 인스턴스가 여러 채널로 구성됩니다.
// (동일한 채널에 있는 여러 로봇 인스턴스라도) 동시 재사용, 모든 자격 증명은 cfg 매개 변수에서 전달됩니다.
// webhook URL와 같은 항목을 자체적으로 구현하는 필드에 캐시하는 것은 허용되지 않습니다.
package notify

// 채널 유형 식별자입니다. 값은 server 옆에 있는 notification_channels.kind의 합법적인 집합이기도 합니다.
// 화이트리스트 확인(findings.status와 동일, 후속 채널 추가를 용이하게 하기 위해 DB CHECK가 필요하지 않음).
const (
	KindDingTalk = "dingtalk" // DingTalk 맞춤형 로봇
	KindFeishu   = "feishu"   // Feishu(Lark 포함) 맞춤형 로봇
	KindWeCom    = "wecom"    // 엔터프라이즈 WeChat 그룹 로봇
	KindWebhook  = "webhook"  // 일반 Webhook: 사용자 정의 방법/헤더/JSON 템플릿
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP 이메일
)

// notification_events.kind에 해당하는 이벤트 유형입니다.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind는 config에 비어 있는 kind의 하위 값입니다.
const InitKind = KindDingTalk

// severityRank는 취약성 수준을 비슷한 서수로 매핑합니다. 알 수 없는 수준은 0을 반환하므로
// min_severity 설정은 알 수 없는 수준을 유지합니다. 잘못된 긍정 및 화면 스와이프를 방지하려면 의심스러운 경우 누르지 마십시오.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank는 레벨의 서수를 반환합니다. 알 수 없는 수준의 경우 0이 반환됩니다.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel는 메시지 제목과 카드 색상에 사용되는 emoji를 포함하는 한국어 레벨 이름을 반환합니다.
// 알 수 없는 레벨은 구성되지 않고 그대로 에코됩니다.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 심각한"
	case "high":
		return "🟠 높은 위험"
	case "medium":
		return "🟡 중간 위험"
	case "low":
		return "🔵 낮은 위험"
	default:
		return severity
	}
}

// StatusLabel 상태 변경 메시지에 사용하기 위해 처리 상태를 한국어로 변환합니다.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "처리 대기"
	case "in_progress":
		return "처리 중"
	case "confirmed":
		return "확인됨"
	case "resolved":
		return "처리됨"
	case "fixed":
		return "수정됨"
	case "false_positive":
		return "오탐"
	case "ignored":
		return "무시"
	case "duplicate":
		return "중복"
	case "risk_accepted":
		return "위험 수용"
	default:
		return status
	}
}

// AtLeast는 severity가 min 임계값에 도달하는지 여부를 확인합니다. min가 비어 있으면 임계값이 없으며 모두 통과한다는 의미입니다.
// 알 수 없는 severity의 서수는 0이며 비어 있지 않은 min에 의해 거부됩니다(severityRank 설명 참조).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}

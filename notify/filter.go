package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter는 JSONB 열 notification_channels.filter의 계약입니다: 채널 인스턴스의 필터 조건입니다.
// 모든 필드는 선택 사항이며 기본값은 "필터링 없음"입니다. 이는 잘못된 구성의 숨겨진 의미입니다. ParseFilter를 참조하세요.
type Filter struct {
	// MinSeverity는 가장 낮은 수준의 임계값(low/medium/high/critical)이며, 비어 있으면 임계값이 없습니다.
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs 배열이 비어 있으면 제한이 없음을 의미합니다. 비어 있지 않으면 이벤트가 교차해야 합니다.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude가 비어 있으면 모두 수신되었음을 의미합니다. 비어 있지 않은 경우 키워드를 누르려면 vulnclass가 필요합니다.
	// VulnClassExclude는 키워드가 히트되면 제외됩니다(포함보다 제외가 우선).
	// 일치 방법은 대소문자를 구분하지 않는 하위 문자열입니다. 정규 표현식보다 안전합니다. 정규 표현식이 일치하지 않는 사용자는 채널이 자동으로 실패하지 않도록 합니다.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange는 이 채널이 취약점 상태 변경 이벤트를 수신하는지 여부를 결정합니다(realtime 모드만 의미 있음).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter는 채널 필터링 구성을 구문 분석합니다.
//
// **error를 반환하지 않습니다. ** 이는 의도적인 설계 선택입니다. 필터 조건은 잘못 구성되면 항상 0으로 저하됩니다.
// Filter(= 필터링 없음 = 모든 적중), 취약점 알림 시스템의 경우 하나 더 푸시하는 것이 훨씬 낫기 때문입니다.
// 고위험 비밀을 조용히 놓쳤습니다. 구문 분석 실패를 "푸시 안 함"으로 바꾸는 것은 사용자에게 준비된 것처럼 보이는 메시지를 제공하는 것과 같습니다.
// 실제로 아무것도 푸시하지 않는 채널 – 이는 최악의 실패 모드입니다.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// 구문 분석이 실패하면 f는 0으로 유지됩니다. 즉, 필터링이 수행되지 않습니다.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity는 s가 적법한 수준 임계값인지 여부를 보고합니다(빈 문자열은 임계값이 없음을 나타냄).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate 채널 저장 시 호출될 수 있는 검증필터링 설정에서 **제한값** 필드를 확인하세요.
//
// 쓸 때 차단해야 하는 이유: Match 알 수 없는 임계값의 판단은 `rank >= 0`이며 항상 true입니다.
// 즉, min_severity가 오타("hgih")인 경우 필터는 **조용히 실패**하고
// "모두 밀어 넣기". 이는 "놓치느니 차라리 밀어붙이는 편이 낫다"(누출 없음)라는 이 패키지의 방향과 일치하며,
// 그러나 결과적으로 사용자는 계층적 푸시를 수행한다고 생각하지만 실제로는 모든 취약점을 그룹에 쏟아 붓습니다.
// 그리고 그가 일치하지 않는다는 것을 암시하는 것도 없었습니다. 이런 '조용한 타락'은 입구에서 멈춰야 한다.
//
// Validate는 **쓰기** 경로에만 사용됩니다. 읽기 경로는 여전히 ParseFilter의 허용 의미를 따릅니다.
// 이렇게 하면 과거 데이터에 이미 존재하는 잘못된 값으로 인해 채널을 읽을 수 없게 되는 일이 발생하지 않습니다.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("가장 낮은 수준의 %q는 유효하지 않습니다. 선택 사항: low / medium / high / critical 또는 제한이 없음을 나타내려면 공백으로 남겨두세요.", f.MinSeverity)
	}
	return nil
}

// Match 이 필터 조건을 사용하여 이벤트를 채널에 전달해야 하는지 여부를 결정합니다.
//
// **error를 반환하지 마세요**, 이유는 ParseFilter와 동일합니다. 모든 내부 예외는 "적중"으로 처리됩니다.
// 판단 순서: 이벤트 유형 → 수준 임계값 → 작업/자산 범위 → 취약점 유형 키워드.
func Match(f Filter, s Snapshot) bool {
	// 명시적으로 열린 채널만 상태 변경 이벤트를 수신합니다. 대부분의 사용자는 기본적으로 꺼져 있습니다.
	// "푸시"는 실행 중인 계정의 각 상태 흐름에 대한 후속 조치가 아닌 "새로운 취약점 발견"을 의미할 것으로 예상됩니다.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// 제외 우선순위: 제외 키워드가 하나라도 적중되면 포함 목록에도 적중되더라도 제외됩니다.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// 작은 세트는 선형적으로 스캔할 수 있습니다. 양쪽의 크기는 "수십 개의 인간이 만든 수표"입니다.
	// map를 구축하는 데 드는 비용은 이점보다 큽니다.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold는 s에 keywords에 키워드가 포함되어 있는지 여부를 보고합니다(대소문자 구분 안 함).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

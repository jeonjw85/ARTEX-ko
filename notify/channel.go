package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel는 알림 채널 어댑터입니다. 구현은 **상태 비저장**이어야 합니다. 동일한 인스턴스가 여러 채널에서 사용됩니다.
// 동시 멀티플렉싱을 구성하면 자격 증명은 항상 cfg 매개 변수에서 전달됩니다.
type Channel interface {
	// Kind 레지스트리의 키와 일치해야 하는 채널 유형 식별자를 반환합니다.
	Kind() string
	// 필수 필드와 형식을 확인하기 위해 구성을 저장할 때 Validate가 호출됩니다. 반환된 오류는 다음에 직접 표시됩니다.
	// 구성자이므로 사본에서는 일반적인 "구성이 잘못되었습니다."가 아니라 "어떤 필드가 누락되었는지"를 설명해야 합니다.
	Validate(cfg map[string]any) error
	// Send는 메시지를 한 번 전달하고 실제 전달된 항목 수와 오류를 반환합니다.
	//
	// 항목 수가 반환되어야 하는 이유: 각 플랫폼에는 최대 메시지 길이 제한이 있으며 요약 메시지가 전체 배치에 맞지 않으면 잘립니다.
	// 호출자가 무조건 전체 배치를 전달된 것으로 표시하면 잘린 항목이 사라지고 메시지에 표시되지 않습니다.
	// 전송 내역에도 성공이 표시되며, 전송되지 않은 곳에서는 취약점이 발견되지 않습니다. kept를 반환한 후,
	// 호출자는 첫 번째 kept 항목만 표시하고 나머지는 다음 배치를 위해 남겨 둡니다.
	//
	// 오류 반환은 배달 실패를 의미하며, 여기서 *PermanentError는 재시도를 수행해서는 안 됨을 나타냅니다.
	// 실패 시 kept는 의미가 없으며 호출자는 무시해야 합니다.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin는 채널의 공식 권장 분당 전송 제한을 새 채널 인스턴스로 반환합니다.
	// 당시의 기본 전류 제한 값입니다. 알려진 제한이 없으면 0을 반환합니다.
	DefaultRatePerMin() int
	// SecretKeys는 채널 구성의 자격 증명에 속하는 키 이름을 반환합니다. API 이 키의 값은 에코될 때 마스크됩니다.
	// 업데이트 중에 마스크 값을 수신하면 라이브러리의 원래 값이 유지됩니다. 구현에서만 어떤 필드가 자격 증명으로 간주되는지 알 수 있습니다.
	// (Enterprise WeChat의 전체 Webhook 주소는 자격 증명이며 DingTalk는 secret뿐입니다.)
	// 따라서 이러한 지식은 채널을 통해 제공되어야 하며 상위 경영진이 추측할 수 없습니다.
	SecretKeys() []string
	// DestinationKeys는 채널 구성에서 "메시지가 전송되는 위치"를 결정하는 키 이름을 반환합니다.
	//
	// SecretKeys와 마찬가지로 보안과 관련된 것입니다. 대상 주소와 자격 증명은 두 개의 독립적인 필드 세트입니다.
	// "주소만 변경하고 자격 증명은 그대로 유지"를 허용하면 채널 구성을 변경할 수 있는 사람은 누구나 라이브러리에서 실제 자격 증명을 변경할 수 있습니다.
	// 자신이 제어하는 ​​서버로 전송되면 채널에 구성된 마스크는 전혀 의미가 없게 됩니다.
	// 자세한 내용은 PrepareConfigUpdate를 참조하세요.
	DestinationKeys() []string
}

// registry는 채널 등록 양식입니다. 자체 등록을 위해 init() 대신 명시적 리터럴을 의도적으로 사용합니다. "어떤 채널이 있습니까?"
// 한 곳에서 모든 것을 볼 수 있으며, 새로운 채널은 런타임 부작용에 의존하지 않고 컴파일 타임에 누락된 부분을 노출합니다.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get는 종류와 채널에 따라 구현됩니다.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind는 kind가 지원되는 채널 유형인지 여부를 보고합니다.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds는 지원되는 모든 채널 유형을 사전 순서로 정렬하여 반환합니다(UI에 의한 안정적인 표시를 위해).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError는 잘못된 자격 증명, 대상 거부, 잘못된 요청 본문 등 재시도해서는 안되는 배달 실패를 표시합니다.
// 재시도는 일시적인 오류(네트워크 지터, 전류 제한, 피어 5xx)에만 의미가 있습니다. 영구적인 실패의 경우 반복적으로 물러났다가 다시 시도하십시오.
// 성공하지 못하며 실제 오류는 재시도 로그에 기록됩니다.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent는 err를 영구적으로 실패한 것으로 표시합니다. err가 nil이면 nil가 반환됩니다.
// `return Permanent(someCheck())`로 작성하시면 편리합니다.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent는 err 체인이 영구 오류로 표시되는지 보고합니다.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- 구성 읽기 도우미 ----
//
// 채널 구성은 데이터베이스의 JSONB 열에서 나옵니다. encoding/json에 의해 역직렬화되면 map[string]any가 됩니다.
// 숫자 값은 항상 float64이고 배열은 []any입니다. 다음 helper는 이 변환 계층을 통합하고 사용자를 허용합니다.
// UI를 공백으로 두면(예: 포트를 문자열로 채움) 유형 편차가 발생합니다.

// cfgString는 문자열 구성 항목을 가져오고 앞뒤 공백이 잘립니다. 웹 양식에서 복사하여 붙여넣기가 쉽습니다.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt는 float64(JSON 기본값) 및 문자열의 두 가지 소스와 호환되는 정수 구성 항목입니다.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool는 부울 구성 항목을 사용하며 "true"/"1" 문자열과 호환됩니다.
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings는 문자열 배열 구성 항목을 가져와 자동으로 공백을 자르고 빈 문자열을 삭제합니다.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// 또한 값이 하나만 있는 경우 양식 제출을 용이하게 하기 위해 단일 문자열을 허용합니다.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap는 문자열 매핑 구성 항목(예: 사용자 정의 HTTP 헤더)을 가져와 모든 키 값을 자르고 빈 키를 삭제합니다.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}

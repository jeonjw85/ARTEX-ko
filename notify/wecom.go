package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit는 Qiweiqun 로봇 markdown content의 하드 상한(바이트, 비문자)입니다.
// 이는 6개 채널 모두에서 가장 엄격한 제한 사항이자 TruncateBytes가 존재하는 주된 이유입니다.
const weComMarkdownLimit = 4096

// weComChannel는 기업용 WeChat 그룹 로봇을 구현합니다.
//
// 플랫폼 기능:
//   - 유일한 인증은 서명을 지원하지 않는 URL의 key입니다. 따라서 webhook 주소 자체가 전체 자격 증명입니다.
//   - markdown content의 상한은 4096 **바이트**입니다. 길이가 너무 길면 전체 메시지가 거부됩니다(잘리지 않음). 한글 3바이트/워드,
//     이는 텍스트에 쓸 단어가 1,000개가 넘고 클라이언트가 이를 잘라야 함을 의미합니다.
//   - 현재 제한은 분당 20개 메시지이며, 이는 클라이언트의 현재 제한에 의해 제어됩니다.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// Enterprise WeChat에는 Webhook(URL의 key)라는 하나의 자격 증명만 있으며 서명을 지원하지 않습니다.
// 전체 주소는 전체 자격 증명이므로 다른 필드를 마스킹할 필요가 없습니다.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// Qiwei에는 대상이자 자격 증명인 Webhook라는 하나의 필드만 있으므로 "주소 변경 후 남은 자격 증명"이 없습니다.
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Webhook 주소 누락")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 잘못된 주소: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// 요약 배치는 매우 길 수 있으며(50개 항목 × 항목당 한 줄 + 접두사) 4096바이트를 쉽게 초과할 수 있습니다.
	// 오류를 보고하기 위해 플랫폼에 의존하는 대신 여기서 잘림이 수행됩니다. 거부는 전체 배치가 손실됨을 의미하고 잘림은 최소한 처음 몇 개의 항목이 전달됨을 의미합니다.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Enterprise WeChat 응답을 구문 분석하지 못했습니다: %w(%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009는 인터페이스 호출이 제한을 초과했음을 의미합니다. 플랫폼의 현재 제한 창이 스크롤되고 백오프 후 재시도가 적용됩니다.
		// 따라서 명시적으로 재시도 가능으로 분류됩니다. 여기에 오면 클라이언트 rate_per_min가 너무 공격적이라는 것을 알 수 있습니다.
		// 재시도는 단지 은폐일 뿐입니다. 실제 해결 방법은 채널의 전류 제한을 낮추는 것입니다.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("엔터프라이즈 WeChat 현재 제한 %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 예 webhook key 유효하지 않음 - 영구적인 실패로 재시도가 저절로 복구되지 않습니다.
		return 0, Permanent(fmt.Errorf("Enterprise WeChat에서 오류 %d: %s를 반환합니다.", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

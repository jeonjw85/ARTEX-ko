package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// feishuChannel는 Feishu(Lark 포함) 맞춤형 로봇을 구현하여 대화형 카드를 실행합니다.
//
// 플랫폼 기능:
//   - 서명 알고리즘은 딩톡과 **다르며** 실수하기 쉽습니다. feishuSign 설명을 참조하세요.
//   - DingTalk와 마찬가지로 HTTP 200(code != 0)의 body에 비즈니스 오류를 넣습니다.
//   - 카드 header는 색상 템플릿을 지원하고 레벨 매핑을 사용하여 색상을 일치시켜 사람들이 메시지 목록에서 심각도를 한눈에 확인할 수 있도록 합니다.
type feishuChannel struct{}

func (feishuChannel) Kind() string { return KindFeishu }

// Feishu는 초당 약 5회, 즉 분당 100회에 해당하는 속도로 로봇을 맞춤 설정합니다.
func (feishuChannel) DefaultRatePerMin() int { return 100 }

// Webhook 주소의 마지막 세그먼트는 로봇의 고유 식별자이며 자격 증명입니다.
func (feishuChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// 마찬가지로 Webhook 주소를 변경하려면 새 주소에 대한 서명 키를 다시 선언해야 합니다.
func (feishuChannel) DestinationKeys() []string { return []string{"webhook"} }

func (feishuChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Webhook 주소 누락")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 잘못된 주소: %w", err)
	}
	return nil
}

func (c feishuChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	card, kept := feishuCard(m)
	payload := map[string]any{
		"msg_type": "interactive",
		"card":     card,
	}
	// 서명 매개변수는 메시지와 동일한 레이어에 있으며 secret가 구성된 경우에만 나타납니다.
	if secret := cfgString(cfg, "secret"); secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		payload["timestamp"] = ts
		payload["sign"] = feishuSign(ts, secret)
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		// Feishu hook의 일부 버전은 이 필드 이름 세트를 사용하며 호환됩니다.
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Feishu 응답 구문 분석 실패: %w(%s)", err, snippet(raw))
	}
	if res.Code != 0 {
		return 0, Permanent(fmt.Errorf("Feishu가 오류 %d를 반환합니다: %s", res.Code, res.Msg))
	}
	if res.StatusCode != 0 {
		return 0, Permanent(fmt.Errorf("Feishu가 오류 %d를 반환합니다: %s", res.StatusCode, res.StatusMessage))
	}
	return kept, nil
}

// feishuSign 서명은 Feishu 공식 규칙에 따라 계산됩니다.
//
// 여기서 문제가 발생하기 특히 쉽습니다. 공식적인 예는 다음과 같습니다.
//
//	hmac.new(string_to_sign.encode(), digestmod=sha256)
//
// 즉, **key = timestamp + "\n" + secret, message는 직관적이 아닌 비어있습니다**
// "key=secret, message=stringToSign" - 이것이 바로 DingTalk의 알고리즘입니다. 양쪽의 알고리즘은 정반대입니다.
// 다른 회사의 구현 방식에 따라 작성하면 필연적으로 서명 확인에 실패하게 됩니다(보고서 19021).
func feishuSign(timestamp, secret string) string {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// feishuSeverityTemplate는 취약점 수준을 카드 header 색상 템플릿에 매핑합니다.
// 알 수 없는 레벨에는 grey를 사용하십시오. low와의 혼동을 피하기 위해 blue를 사용하지 마십시오.
func feishuSeverityTemplate(severity string) string {
	switch severity {
	case "critical":
		return "red"
	case "high":
		return "orange"
	case "medium":
		return "yellow"
	case "low":
		return "blue"
	default:
		return "grey"
	}
}

// feishuMaxCardBytes는 카드 콘텐츠에 대한 보수적인 상한선입니다. Feishu에는 카드 크기 제한이 있습니다. 크기 제한을 초과하면 전체 카드가 거부됩니다.
// 공식 상한보다 훨씬 낮은 값을 선택하고 JSON 패키징 오버헤드를 포함합니다.
const feishuMaxCardBytes = 24000

// feishuCard 대화형 카드를 구성하고 카드와 실제로 작성된 항목 수를 반환합니다.
// kept는 markdownBody와 동일한 목적으로 사용됩니다. 실제로 카드에 기록된 항목만 전달된 것으로 표시되어야 합니다.
func feishuCard(m Message) (map[string]any, int) {
	elements := []any{}
	kept := 0
	if m.Batch {
		// 먼저 전체 메시지를 압축한 다음 헤더를 합칩니다. 헤더에는 "나머지 N 메시지는 다음 메시지에서 계속됩니다."라고 쓰여야 합니다.
		// N는 실제 로드된 항목 수에서 나와야 합니다.
		kept = packItemCount(m.Items, feishuMaxCardBytes, markdownReservedBytes, "", byteSize, func(it Item, idx int) string {
			return feishuBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		elements = append(elements, feishuMarkdownDiv(markdownBatchIntro(m, items, len(m.Items))))
		for i, it := range items {
			elements = append(elements, feishuMarkdownDiv(feishuBatchLine(it, i+1)))
		}
		if m.HomeURL != "" {
			elements = append(elements, feishuButton("플랫폼에서 모두 보기", m.HomeURL))
		}
	} else if len(m.Items) > 0 {
		kept = 1
		it := m.Items[0]
		elements = append(elements, feishuMarkdownDiv(feishuItemLines(it)))
		if it.DetailURL != "" {
			elements = append(elements, feishuButton("세부 사항을 확인하세요", it.DetailURL))
		}
	}

	card := map[string]any{
		"config":   map[string]any{"wide_screen_mode": true},
		"header":   map[string]any{"title": map[string]any{"tag": "plain_text", "content": markdownTitle(m)}},
		"elements": elements,
	}
	if len(m.Items) > 0 {
		card["header"].(map[string]any)["template"] = feishuSeverityTemplate(m.Items[0].Severity)
	}
	return card, kept
}

func feishuMarkdownDiv(content string) map[string]any {
	return map[string]any{"tag": "div", "text": map[string]any{"tag": "lark_md", "content": content}}
}

func feishuButton(label, url string) map[string]any {
	return map[string]any{
		"tag": "action",
		"actions": []any{map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "lark_md", "content": label},
			"url":  url,
			"type": "primary",
		}},
	}
}

// feishuItemLines 단일 취약점에 대해 lark_md 텍스트를 렌더링합니다.
//
// lark_md 및 markdown는 동일한 계열의 텍스트 형식입니다. 또한 링크와 강조를 구문 분석하므로 외부 소스에서 가져옵니다.
// 모든 필드는 markdownText(한 줄 + 이스케이프)를 통과해야 합니다. 그렇지 않으면 취약점 제목이 다음과 같을 수 있습니다.
// Feishuli는 클릭 가능한 외부 링크가 됩니다.
func feishuItemLines(it Item) string {
	out := fmt.Sprintf("**%s · %s**", SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if it.IsStatusChange() {
		out += fmt.Sprintf("\n**상태 변경**: %s → %s",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		out += fmt.Sprintf("\n**유형**: %s", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		out += fmt.Sprintf("\n**자산**: %s", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			out += fmt.Sprintf("\n **요약**: %s", s)
		}
	}
	return out
}

// feishuBatchLine 렌더링 요약 카드의 항목입니다.
func feishuBatchLine(it Item, index int) string {
	line := fmt.Sprintf("**%d. %s · %s**", index, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		line += " — " + markdownText(a, 0)
	}
	return line
}

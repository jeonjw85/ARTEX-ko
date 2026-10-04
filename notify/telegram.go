package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// telegramTextLimit는 Telegram sendMessage의 text 필드의 상한(문자 수)입니다.
const telegramTextLimit = 4096

// telegramChannel는 Telegram Bot API를 구현합니다.
//
// 플랫폼 기능:
//   - 인증은 모두 URL path(/bot<token>/sendMessage)로 이루어지며 서명이 필요하지 않습니다.
//   - MarkdownV2 대신 HTML를 사용하여 패턴을 구문 분석합니다. MarkdownV2에서는 `_*[]()~`>#+-=|{}.!`를 이스케이프해야 합니다.
//     총 18자가 있으며, 한 글자가 누락되면 전체 메시지가 거부됩니다. HTML 및 < >만 이스케이프하면 됩니다.
//   - 비즈니스 오류는 HTTP 200에도 숨겨져 있으며 ok 필드로 판단할 수 있습니다.
type telegramChannel struct{}

func (telegramChannel) Kind() string { return KindTelegram }

// Telegram 단일 채팅은 초당 약 1개의 메시지이고, 그룹 채팅은 분당 약 20개의 메시지입니다. 보수적인 가치를 취하세요.
func (telegramChannel) DefaultRatePerMin() int { return 20 }

// Bot Token는 완전한 자격 증명입니다. chat_id는 비밀이 아닌 수신자일 뿐입니다(Token를 받으면 메시지를 보낼 수 없습니다).
func (telegramChannel) SecretKeys() []string { return []string{"bot_token"} }

// base_url는 어느 API 엔드포인트 Token가 전송되는지 결정합니다(예: 자체 구축된 역방향 생성). 이를 변경하려면 Token를 다시 지정해야 합니다.
func (telegramChannel) DestinationKeys() []string { return []string{"base_url"} }

func (telegramChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "bot_token") == "" {
		return errors.New("Bot Token 누락")
	}
	if cfgString(cfg, "chat_id") == "" {
		return errors.New("Chat ID 누락")
	}
	if base := cfgString(cfg, "base_url"); base != "" {
		if err := validateHTTPURL(base); err != nil {
			return fmt.Errorf("API 잘못된 주소: %w", err)
		}
	}
	return nil
}

func (c telegramChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := telegramEndpoint(cfg)
	if err != nil {
		return 0, Permanent(err)
	}
	text, kept := telegramHTML(m)
	payload := map[string]any{
		"chat_id":                  cfgString(cfg, "chat_id"),
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": false,
	}
	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("Telegram 응답 구문 분석 실패: %w(%s)", err, snippet(raw))
	}
	if res.OK {
		return kept, nil
	}
	// 429는 전류 제한이며 백오프 후 재시도가 유효합니다. 나머지 (400 매개변수 오류, 401 token 오류, 403 블랙리스트,
	// 404 chat가 존재하지 않음)은 모두 구성 문제이므로 재시도해도 저절로 해결되지 않습니다.
	if res.ErrorCode == 429 {
		return 0, fmt.Errorf("Telegram 전류 제한: %s", res.Description)
	}
	return 0, Permanent(fmt.Errorf("Telegram는 오류 %d를 반환합니다: %s", res.ErrorCode, res.Description))
}

// telegramEndpoint는 sendMessage 주소를 나타냅니다. base_url가 비어 있는 경우 공식 API를 사용하세요.
// 비어 있지 않으면 자체 구축된 Bot API 역세대(국내 네트워크의 공통 요구 사항)를 구축하는 데 사용됩니다.
func telegramEndpoint(cfg map[string]any) (string, error) {
	base := cfgString(cfg, "base_url")
	if base == "" {
		base = "https://api.telegram.org"
	}
	base = strings.TrimSuffix(base, "/")
	token := cfgString(cfg, "bot_token")
	raw := base + "/bot" + token + "/sendMessage"
	u, err := url.Parse(raw)
	if err != nil {
		// 투명 전송 없음 err: 주소에 Bot Token가 포함되어 있으며 심지어 addr도 이때 에코되어서는 안 됩니다.
		return "", fmt.Errorf("API 주소(API 주소: %s)를 연결하지 못했습니다.", redactRequestTarget(base))
	}
	return u.String(), nil
}

// telegramHTML는 HTML의 텍스트를 렌더링하여 해당 텍스트와 작성된 실제 항목 수를 반환합니다(Channel.Send 참조).
func telegramHTML(m Message) (string, int) {
	var b strings.Builder
	b.WriteString("<b>" + telegramEscape(markdownTitle(m)) + "</b>\n")
	if m.Batch {
		// Telegram의 상한은 문자수이므로 포장도 문자(runeSize)로 측정됩니다.
		footer := ""
		if m.HomeURL != "" {
			footer = fmt.Sprintf("\n\n<a href=\"%s\">플랫폼에서 전체 보기</a>", telegramEscapeAttr(m.HomeURL))
		}
		kept := packItemCount(m.Items, telegramTextLimit, telegramReservedRunes, footer, runeSize, func(it Item, idx int) string {
			return telegramBatchLine(it, idx+1)
		})
		items := m.Items[:kept]
		b.Reset()
		b.WriteString("<b>" + telegramEscape(telegramBatchTitle(m, items, len(m.Items))) + "</b>")
		for i, it := range items {
			b.WriteString("\n" + telegramEscape(telegramBatchLine(it, i+1)))
		}
		b.WriteString(footer)
		return TruncateHTML(b.String(), telegramTextLimit), kept
	}
	if len(m.Items) == 0 {
		return b.String(), 0
	}
	it := m.Items[0]
	if it.IsStatusChange() {
		b.WriteString(fmt.Sprintf("\n<b> 상태 변경 </b>: %s → %s",
			telegramEscape(StatusLabel(it.FromStatus)), telegramEscape(StatusLabel(it.ToStatus))))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		b.WriteString("\n<b> 유형 </b>:" + telegramEscape(it.VulnClass))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		b.WriteString("\n<b> 자산 </b>:" + telegramEscape(a))
	}
	if s := OneLine(it.Summary, maxSummaryRunes); s != "" {
		b.WriteString("\n<b> 요약</b>:" + telegramEscape(s))
	}
	if it.DetailURL != "" {
		b.WriteString(fmt.Sprintf("\n\n<a href=\"%s\">자세히 보기</a>", telegramEscapeAttr(it.DetailURL)))
	}
	return TruncateHTML(b.String(), telegramTextLimit), 1
}

// telegramReservedRunes 메시지 헤더 및 가능한 잘림 힌트(문자)용으로 예약되어 있습니다.
const telegramReservedRunes = 160

// telegramBatchLine 렌더링 요약의 항목입니다(이스케이프되지 않고 호출자가 균일하게 이스케이프함).
func telegramBatchLine(it Item, idx int) string {
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		return fmt.Sprintf("%d. %s · %s — %s", idx, SeverityLabel(it.Severity), it.Title(), a)
	}
	return fmt.Sprintf("%d. %s · %s", idx, SeverityLabel(it.Severity), it.Title())
}

// telegramBatchTitle 요약 메시지의 헤더 라인을 렌더링합니다. 항목 수는 **이 항목에 실제로 포함된** 항목 수입니다.
// 이 배치의 총 개수가 아니라, 그렇지 않으면 독자들은 헤더에 적힌 숫자가 정수라고 생각할 것입니다.
func telegramBatchTitle(m Message, items []Item, total int) string {
	title := fmt.Sprintf("취약점 요약 · 총 %d 항목", total)
	if extra := total - len(items); extra > 0 {
		title += fmt.Sprintf("(처음 %d개 표시, 나머지 %d개는 다음 메시지에 이어서 전송됩니다)", len(items), extra)
	}
	if m.WindowMinutes > 0 {
		title = fmt.Sprintf("최근 %d분 · %s", m.WindowMinutes, title)
	}
	return title
}

// telegramEscape는 HTML의 텍스트 콘텐츠를 이스케이프합니다.
// Telegram는 이 세 가지 엔터티만 인식합니다. 이스케이프 후 &amp;와 같은 기존 엔터티 두 번 이스케이프됩니다. 이것은 정확히
// 원하는 동작: 사용자가 HTML를 삽입하지 않고 원래 문자를 표시하고 싶습니다.
func telegramEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// telegramEscapeAttr는 HTML 속성 값을 이스케이프합니다. 텍스트 이스케이프 외에도 따옴표도 처리해야 합니다.
// URL의 따옴표는 href 속성을 미리 닫아 다음 내용을 주입 지점으로 전환합니다.
func telegramEscapeAttr(s string) string {
	s = telegramEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}

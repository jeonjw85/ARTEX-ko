package notify

import (
	"fmt"
	"strings"
)

// 이 문서는 "Markdown 시리즈" 채널(DingTalk, Enterprise WeChat)에서 공유하는 메시지 렌더링입니다.
// Feishu용 카드 JSON, Telegram용 HTML, 이메일용 HTML가 각각 어댑터에서 렌더링됩니다.

// maxAssetsShown는 메시지에 나열된 최대 자산 수입니다. 취약점은 수십 개의 자산을 고정할 수 있습니다.
// 전체 목록은 너무 복잡하고 정보 가치가 없습니다. 아무도 IM에서 4번째 도메인 이름 이후의 도메인 이름을 보지 않을 것입니다.
const maxAssetsShown = 3

// maxSummaryRunes는 다이제스트가 압축되는 문자 수입니다. IM 메시지는 "세부 사항을 확인하라는 메시지입니다"입니다.
// 보고서 자체가 아니라 전체 콘텐츠가 플랫폼에 있습니다.
const maxSummaryRunes = 120

// 메시지 헤더용으로 예약된 markdownReservedBytes(요약 행 + 수준 분포 + 가능한 잘림 힌트)
// 꼬리(플랫폼 링크) 포함. 통째로 포장할 때 머리와 꼬리가 잘리지 않도록 이 부분을 예산에서 공제해 주세요——
// 시작과 끝이 잘리면 독자는 "이게 어떤 배치인지, 표시되지 않는 배치가 몇 개인지"조차 알 수 없습니다.
const markdownReservedBytes = 320

// markdownEscape는 markdown 메타 문자를 이스케이프합니다.
//
// 필요한 이유: ​​취약점 제목, 요약, 유형 및 자산 표시 이름은 모두 **신뢰할 수 없는 소스**에서 옵니다——
// 제목과 요약은 모델 출력(모델이 측정된 대상의 응답을 읽음)에서 나오며 자산의 url는 스캔입니다.
// 획득된 완전한 URL(대상 제어 가능한 쿼리 문자열 포함) 탈출하지 않고 제목은 다음과 같습니다.
//
//	로그인 포트 SQL \n 삽입 [긴급 상황: 계정을 확인하려면 여기를 클릭하세요] (http://attacker.tld)
//
// 취약점은 보안 엔지니어의 DingTalk/Feishu에서 클릭 가능한 외부 링크로 렌더링됩니다. 그리고
// `![](http://attacker.tld/beacon)`는 렌더링 중에 클라이언트에 의해 당겨지며 이는 알림과 동일합니다.
// "이 취약점이 발견되었습니다." 독자 IP에게 유출되었습니다. 무해한 내용이라 하더라도 굵은 글씨나
// 견적 블록은 접힌 부분 아래에 중요한 구멍을 밀어낼 수도 있습니다.
//
// 이스케이프 세트는 제목/링크/강조/목록/인용문/취소선 카테고리를 포함하며 구조를 변경하거나 클릭 가능하게 만듭니다.
// 요소의 특성입니다. `\`를 먼저 처리해야 합니다. 그렇지 않으면 끝에 추가된 백슬래시가 다시 이스케이프됩니다.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText는 신뢰할 수 없는 텍스트를 한 줄로 압축하고 markdown 본문에서 사용하기 위해 이스케이프합니다.
// 단일 라이닝은 이스케이프의 나머지 절반입니다. 개행 자체는 새 목록 항목이나 따옴표 블록을 가짜로 만들 수 있습니다.
// 그리고 이스케이프 문자는 이를 막을 수 없습니다.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle는 메시지 제목(IM 플랫폼의 제목 표시줄/카드 제목)을 반환하며, 내용은 **이스케이프되지 않은 원본 텍스트**입니다.
//
// 여기서는 의도적으로 탈출할 수 없습니다. 이 제목은 markdown 텍스트, Telegram라는 네 가지 컨텍스트의 렌더러에서 공유됩니다.
// Feishu 카드의 HTML, plain_text, 일반 Webhook의 JSON 및 이메일 제목입니다. 상황에 따라
// 이스케이프 규칙은 다릅니다(markdown를 HTML로 이스케이프하면 눈에 띄는 백슬래시가 남고 JSON를 이스케이프하면 오염됩니다).
// 데이터), 따라서 이스케이프는 해당 출력에 의해 수행되어야 합니다. writeItem / feishuItemLines /를 참조하세요.
// telegramEscape. 한때 공유 함수에 markdown 이스케이프를 추가했는데 결과가 메시지에 Telegram였습니다.
// `\(1\)`와 같은 백슬래시가 표시됩니다.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("취약점 요약 · 총 %d 항목", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "취약점 알림"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody는 메시지 본문을 렌더링하고 본문과 작성된 항목의 실제 수를 반환합니다.
//
// 반환 값 kept는 이 배송으로 실제로 배송된 항목 수입니다. 따라서 호출자는 첫 번째 kept 항목만 다음으로 표시합니다.
// 배송됨 - 채널 길이 제한으로 인해 차단된 항목은 함께 전송되지 않고 다음 배치를 위해 보관되어야 합니다.
// 표시에 성공했습니다. 여기서 "조용한 손실"이 발생합니다. 메시지가 잘렸지만 배달 기록에는 메시지가 완전히 배달되었음을 표시합니다.
// 후반부가 발행되지 않았다는 내용은 어디에도 없습니다.
//
// maxBytes<=0은 제한이 없음을 의미합니다.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// 너무 길어도 단일 메시지가 전송됩니다(끝에는 잘림): 취약점에 대한 부분 정보
		// 전혀 보내지 않는 것보다 낫습니다.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[플랫폼 전체 보기](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro는 요약 메시지의 시작 부분(시간 창, 항목 수 및 레벨 분포)을 렌더링합니다.
// 이를 통해 요약을 받은 사람들은 플랫폼을 클릭하지 않고도 이 배치를 즉시 처리해야 하는지 여부를 판단할 수 있습니다.
//
// items는 실제로 로드되는 항목이고 total는 이 배치에 있어야 하는 총 개수입니다. 둘이 다른 경우에는 이를 명확히 명시해야 합니다.
// "다음 메시지에는 메시지가 몇 개나 더 있나요?" 그렇지 않으면 독자들은 메시지 헤더에 적힌 숫자가 전부라고 생각할 것입니다.
// 전송되지 않은 이후 항목은 인터페이스에 전혀 존재하지 않습니다.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**최근 %d분 동안 발견된 취약점 %d개**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**새로 발견된 취약점 %d개**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, "(처음 %d개 표시, 나머지 %d개는 다음 메시지에 이어서 전송됩니다)", len(items), extra)
	}
	// 레벨별로 분포를 주어 심각한 항목이 있는지 독자들이 한눈에 알 수 있도록 해준다. **이 기사에 실제로 포함된 내용**만 계산하세요.
	// 항목은 '심각 3'이 아래에 집계 가능한 항목과 일치하는지 확인하세요.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem는 단일 취약점 항목을 렌더링합니다.
//
// prefix는 목록을 요약하는 데 사용되는 일련 번호입니다. single=true인 경우 전체 버전(요약 및 백링크 포함)이 렌더링됩니다.
// 요약 목록에서는 한 줄의 요약만 렌더링합니다. 그렇지 않으면 50개의 요약이 하나의 긴 문서가 됩니다.
//
// 외부의 모든 콘텐츠(제목/유형/자산/초록)는 markdownText를 통과합니다.
// 싱글라인 + 이스케이프. 링크 백은 관리자가 구성한 public_base_url로 작성되었으며 신뢰할 수 없는 내용이 아닙니다.
// 그리고 클릭 가능한 링크여야 하므로 그대로 출력됩니다.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// 요약 모드: 한 줄로 표시되며 자산 및 요약 압축이 이어집니다.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**상태 변경**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**유형**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**자산**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**요약**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[세부 사항을 확인하세요](%s)\n", it.DetailURL)
	}
}

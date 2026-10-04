package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes는 s를 max 바이트 이하로 자르므로 문자가 잘리지 않고 결과가 올바른 UTF-8가 됩니다.
//
// 문자 경계에 따라 잘라야 하는 이유: Qiweiqun Robot의 markdown에는 4096 **바이트**라는 엄격한 상한이 있습니다.
// 문자), 한글은 3바이트입니다. 바이트 단위로 직접 자르면 문자가 반으로 잘려 잘못된 출력이 발생합니다.
// UTF-8——플랫폼 측은 전체 라인을 거부하거나 왜곡된 사각형으로 표시합니다. 여기서 접근 방식은 예산 위치부터 시작하는 것입니다.
// 최신 rune 시작 바이트(utf8.RuneStart 판정 연속 바이트 0b10xxxxxx)로 돌아갑니다.
//
// max<=0은 제한이 없음을 의미합니다. max가 너무 작아서 줄임표에 맞지 않는 경우를 제외하고 잘린 후에 줄임표가 추가됩니다.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max는 줄임표보다 짧습니다. 줄임표를 버리고 max를 초과하는 결과를 피하기 위해 완전히 자릅니다.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine 여러 줄의 텍스트를 한 줄로 변환합니다. 공백을 모두 접고 문자 수에 따라 자릅니다.
// IM 메시지에 사용된 제목 줄 - 초록에 줄바꿈이 있는 경우가 많으며 이를 표/제목에 직접 삽입하면 레이아웃이 깨집니다.
// max<=0은 길이 제한이 없음을 의미합니다.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes s를 max 문자(바이트 아님) 이하로 자르고, 초과하는 경우 줄임표를 추가합니다.
// max<=0은 제한이 없음을 의미합니다.
//
// TruncateBytes와의 차이점은 플랫폼 구경에 있습니다. Qiwei는 길이가 바이트로 제한되는 반면 Telegram는 문자 수로 길이가 제한됩니다.
// 잘못된 구경을 사용하면 오류가 보고되지 않지만 메시지 길이가 예상보다 훨씬 짧은 길이(한국어 1단어 = 3바이트,
// 4096바이트를 잘라서 1365워드 정도만 남게 되므로 두 기능 모두 유지하고 채널에 따라 선택해야 합니다.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML는 문자 수에 따라 HTML 조각을 자르고 하프 컷 레이블이 생성되지 않도록 합니다.
//
// HTML의 문자를 직접 자르면 `<a href="htt`와 같은 불완전한 태그가 잘리고 플랫폼 파서는 다음 중 하나를 수행합니다.
// 오류가 보고되면 기사 전체가 거부되거나 후속 텍스트가 속성 값으로 삼켜집니다. 여기서 접근 방식은 다음과 같습니다. 먼저 문자별로 자르고,
// 꼬리 부분에 닫히지 않은 `<`가 있는지 다시 확인하고, 있으면 그곳으로 후퇴한다.
//
// 라벨 밸런싱 없음(</b> 완료 등): Telegram의 HTML 파서는 닫히지 않은 라벨을 자동으로 닫습니다.
// 균형 조정을 직접 구현하려면 속성의 따옴표, 주석 및 자체 닫는 태그를 처리해야 하며 이점에 비해 복잡성이 너무 큽니다.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// 꼬리가 `<`로 시작하는 조각인 경우(마지막 `<` 뒤에 `>`가 없음) `<` 앞에 반환됩니다.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// 꼬리가 잘린 HTML 개체인 경우(예: `&amp;`가 `&amp`로 잘려진 경우) 꼬리도 반환되어야 합니다.
	// 엔터티 전용 파서의 엔터티 조각화로 인해 전체 메시지가 거부될 수 있습니다.
	// 최대 길이의 집계된 메시지는 이미 충분히 일반적이므로 전체 알림을 버릴 가치가 없습니다.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount 요약된 메시지를 전체 메시지로 패키징할 수 있도록 예산 내에서 **완전히** 작성할 수 있는 메시지 수를 계산합니다.
//
// 전체 항목을 렌더링한 다음 자르는 대신 전체 항목을 클릭해야 하는 이유는 잘림으로 인해 항목의 나머지 절반이 허공에서 사라지게 됩니다.
// 그리고 배송 기록은 계속 배송된 것으로 표시됩니다. 이는 메시지나 배송 기록에서 볼 수 없습니다.
// 허점이 사라졌습니다. 전체 품목을 포장한 후 맞지 않는 품목은 라이브러리에 남아 다음 배치가 됩니다.
// 호출자가 얻은 kept는 실제로 전달된 메시지 수입니다.
//
// 매개변수: maxSize<=0은 제한이 없음을 의미합니다. reserve는 메시지의 헤더/테일을 위해 예약된 금액입니다.
// size는 측정을 담당합니다(각 플랫폼의 구경은 다릅니다. Qiwei/DingTalk는 바이트를 기반으로 하고 Telegram는 문자 수를 기반으로 합니다.
// 잘못된 구경을 사용하면 오류가 보고되지 않고 한국어 메시지가 상한보다 훨씬 낮은 수준으로 억제됩니다.
// render 항목 idx를 실제 텍스트로 렌더링합니다. 길이는 내용에 따라 다르며 추정할 수 없습니다.
//
// 최소한 1개를 반환합니다(아직 항목이 있는 경우). 이 메시지는 단일 메시지가 너무 길고 발신자의
// 마지막으로 주머니 바닥을 잘라냅니다. 그렇지 않으면 매우 긴 허점이 전체 배치를 영구적으로 제자리에 고정하게 됩니다.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize는 packItemCount의 두 가지 측정 구경입니다. 전화를 피하기 위해 이름이 지정되었습니다.
// 벌거벗은 func(s string) int 클로저가 나타나며, 그렇지 않으면 어떤 구경이 사용되는지 한눈에 알기 어렵습니다.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine는 자산 목록을 표시 텍스트 줄로 렌더링합니다. limit를 초과할 경우 나머지는 생략되고 총 개수가 표시됩니다.
// 취약점은 수십 개의 자산에 고정되어 있을 수 있으며 이를 모두 나열하면 많은 뉴스가 나올 것입니다.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " 외 총 " + itoa(len(assets)) + "개"
}

// itoa는 strconv.Itoa의 짧은 별칭으로, 어디에서나 import strconv를 방지하기 위해 표시 텍스트를 연결하는 데만 사용됩니다.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

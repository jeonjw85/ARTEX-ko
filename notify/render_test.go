package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// 이것이 이 패키지의 가장 중요한 불변입니다. Qiwei.com에는 **바이트** 제한이 있습니다. 한국어는 단어당 3바이트입니다.
	// 바이트 단위로 하드 커팅을 구현하면 문자가 반으로 자르고 불법적인 UTF-8가 생성되며 플랫폼에서 거부됩니다.
	// 가능한 모든 컷 포인트에 도달하려면 상대적으로 긴 길이의 여러 중국어 및 영어 혼합 입력을 사용하십시오.
	inputs := []string{
		"한국어 시험 내용",
		"믹스 mixed 콘텐츠 content",
		"a 중국어 b 중국어 c 테스트 d 테스트 e",
		"🔴🟠🟡🔵", // 4바이트 emoji, 오류가 더 분명합니다.
		strings.Repeat("취약점", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("입력 %q max=%d: 잘못된 출력 UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("입력 %q max=%d: 결과 %d 바이트가 상한을 초과했습니다.", in, max, len(got))
			}
			// 내용이 잘리지 않으면 변경해서는 안 됩니다.
			if len(in) <= max && got != in {
				t.Fatalf("입력 %q max=%d : 한도를 초과하지 않고 내용이 변경됨 -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0은 제한이 없음을 의미해야 합니다.")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0은 제한이 없음을 의미해야 합니다.")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// max가 줄임표 자체보다 작은 경우 줄임표를 추가하여 제한을 초과할 수 없습니다.
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("max=1인 경우 결과는 %q 길이 %d가 제한을 초과합니다.", got, len(got))
	}
	// 일반적으로 줄임표가 있어야 합니다.
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("예상되는 타원은 %q입니다.", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// TruncateBytes와의 구경 차이는 유지되어야 합니다. Telegram는 길이가 문자로 제한됩니다.
	// 바이트 크기를 사용하면 한국어 메시지가 1/3로 줄어듭니다.
	s := "하나, 둘, 셋, 넷, 다섯, 여섯, 일곱, 여덟, 아흔"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("5자 예상, %d 획득(%q)", n, got)
	}
	// 동일한 문자열은 바이트 측면에서 상당히 짧아야 합니다.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("바이트 구경은 문자 구경과 동일한 수의 문자를 생성해서는 안 됩니다.")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("첫 번째 줄은 \n\n입니다. 두 번째 줄은 탭과 여러 공백이 있는 \t입니다.", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("모든 공백은 축소되어 %q를 제공해야 합니다.", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("연속 공백이 유지되어서는 안 됩니다. %q가 발생합니다.", got)
	}
	// 잘림은 여전히 ​​읽을 수 있고 합법적이어야 합니다.
	got = OneLine("하나, 둘, 셋, 넷, 다섯, 여섯, 일곱, 여덟, 아흔", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("4자가 필요합니다. %d(%q)를 얻었습니다.", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// HTML를 직접 자르면 `<a href="htt`와 같은 조각이 잘리고 플랫폼은 전체 메시지를 거부합니다.
	s := `<b>Title</b>TextTextText <a href="https://example.com/very/long/path">세부정보 보기</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: 결과 %d 문자가 제한을 초과했습니다.", max, n)
		}
		// 꼬리에는 닫히지 않은 `<`가 있을 수 없습니다. 즉, `<`는 마지막 단락에 나타나지만 `>`는 없습니다.
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: 꼬리 라벨이 잘림 -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("자산이 없으면 빈 문자열이 반환되어야 하며 %q를 얻습니다.", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("한도를 초과하지 않은 모든 항목이 나열되어야하며 %q를 얻습니다.", got)
	}
	// 상한을 초과하는 경우 총 개수를 표시해야 하며, 그렇지 않으면 독자는 목록에 없는 자산의 개수를 알 수 없습니다.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "총 5개") {
		t.Fatalf("총 개수는 5여야 ​​하며 결과적으로 %q가 됩니다.", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("빈 수준 서수는 0이며 모든 임계값에 의해 차단되어야 합니다.")
	}
	if !AtLeast("critical", "") {
		t.Fatal("빈 임계값을 해제해야 합니다.")
	}
	if got := StatusLabel("fixed"); got != "수정됨" {
		t.Fatalf("알 수 없는 상태 매핑, %q 가져오기", got)
	}
	// 알 수 없는 상태는 잘못된 라벨 없이 그대로 에코됩니다.
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("알 수 없는 상태는 있는 그대로 에코되어 %q를 가져와야 합니다.", got)
	}
}

// TestTruncateHTMLNeverCutsEntity 커버리지 감사에서 지적된 누락: 잘림은 피해야 할 뿐만 아니라
// 라벨을 절반으로 자르되 HTML 개체도 잘리지 않도록 하십시오.
//
// `&amp;`가 `&amp`로 절단된 후 엔터티 전용 파서가 전체 메시지를 거부할 수 있습니다.
// 그러나 매우 긴 요약 메시지는 이미 일반화되어 있으며 비용도 너무 높습니다.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// "&는 있지만 해당하는 ;"은 없는 엔터티 조각 마지막에 나타나지 않아야 합니다.
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: 꼬리에 물리적 조각 남기기 %q", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: 변형된 개체가 나타납니다.", max)
		}
	}
}

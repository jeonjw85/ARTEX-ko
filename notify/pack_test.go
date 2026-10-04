package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 이 문서는 "전체 메시지로 패키지화"에 대한 수정 사항을 다룹니다. 요약 메시지가 채널 길이의 상한을 초과하는 경우 **전체 메시지를 눌러야 합니다**
// 언로드된 항목의 실제 수를 자르고 보고하여 호출자가 실제로 전달된 항목만 표시할 수 있도록 합니다.
//
// 이전 방법은 전체 메시지를 렌더링한 다음 잘라낸 다음 전체 마크 배치를 전달하는 것이었습니다. 메시지의 후반부는 허공에서 사라졌습니다.
// 전달 기록을 보면 모두 성공했음을 알 수 있습니다. 취약점은 사라졌고 어디에서도 찾을 수 없습니다.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// 200개의 한국어 항목을 요약하면 Qiwei의 4096바이트를 훨씬 초과해야 합니다.
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("텍스트 %d 바이트가 상한 %d를 초과했습니다.", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("텍스트가 합법적이지 않습니다. UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("%d를 얻으려면 그 중 일부만 설치해야 합니다(0 < kept < %d).", len(m.Items), kept)
	}
	// 헤더에는 이 기사에 포함된 항목 수와 다른 항목 수를 정확하게 명시해야 합니다. 그렇지 않으면 독자는 헤더를 다음과 같이 해석할 것입니다.
	// 그 숫자는 모두로 간주됩니다.
	if !strings.Contains(body, "나머지") || !strings.Contains(body, "다음 메시지에 이어서") {
		t.Fatalf("헤더에는 이 문서에 포함되지 않은 항목 수가 표시되어야 합니다. \n%s", body[:minInt(400, len(body))])
	}
	// 첫 번째 kept 항목만 포함되어야 합니다.
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "취약점"+itoa(i+1)) {
			t.Fatalf("기사 %d가 이 메시지에 있어야 합니다: \n%s", i+1, body)
		}
	}
	if strings.Contains(body, "취약점"+itoa(kept+1)) {
		t.Fatalf("품목 %d가 나타나지 않아야 합니다(다음 배치에 속함).", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = 제한 없음
	if kept != len(m.Items) {
		t.Fatalf("길이에 제한이 없으면 모두 유지해야 하며, kept=%d 가 됩니다.", kept)
	}
	if strings.Contains(body, "나머지") {
		t.Fatalf("잘림이 없는 경우 잘림 프롬프트가 표시되지 않아야 합니다. \n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// 예산이 너무 작아서 한 마리도 들어갈 수 없을 때 한 마리는 계속 발송됩니다(결국 주머니 바닥이 잘립니다).
	// 그렇지 않으면 매우 긴 허점이 전체 배치를 영구적으로 차단하게 됩니다. 배치를 받을 때마다 맞지 않고 매번 발행되지도 않습니다.
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("%d를 얻으려면 최소한 1개는 유지해야 합니다.", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("단일 메시지가 전달된 것으로 보고되어야 하며 %d를 얻습니다.", kept)
	}
	// 빈 메시지에는 전달 가능한 항목이 없습니다.
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("0개의 빈 메시지가 보고되어야 하며 %d를 얻습니다.", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram는 **문자 수**에 따라 길이가 제한됩니다. 바이트 크기를 사용하면 한국어 메시지가 1/3로 줄어듭니다.
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("Text %d 문자가 상한을 초과했습니다 %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("다음 부품만 설치해야 합니다. %d를 구입하세요.", kept)
	}
	if !strings.Contains(text, "다음 메시지에 이어서") {
		t.Fatalf("포함되지 않은 남은 금액이 있다는 점에 유의하세요: \n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("카드는 %d를 얻으려면 카드의 일부만 보유해야 합니다.", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// 이 두 채널은 텍스트를 자르지 않으며 전체 배치가 전달된 것으로 간주됩니다.
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("전제 조건이 확립되지 않았습니다.")
	}
	// 렌더러의 반환 값을 통해 간접적으로 확인: markdownBody(0) 제한이 없는 경우 모두 예약됩니다.
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("길이 제한 없이 모두 적용하고 %d 획득", k)
	}
}

// TestMarkdownEscapesUntrustedContent는 "신뢰할 수 없는 콘텐츠는 메시지 구조를 변경해서는 안 됩니다"에 대한 회귀 테스트입니다.
// 제목과 요약은 모델 출력(모델이 측정된 대상의 응답을 읽음)에서 오고, 자산 이름은 측정된 대상의 URL에서 옵니다.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // 결과에 나타나야 합니다(이스케이프된 형식).
		wrong []string // 결과에 나타나지 않아야 합니다(이스케이프되지 않은 형식).
	}{
		{
			name: "제목의 줄 바꿈 + 외부 링크",
			item: Item{
				Severity: "high",
				Name:     "로그인 포트 SQL \n 삽입 [긴급 상황: 계정을 확인하려면 여기를 클릭하세요] (http://attacker.tld)",
			},
			// 줄 바꿈은 축소되어야 합니다(그렇지 않으면 새 목록 항목/인용 블록이 위조될 수 있음).
			// 대괄호와 괄호는 이스케이프되어야 합니다. 그렇지 않으면 외부 링크를 클릭할 수 있습니다.
			must:  []string{`\[긴급 상황: 계정을 확인하려면 여기를 클릭하세요\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[긴급", "\n\n[긴급"},
		},
		{
			name: "헤더의 이미지 비콘",
			item: Item{
				Severity: "high",
				Name:     "취약점 ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "자산명의 강조 및 인용",
			item: Item{
				Severity: "high",
				Name:     "일반 제목",
				Assets:   []string{"a.com/*주입*>견적"},
			},
			must:  []string{`\*주입\*`, `\>`},
			wrong: []string{"*주입*"},
		},
		{
			name: "초록의 백틱 및 세로 막대",
			item: Item{
				Severity: "high",
				Name:     "제목",
				Summary:  "`code` | 형태",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			// 단일 모드의 쓰기 항목은 3개의 markdown 채널이 공유하는 렌더링 경로입니다.
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("누락된 이스케이프 양식 %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("이스케이프되지 않은 형식 %q가 나타납니다(구조 또는 외부 링크를 삽입하는 데 사용할 수 있음): \n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst 잠금 이스케이프 순서: 백슬래시를 먼저 처리해야 합니다.
// 그렇지 않으면 추가된 백슬래시가 다시 덮이고 출력에 이중 백슬래시가 나타납니다.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("이스케이프 시퀀스가 ​​잘못되어 %q가 표시됩니다.", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes는 특정 반환을 잠급니다.
// markdown 이스케이프는 Telegram의 HTML 출력으로 누출되어서는 안 됩니다(공유 헤더 기능에서 한 번)
// 이스케이프 후 Telegram는 `\(1\)`와 같은 백슬래시가 보이는 메시지에 나타납니다.
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *핵심 포인트*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("Telegram markdown의 백슬래시 이스케이프는 텍스트에 나타납니다: \n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

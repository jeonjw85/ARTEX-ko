package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// singleMsg 따옴표와 줄 바꿈을 사용하여 단일 메시지를 구성합니다. `"` 및 `\n`가 포함된 제목/초록을 의도적으로 사용하십시오.
// 이는 템플릿 보간이 불법 JSON로 가장 쉽게 생성하는 입력입니다.
func singleMsg() Message {
	return Message{
		Items: []Item{{
			FindingID: 42,
			Name:      `로그인 "SQL 주입" 위험`,
			VulnClass: "SQL 주입",
			Severity:  "high",
			Summary:   "매개변수 id\n가 필터링되지 않아 주입이 발생함",
			Assets:    []string{"a.example.com", "b.example.com"},
			DetailURL: "https://artex.local/function/findings/detail?id=42",
		}},
	}
}

// batchMsg는 요약 메시지 배치를 구성합니다.
func batchMsg(n int) Message {
	m := Message{Batch: true, WindowMinutes: 30, HomeURL: "https://artex.local/function/findings"}
	for i := 0; i < n; i++ {
		m.Items = append(m.Items, Item{
			FindingID: int64(i + 1),
			Name:      "취약점" + itoa(i+1),
			VulnClass: "XSS",
			Severity:  "medium",
			Summary:   "반영된 크로스 사이트 스크립팅",
			Assets:    []string{"target.example.com"},
		})
	}
	return m
}

// capturePost는 가짜 수신단을 시작하고 수신된 요청 본문과 헤더를 어설션 기능으로 다시 보냅니다.
func capturePost(t *testing.T, respBody string, assert func(t *testing.T, body map[string]any, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Errorf("요청 본문이 유효하지 않습니다. JSON: %v\n 원본 텍스트: %s", err, raw)
			}
		}
		if assert != nil {
			assert(t, body, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDingTalkSendsActionCardWhenLinkPresent(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "actionCard" {
			t.Fatalf("다시 링크가 있으면 actionCard를 보내고 %v를 받아야 합니다.", body["msgtype"])
		}
		card, _ := body["actionCard"].(map[string]any)
		if card["singleURL"] != "https://artex.local/function/findings/detail?id=42" {
			t.Errorf("잃어버린 링크: %v", card["singleURL"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
}

func TestDingTalkFallsBackToMarkdownForBatch(t *testing.T) {
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msgtype"] != "markdown" {
			t.Fatalf("요약 메시지는 markdown로 전송되고 %v를 받아야 합니다.", body["msgtype"])
		}
		md, _ := body["markdown"].(map[string]any)
		if !strings.Contains(md["text"].(string), "최근 30분") {
			t.Errorf("요약 텍스트에 기간이 누락되었습니다: %v", md["text"])
		}
	})
	if _, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, batchMsg(3)); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
}

// TestDingTalkBusinessErrorIsPermanent는 "HTTP 200이지만 errcode는 0이 아닙니다"라는 판정을 잠급니다.
// errcode를 확인하지 못하면 배송 실패가 성공으로 기록됩니다. 이는 모든 국내 IM 플랫폼에 공통적으로 나타나는 함정입니다.
func TestDingTalkBusinessErrorIsPermanent(t *testing.T) {
	srv := capturePost(t, `{"errcode":310000,"errmsg":"keywords not in content"}`, nil)
	_, err := (dingTalkChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg())
	if err == nil {
		t.Fatal("errcode 0이 아닌 경우 오류가 보고되어야 합니다.")
	}
	if !IsPermanent(err) {
		t.Fatalf("키워드 불일치는 구성 오류이며 영구적인 실패로 표시되어야 하며 결과적으로 %v가 발생합니다.", err)
	}
	if !strings.Contains(err.Error(), "310000") {
		t.Errorf("오류 메시지에는 플랫폼 오류 코드(%v 가져오기)가 포함되어야 합니다.", err)
	}
}

func TestWeComTruncatesCJKWithinByteLimit(t *testing.T) {
	var contentLen int
	srv := capturePost(t, `{"errcode":0,"errmsg":"ok"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		md, _ := body["markdown"].(map[string]any)
		content, _ := md["content"].(string)
		contentLen = len(content)
		if !utf8.ValidString(content) {
			t.Fatal("잘린 후에는 올바르지 않습니다. UTF-8 - Qiwei가 전체 메시지를 거부합니다.")
		}
	})
	// 충분히 긴 한국어 요약 배치를 생성하려면 4096바이트를 초과해야 합니다.
	m := batchMsg(200)
	if _, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, m); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
	if contentLen > weComMarkdownLimit {
		t.Fatalf("텍스트 %d 바이트가 엔터프라이즈 마이크로 상한 %d를 초과했습니다.", contentLen, weComMarkdownLimit)
	}
	if contentLen == 0 {
		t.Fatal("텍스트가 비어 있습니다.")
	}
}

func TestWeComRateLimitIsRetryableButKeyErrorIsPermanent(t *testing.T) {
	limited := capturePost(t, `{"errcode":45009,"errmsg":"api freq out of limit"}`, nil)
	_, err := (weComChannel{}).Send(context.Background(), map[string]any{"webhook": limited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("45009는 롤링 창 전류 제한입니다. 재시도하여 %v를 얻을 수 있어야 합니다.", err)
	}

	badKey := capturePost(t, `{"errcode":93000,"errmsg":"invalid webhook url"}`, nil)
	_, err = (weComChannel{}).Send(context.Background(), map[string]any{"webhook": badKey.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("93000은 key이며 유효하지 않습니다. 재시도는 저절로 치유되지 않습니다. 영구적인 오류가 발생하고 %v를 받아야 합니다.", err)
	}
}

func TestFeishuCardStructureAndSign(t *testing.T) {
	const secret = "SECtest123"
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["msg_type"] != "interactive" {
			t.Fatalf("인터랙티브 카드를 발급받아 %v를 받아야 합니다.", body["msg_type"])
		}
		card, _ := body["card"].(map[string]any)
		header, _ := card["header"].(map[string]any)
		if header["template"] != "orange" {
			t.Errorf("high 레벨은 orange 색상 일치여야 하며, %v를 얻으세요.", header["template"])
		}
		// secret가 구성된 경우 서명 매개변수가 포함되어야 합니다. 그렇지 않으면 Feishu는 이를 19021로 거부합니다.
		if body["sign"] == nil || body["timestamp"] == nil {
			t.Fatalf("누락된 서명 매개변수: %v", body)
		}
		// 카드 요소에는 취약점 세부정보를 가리키는 url 버튼이 포함되어야 합니다.
		elements, _ := card["elements"].([]any)
		foundButton := false
		for _, e := range elements {
			em, _ := e.(map[string]any)
			if em["tag"] != "action" {
				continue
			}
			actions, _ := em["actions"].([]any)
			for _, a := range actions {
				am, _ := a.(map[string]any)
				if am["url"] == "https://artex.local/function/findings/detail?id=42" {
					foundButton = true
				}
			}
		}
		if !foundButton {
			t.Fatal("카드에 세부정보 페이지로 연결되는 버튼이 없습니다.")
		}
	})
	cfg := map[string]any{"webhook": srv.URL, "secret": secret}
	if _, err := (feishuChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
}

func TestFeishuWithoutSecretOmitsSign(t *testing.T) {
	srv := capturePost(t, `{"code":0,"msg":"success"}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["sign"] != nil || body["timestamp"] != nil {
			t.Fatalf("secret가 구성되지 않은 경우 서명 매개변수 %v를 포함해서는 안 됩니다.", body)
		}
	})
	if _, err := (feishuChannel{}).Send(context.Background(), map[string]any{"webhook": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
}

func TestTelegramEscapesHTMLInUntrustedContent(t *testing.T) {
	var text string
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		text, _ = body["text"].(string)
		if body["parse_mode"] != "HTML" {
			t.Fatalf("%v를 얻으려면 HTML 구문 분석 모드를 사용해야 합니다.", body["parse_mode"])
		}
	})
	m := Message{Items: []Item{{
		Severity: "high",
		// 제목과 요약은 테스트된 대상/모델 출력에서 ​​나온 것이며 신뢰할 수 없는 콘텐츠입니다.
		Name:    `<script>alert(1)</script>`,
		Summary: "a & b < c",
	}}}
	if _, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": srv.URL}, m); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
	if strings.Contains(text, "<script>") {
		t.Fatalf("HTML가 이스케이프되지 않고 주입이 존재합니다: %q", text)
	}
	if !strings.Contains(text, "&lt;script&gt;") {
		t.Fatalf("이스케이프된 엔터티가 예상되며 %q를 얻었습니다.", text)
	}
	if !strings.Contains(text, "a &amp; b") {
		t.Fatalf("&가 이스케이프되지 않아 %q를 얻습니다.", text)
	}
}

func TestTelegramErrorClassification(t *testing.T) {
	rateLimited := capturePost(t, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`, nil)
	_, err := (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": rateLimited.URL}, singleMsg())
	if err == nil || IsPermanent(err) {
		t.Fatalf("429는 재시도하여 %v를 얻을 수 있어야 합니다.", err)
	}

	forbidden := capturePost(t, `{"ok":false,"error_code":403,"description":"bot was blocked by the user"}`, nil)
	_, err = (telegramChannel{}).Send(context.Background(),
		map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": forbidden.URL}, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("403은 구성 문제이므로 영구적인 오류여야 하며 %v가 표시됩니다.", err)
	}
}

func TestWebhookDefaultTemplateProducesValidJSON(t *testing.T) {
	// 이것이 기본 템플릿의 의미입니다. 제목에 따옴표와 줄 바꿈이 있는 경우 모든 간단한
	// `"title": "{{.Title}}"`를 작성하면 잘못된 JSON가 생성됩니다. {{json .}}는 그렇지 않습니다.
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, _ *http.Request) {
		if body["title"] != `[🟠 높은 위험] 로그인 "SQL 주입" 위험` {
			t.Errorf("제목이 올바르게 복원되지 않음: %v", body["title"])
		}
		items, _ := body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items 수량은 1이어야 하며, %d를 얻으세요.", len(items))
		}
		it, _ := items[0].(map[string]any)
		if it["summary"] != "매개변수 id\n가 필터링되지 않아 주입이 발생함" {
			t.Errorf("요약이 올바르게 복원되지 않음: %v", it["summary"])
		}
		// 값은 문자열이 아닌 JSON 숫자여야 합니다(json:"...,string"와 같이 쓰면 이 트랩이 발생함).
		if _, ok := it["finding_id"].(float64); !ok {
			t.Errorf("finding_id는 숫자여야 합니다. %T가 있습니다.", it["finding_id"])
		}
	})
	if _, err := (webhookChannel{}).Send(context.Background(), map[string]any{"url": srv.URL}, singleMsg()); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
}

func TestWebhookCustomTemplateAndHeaders(t *testing.T) {
	srv := capturePost(t, `{"ok":true}`, func(t *testing.T, body map[string]any, r *http.Request) {
		if r.Header.Get("X-Token") != "s3cret" {
			t.Errorf("맞춤 헤더 누락: %v", r.Header)
		}
		if body["msg"] != "3개 항목" {
			t.Errorf("맞춤 템플릿 렌더링 오류: %v", body["msg"])
		}
		if body["first"] != "취약점1" {
			t.Errorf("range 추출 오류: %v", body["first"])
		}
	})
	cfg := map[string]any{
		"url":           srv.URL,
		"headers":       map[string]any{"X-Token": "s3cret"},
		"body_template": `{"msg": {{json (printf "%d개 항목" .Count)}}, "first": {{json (index .Items 0).Name}}}`,
	}
	if _, err := (webhookChannel{}).Send(context.Background(), cfg, batchMsg(3)); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
}

func TestWebhookRejectsNonJSONRenderResult(t *testing.T) {
	cfg := map[string]any{"url": "https://example.com/hook", "body_template": `not json at all`}
	_, err := (webhookChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil || !IsPermanent(err) {
		t.Fatalf("렌더링 결과가 JSON가 아니면 영구 실패여야 하며(템플릿이 잘못 작성되어 재시도가 쓸모 없음) %v를 얻습니다.", err)
	}
}

func TestWebhookValidateCatchesBadConfigEarly(t *testing.T) {
	bad := []map[string]any{
		{},
		{"url": "file:///etc/passwd"},
		{"url": "https://example.com", "method": "DELETE"},
		{"url": "https://example.com", "body_template": `{{.Items.`},
	}
	for i, cfg := range bad {
		if err := (webhookChannel{}).Validate(cfg); err == nil {
			t.Errorf("그룹 구성 %d는 거부되어야 합니다: %v", i, cfg)
		}
	}
}

func TestEmailMessageIsWellFormed(t *testing.T) {
	msg, err := buildEmailMessage("artex@example.com", []string{"a@example.com", "b@example.com"}, singleMsg())
	if err != nil {
		t.Fatalf("이메일 수집 실패: %v", err)
	}
	if !strings.HasPrefix(msg, "From: artex@example.com\r\n") {
		t.Fatalf("From 헤더 오류: \n%s", msg)
	}
	if !strings.Contains(msg, "To: a@example.com, b@example.com\r\n") {
		t.Fatalf("To 헤더 오류: \n%s", msg)
	}
	// 한국어 테마는 RFC 2047로 인코딩되어야 합니다. 그렇지 않으면 클라이언트에 잘못된 문자가 표시됩니다.
	if !strings.Contains(msg, "Subject: =?utf-8?") {
		t.Fatalf("테마가 완료되지 않았습니다. RFC 2047 코드: \n%s", msg)
	}
	if dec, err := new(mime.WordDecoder).DecodeHeader(mustExtractHeader(t, msg, "Subject")); err != nil {
		t.Fatalf("주제를 디코딩할 수 없습니다: %v", err)
	} else if !strings.Contains(dec, "SQL 주입") {
		t.Fatalf("주제의 디코딩된 내용이 올바르지 않습니다: %q", dec)
	}

	// 텍스트는 base64이고 솔루션은 합법적인 HTML여야 합니다.
	parts := strings.SplitN(msg, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatal("메시지에 헤더/본문 구분 기호가 없습니다.")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(parts[1]), "\r\n", ""))
	if err != nil {
		t.Fatalf("텍스트 base64 디코딩 실패: %v", err)
	}
	html := string(decoded)
	if !strings.HasPrefix(html, "<div") {
		t.Fatalf("텍스트가 HTML가 아닙니다: %.80s", html)
	}
	// 제목은 HTML 텍스트 위치에 있는 그대로 나타납니다. 텍스트 내용의 큰따옴표는 유효한 문자이므로 이스케이프할 필요가 없습니다.
	// 여기서 "그대로 유지하라"는 주장은 앞으로 누군가가 실수로 따옴표 이스케이프를 추가하는 것을 방지하기 위한 것이므로 한국어 따옴표가
	// &quot;로 표시됩니다.
	if !strings.Contains(html, `"SQL 주입"`) {
		t.Fatalf("제목의 따옴표는 텍스트 위치(%.200s)에 그대로 두어야 합니다.", html)
	}
}

// TestEmailEscapesStructuralInjection는 이메일 본문이 실제로 방지해야 하는 주입을 다룹니다.
// 취약점 제목과 요약은 테스트된 대상과 모델 출력에서 ​​나온 것이며 신뢰할 수 없는 콘텐츠입니다. 텍스트 위치를 이스케이프해야 합니다.
// & < >(그렇지 않으면 태그를 삽입할 수 있음), 속성 위치에도 이스케이프된 따옴표가 있어야 합니다(그렇지 않으면 href를 닫을 수 있음).
func TestEmailEscapesStructuralInjection(t *testing.T) {
	m := Message{
		Items: []Item{{
			Severity:  "high",
			Name:      `<script>alert(1)</script>`,
			Summary:   "a & b > c",
			DetailURL: `https://artex.local/x?a="onmouseover=alert(1)`,
		}},
	}
	html := htmlBody(m, 0)
	if strings.Contains(html, "<script>") {
		t.Fatalf("제목은 이스케이프되지 않으며 태그를 삽입할 수 있습니다: %s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Fatalf("예상되는 이스케이프 엔터티: %s", html)
	}
	if !strings.Contains(html, "a &amp; b &gt; c") {
		t.Fatalf("& 및 > 이스케이프되지 않음: %s", html)
	}
	// 백링크는 public_base_url이며 관리자가 구성할 수 있습니다. 신뢰성은 높지만 속성 위치는 여전히
	// 따옴표를 이스케이프 처리하세요. 그렇지 않으면 따옴표 붙은 주소가 href를 묶고 이벤트 핸들러를 삽입합니다.
	if strings.Contains(html, `onmouseover=alert(1)">`) {
		t.Fatalf("href 속성이 올바르게 이스케이프되지 않았습니다: %s", html)
	}
	if !strings.Contains(html, "&quot;") {
		t.Fatalf("속성 위치의 따옴표는 이스케이프되어야 합니다: %s", html)
	}
}

func mustExtractHeader(t *testing.T, msg, name string) string {
	t.Helper()
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, name+": ") {
			return strings.TrimPrefix(line, name+": ")
		}
	}
	t.Fatalf("%s 헤더를 찾을 수 없습니다", name)
	return ""
}

func TestChannelValidateReportsMissingFields(t *testing.T) {
	// 확인 오류는 구성자에게 직접 표시되며, 일반적인 "구성이 잘못되었습니다"가 아니라 누락된 내용을 명확하게 명시해야 합니다.
	cases := []struct {
		kind   string
		cfg    map[string]any
		substr string
	}{
		{KindDingTalk, map[string]any{}, "Webhook"},
		{KindFeishu, map[string]any{}, "Webhook"},
		{KindWeCom, map[string]any{}, "Webhook"},
		{KindTelegram, map[string]any{}, "Bot Token"},
		{KindTelegram, map[string]any{"bot_token": "t"}, "Chat ID"},
		{KindEmail, map[string]any{}, "SMTP"},
		{KindEmail, map[string]any{"host": "h"}, "포트"},
		{KindEmail, map[string]any{"host": "h", "port": 587, "from": "f"}, "수신자"},
	}
	for _, tc := range cases {
		ch, ok := Get(tc.kind)
		if !ok {
			t.Fatalf("채널 %s 등록되지 않음", tc.kind)
		}
		err := ch.Validate(tc.cfg)
		if err == nil {
			t.Errorf("%s 구성 %v는 확인에 실패해야 합니다.", tc.kind, tc.cfg)
			continue
		}
		if !strings.Contains(err.Error(), tc.substr) {
			t.Errorf("%s에 대한 오류 메시지에는 %q가 언급되어야 하며, %q를 얻으세요.", tc.kind, tc.substr, err.Error())
		}
	}
}

// TestEmailSMTPErrorClassification는 SMTP 4xx/5xx의 의미적 구별을 잠급니다.
// 4xx도 영구 실패로 판단되면 그레이리스팅이 활성화된 메일 서버로 인해 각 푸시가 처음으로 실패하게 됩니다.
// 시도한 후 failed에 빠졌으며 그레이리스트 지정은 자동 재시도가 역할을 수행해야 하는 시나리오와 정확히 같습니다.
func TestEmailSMTPErrorClassification(t *testing.T) {
	cases := []struct {
		reply     string
		permanent bool
	}{
		{"450 4.7.1 Greylisting in action, please come back later", false},
		{"451 4.3.0 Temporary system failure", false},
		{"452 4.2.2 Mailbox full", false},
		{"550 5.1.1 User unknown", true},
		{"553 5.1.3 Bad address syntax", true},
		{"554 5.7.1 Relay access denied", true},
		// 응답 코드를 얻을 수 없는 경우 "재시도 가능"을 누르십시오. 순간적인 위험을 감수하는 것보다 한 번 더 시도하는 것이 좋습니다.
		// 실패는 치명적이다.
		{"unexpected EOF", false},
		{"", false},
	}
	for _, tc := range cases {
		err := smtpStageError("수신자가 거부됨", errors.New(tc.reply))
		if got := IsPermanent(err); got != tc.permanent {
			t.Errorf("응답 %q: permanent=%v 예상 %v 가져오기", tc.reply, tc.permanent, got)
		}
		// 어떻게 분류되더라도 사용자가 확인할 수 있도록 원문을 보관해야 합니다.
		if tc.reply != "" && !strings.Contains(err.Error(), tc.reply) {
			t.Errorf("%q 답글의 원본 텍스트는 삭제되었습니다: %v", tc.reply, err)
		}
	}
}

func TestRegistryCoversAllKinds(t *testing.T) {
	// 6개 채널은 모두 필수입니다. 누락된 채널은 UI에서 자동으로 사라집니다.
	want := []string{KindDingTalk, KindEmail, KindFeishu, KindTelegram, KindWebhook, KindWeCom}
	got := Kinds()
	if len(got) != len(want) {
		t.Fatalf("채널 수는 %d여야 하며 %d: %v를 얻습니다.", len(want), len(got), got)
	}
	for _, k := range want {
		if !ValidKind(k) {
			t.Errorf("채널 %s 등록되지 않음", k)
		}
		if ch, ok := Get(k); !ok || ch.Kind() != k {
			t.Errorf("%s 채널의 Kind()가 등록 키와 일치하지 않습니다.", k)
		}
	}
	if ValidKind("nope") {
		t.Error("등록되지 않은 유형은 유효성 검사를 통과하면 안 됩니다.")
	}
}

func TestPermanentErrorUnwrap(t *testing.T) {
	base := &permanentSentinel{}
	err := Permanent(base)
	if !IsPermanent(err) {
		t.Fatal("영구적인 실패로 인식되어야 함")
	}
	if !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("오류 메시지는 하위 계층인 %v에 투명하게 전송되어야 합니다.", err)
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil)는 nil를 반환해야 합니다.")
	}
	if IsPermanent(nil) {
		t.Fatal("nil는 영구적인 오류가 아닙니다.")
	}
}

type permanentSentinel struct{}

func (*permanentSentinel) Error() string { return "sentinel" }

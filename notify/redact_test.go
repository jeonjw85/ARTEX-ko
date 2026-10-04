package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// 이 파일은 고정 테스트입니다. 채널 구현에서 나오는 **모든** 오류 텍스트는 자격 증명을 전달해서는 안 됩니다.
//
// 별도의 파일을 가져오는 이유: 초기 채널 사용 사례에서는 성공적인 경로와 플랫폼 비즈니스 오류만 다루고,
// 전송 계층 오류가 전혀 표시되지 않았습니다. 그리고 가장 위험한 것은 바로 전송 계층 오류(연결 거부/DNS 실패/시간 초과)입니다.
// http.Client.Do에서 반환된 *url.Error는 오류 텍스트에 **완전한 URL**를 입력하며 이 함수는
// 이들 회사의 자격 증명은 URL에 있습니다. 자격 증명은 이 스트림을 따라 4개의 출구로 나갔습니다.
//
//	notification_deliveries.last_error → 라이브러리에 저장된 텍스트 지우기
//	GET /api/notify/deliveries 응답 → 채널 구성 마스크를 우회하고 브라우저에 에코
//	서버 로그 → 외부 당사자가 자주 보관함
//	송신 인터페이스의 502 응답 테스트 → 프런트 엔드에 직접 팝업
//
// 따라서 여기서는 하나의 기능만 테스트하는 것이 아니라 실제로 각 채널을 통해 실패해야 하는 요청을 보내고 오류 텍스트를 확인합니다.
// 해당 자격증명을 찾을 수 없습니다.

// credentialCases는 "URL의 자격 증명"을 사용하여 모든 채널 형식을 다룹니다.
// DingTalk/Qiwei는 query에 있고, Feishu는 경로 끝에 있으며, Telegram는 경로 중간에 있습니다.
var credentialCases = []struct {
	name   string
	ch     Channel
	cfg    map[string]any
	secret string
}{
	{
		name:   "query의 딩톡 access_token",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send?access_token=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "query의 엔터프라이즈 WeChat key",
		ch:     weComChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/cgi-bin/webhook/send?key=" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "Feishu hook id는 경로 끝에 있습니다.",
		ch:     feishuChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/open-apis/bot/v2/hook/" + leakProbeToken},
		secret: leakProbeToken,
	},
	{
		name:   "경로 중간에 있는 Telegram bot token",
		ch:     telegramChannel{},
		cfg:    map[string]any{"bot_token": leakProbeToken, "chat_id": "1", "base_url": "http://127.0.0.1:1"},
		secret: leakProbeToken,
	},
	{
		name:   "DingTalk 서명 키",
		ch:     dingTalkChannel{},
		cfg:    map[string]any{"webhook": "http://127.0.0.1:1/robot/send", "secret": leakProbeToken},
		secret: leakProbeToken,
	},
}

// leakProbeToken는 실제 자격 증명이 될 수 없는 센티널 값으로 오류 텍스트에서 검색하는 데 사용됩니다.
const leakProbeToken = "LEAKPROBE0123456789abcdef"

// TestChannelErrorsNeverLeakCredentials는 핵심 불변입니다.
func TestChannelErrorsNeverLeakCredentials(t *testing.T) {
	for _, tc := range credentialCases {
		t.Run(tc.name, func(t *testing.T) {
			// 실패해야 하는 피어: 127.0.0.1:1은 무인 상태이며 연결 거부 경로를 사용합니다.
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{
				Items: []Item{{FindingID: 1, Severity: "high", Name: "누출 프로브"}},
			})
			if err == nil {
				t.Fatal("연결할 수 없는 주소에 대해서는 오류가 보고되어야 합니다.")
			}
			assertNoSecret(t, err.Error(), tc.secret)
		})
	}
}

// TestChannelErrorsNeverLeakCredentialsInPermanentPath는 영구적으로 실패한 분기를 다룹니다.
// URL 검증 실패, 플랫폼 업무 오류 등도 오류 텍스트를 외부로 전송하며 자격 증명을 포함할 수 없습니다.
func TestChannelErrorsNeverLeakCredentialsInPermanentPath(t *testing.T) {
	cases := []struct {
		name string
		ch   Channel
		cfg  map[string]any
	}{
		// 주소에 자격 증명이 포함되어 있지만 형식이 잘못되었습니다. → validateHTTPURL / url.Parse 분기를 트리거합니다.
		{"딩톡 주소가 불법입니다", dingTalkChannel{}, map[string]any{"webhook": "file:///" + leakProbeToken}},
		{"기업 마이크로 주소가 불법입니다", weComChannel{}, map[string]any{"webhook": "gopher://" + leakProbeToken}},
		{"Feishu 주소가 불법입니다", feishuChannel{}, map[string]any{"webhook": "ftp://" + leakProbeToken + "/hook"}},
		{"Telegram API 불법 주소", telegramChannel{}, map[string]any{"bot_token": "tok", "chat_id": "1", "base_url": "file://" + leakProbeToken}},
		{"일반 Webhook 주소가 불법입니다", webhookChannel{}, map[string]any{"url": "javascript:" + leakProbeToken}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.ch.Send(context.Background(), tc.cfg, Message{Items: []Item{{Severity: "high"}}})
			if err == nil {
				t.Fatal("잘못된 구성은 오류를 보고해야 합니다.")
			}
			assertNoSecret(t, err.Error(), leakProbeToken)
		})
	}
}

func assertNoSecret(t *testing.T, text, secret string) {
	t.Helper()
	if strings.Contains(text, secret) {
		t.Fatalf("오류 텍스트에 자격 증명 %q:\n %s가 표시됩니다.", secret, text)
	}
}

func TestRedactRequestTargetKeepsOnlySchemeAndHost(t *testing.T) {
	cases := map[string]string{
		"https://oapi.dingtalk.com/robot/send?access_token=S1":    "https://oapi.dingtalk.com/…",
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=S2": "https://qyapi.weixin.qq.com/…",
		"https://open.feishu.cn/open-apis/bot/v2/hook/S3":         "https://open.feishu.cn/…",
		"https://api.telegram.org/botS4/sendMessage":              "https://api.telegram.org/…",
		"http://10.0.0.5:8080/hook":                               "http://10.0.0.5:8080/…",
	}
	for in, want := range cases {
		got := redactRequestTarget(in)
		if got != want {
			t.Errorf("redactRequestTarget(%q) = %q, %q를 기대하세요", in, got, want)
		}
		// 감도 줄이기 결과 자체에는 더 이상 원래 주소의 경로/쿼리 조각이 포함되어서는 안 됩니다.
		if parts := strings.SplitN(in, "://", 2); len(parts) == 2 {
			if hostAndRest := strings.SplitN(parts[1], "/", 2); len(hostAndRest) == 2 && hostAndRest[1] != "" {
				if strings.Contains(got, hostAndRest[1]) {
					t.Errorf("둔감화 후에도 경로/쿼리 조각은 여전히 ​​포함됩니다. %q: %q", hostAndRest[1], got)
				}
			}
		}
	}
	// 구문 분석할 수 없는 입력은 원래 문자열을 반영하지 않습니다.
	for _, bad := range []string{"", "://", "not a url", "http://"} {
		if got := redactRequestTarget(bad); strings.Contains(got, bad) && bad != "" {
			t.Errorf("구문 분석할 수 없는 입력 %q가 %q로 에코되었습니다.", bad, got)
		}
	}
}

// TestRedactTransportErrorStripsURL는 특정 유형 *url.Error에 직접 초점을 맞춥니다.
// http.Client.Do의 리턴형으로 최초 유출된 사이트였습니다.
func TestRedactTransportErrorStripsURL(t *testing.T) {
	inner := errors.New("dial tcp 127.0.0.1:1: connect: connection refused")
	uerr := &url.Error{
		Op:  "Post",
		URL: "https://api.telegram.org/bot" + leakProbeToken + "/sendMessage",
		Err: inner,
	}
	got := redactTransportError(uerr)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "api.telegram.org") {
		t.Errorf("문제 해결을 위해 host를 보관해야 하며, %q를 획득해야 합니다.", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("문제 해결을 위해서는 근본 원인을 유지해야 하며 %q를 얻습니다.", got)
	}
	// Op도 유지해야 합니다(POST 또는 GET는 문제 해결에 의미가 있음).
	if !strings.Contains(got, "Post") {
		t.Errorf("작업 이름은 유지되어야 하며 %q를 가져옵니다.", got)
	}
}

// TestRedactURLsInTextHandlesFallback 더티 경로: 비*url.Error 사용자 정의 오류
// (리디렉션 정책에 의해 반환된 오류 등)도 제거됩니다.
func TestRedactURLsInTextHandlesFallback(t *testing.T) {
	in := fmt.Sprintf("호스트 간 리디렉션 거부(a.example → http://b.example/bot%s/send）", leakProbeToken)
	got := redactURLsInText(in)
	assertNoSecret(t, got, leakProbeToken)
	if !strings.Contains(got, "http://b.example/…") {
		t.Errorf("%q를 얻으려면 주소를 둔감한 형식으로 바꿔야 합니다.", got)
	}
	// 주소가 없는 텍스트는 그대로 유지됩니다.
	if plain := "dial tcp: connection refused"; redactURLsInText(plain) != plain {
		t.Error("주소가 없는 텍스트는 수정하면 안 됩니다.")
	}
}

// TestCrossHostRedirectRefused는 "URL의 자격 증명 + 호스트 간 점프 따르기 = 자격 증명 전달"을 다룹니다.
// httptest의 두 서비스는 127.0.0.1의 서로 다른 포트에서 모니터링됩니다. 포트가 다르면 Host가 다릅니다.
// 크로스 호스트 점프를 구성합니다.
func TestCrossHostRedirectRefused(t *testing.T) {
	var hit bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/robot/send?access_token="+leakProbeToken, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": redirector.URL + "/robot/send?access_token=" + leakProbeToken},
		Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("호스트 간 리디렉션은 거부되어야 합니다.")
	}
	if hit {
		t.Fatal("점프 대상에 액세스했습니다. 리디렉션으로 자격 증명이 유출되었습니다.")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestSameHostRedirectAllowed 역 사용 사례: 동일한 호스트 점프(예: 후행 슬래시)를 계속 사용할 수 있어야 합니다.
// 그렇지 않으면 일반적인 작업 흐름이 완전히 차단됩니다.
func TestSameHostRedirectAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robot/send" {
			// 동일한 호스트와 포트로 이동합니다.
			http.Redirect(w, r, "/robot/send/", http.StatusTemporaryRedirect)
			return
		}
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"},
		Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("동일한 호스트 리디렉션을 거부하면 안 됩니다: %v", err)
	}
}

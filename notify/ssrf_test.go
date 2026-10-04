package notify

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// 이 문서에서는 두 가지 관련 강화에 대해 설명합니다.
//   ① 배송주소는 서버를 인트라넷/클라우드 메타데이터(SSRF)에 접근하기 위한 발판으로 삼아서는 안 됩니다.
//   ② 주소 확인 시 오류 메시지가 해당 주소에 있는 자격 증명을 가져오면 안 됩니다.
//
// 테스트 환경 정보: 이 패키지의 많은 사용 사례는 127.0.0.1의 httptest 가짜 수신기를 사용하며 경비원은 기본적으로 이를 차단합니다.
// 그들. 따라서 AllowLocalTargetsEnv는 TestMain에서 균일하게 열리고 아래의 각 SSRF 사용 사례는 다음과 같습니다.
// **기본적으로 거부** 동작을 주장하려면 명시적으로 이를 지웁니다.

func TestMain(m *testing.M) {
	// 일반 사용 사례가 로컬 가짜 수신기에 연결되도록 허용합니다. SSRF 사용 사례는 일시적으로 자체적으로 삭제됩니다.
	_ = os.Setenv(AllowLocalTargetsEnv, "1")
	os.Exit(m.Run())
}

// TestDialGuardRejectsLoopbackByDefault는 SSRF 보호의 핵심 주장입니다.
// 기본적으로 루프백 주소로의 전달은 연결 계층에서 거부되어야 합니다.
func TestDialGuardRejectsLoopbackByDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "") // 탈출 해치 닫기 = 기본 동작
	_, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}})
	if err == nil {
		t.Fatal("루프백 주소로의 배송은 기본적으로 허용되지 않습니다.")
	}
	if hit {
		t.Fatal("요청이 현지 서비스로 전송되었습니다. 경비원이 유효하지 않습니다.")
	}
	// 오류 메시지는 사용자에게 해제 방법을 안내할 수 있어야 합니다(이 기계의 SMTP 릴레이는 합법적인 구성입니다).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("거부 메시지에는 %v를 명시적으로 해제하는 방법이 설명되어 있어야 합니다.", err)
	}
}

// TestDialGuardAllowsLoopbackWhenOptedIn 역 사용 사례: 명시적으로 연 후 사용할 수 있어야 합니다.
// 그렇지 않으면 로컬 postfix/인트라넷 릴레이와 같은 합법적인 배포가 전반적으로 폐기됩니다.
func TestDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (dingTalkChannel{}).Send(context.Background(),
		map[string]any{"webhook": srv.URL + "/robot/send"}, Message{Items: []Item{{Severity: "high"}}}); err != nil {
		t.Fatalf("명시적인 릴리스 이후 제공 가능해야 합니다: %v", err)
	}
}

func TestIsBlockedDialIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.1.2.3", "::1",
		"169.254.169.254", // 클라우드 메타데이터 엔드포인트 - 이 기능이 존재하는 주된 이유
		"169.254.1.1", "fe80::1",
		"0.0.0.0", "::",
		"224.0.0.1", "ff02::1",
		"::ffff:127.0.0.1", // 판단하기 전에 IPv4-mapped 형식을 복원해야 합니다. 그렇지 않으면 포트가 우회됩니다.
		"",
	}
	for _, s := range blocked {
		if !isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s는 거부되어야 합니다", s)
		}
	}
	// RFC1918 개인 네트워크 **의도적으로 허용됨**: 자체 구축된 내부 네트워크 Mattermost / SMTP 릴레이는 일반적인 법적 사용입니다.
	// 이 주장은 이러한 절충안을 해결합니다. 만약 누군가가 나중에 사설 네트워크 판단을 편리하게 추가한다면 이는 실패할 것입니다.
	// 이는 일괄 배포를 자동으로 폐기하는 대신 의식적인 결정을 내리게 합니다.
	allowed := []string{"10.0.0.5", "172.16.3.4", "192.168.1.10", "8.8.8.8", "2606:4700::1111"}
	for _, s := range allowed {
		if isBlockedDialIP(net.ParseIP(s)) {
			t.Errorf("%s를 허용해야 합니다(개인 네트워크는 일반적으로 합법적인 전달 대상입니다).", s)
		}
	}
}

// TestValidateHTTPURLRejectsLiteralPrivateTargets는 구성 단계의 사전 프롬프트를 다룹니다.
// 첫 번째 전달이 실패할 때까지 기다리지 말고 저장 시 리터럴 IP를 거부했어야 합니다.
func TestValidateHTTPURLRejectsLiteralPrivateTargets(t *testing.T) {
	t.Setenv(AllowLocalTargetsEnv, "")
	for _, raw := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]:8080/hook",
	} {
		if err := validateHTTPURL(raw); err == nil {
			t.Errorf("%s는 구성 단계에서 거부되어야 합니다.", raw)
		}
	}
	// 공용 네트워크 주소와 개인 네트워크 주소는 평소대로 전달됩니다(사설 네트워크는 전화 접속 단계용으로 예약되어 있으며 여기서 차단되지 않습니다).
	for _, raw := range []string{"https://oapi.dingtalk.com/robot/send", "http://10.0.0.9/hook"} {
		if err := validateHTTPURL(raw); err != nil {
			t.Errorf("%s는 검증을 통과해야 합니다: %v", raw, err)
		}
	}
}

// TestValidateHTTPURLErrorNeverLeaksCredentials는 감사에서 지난 라운드에서 놓쳤다고 지적한 브랜치입니다.
//
// url.Parse가 **실패**하면 *url.Error를 반환하며, Error()에는 전체 원래 주소가 포함됩니다. 지난 라운드에는 나만
// 여기서는 http.Client.Do의 반환 오류가 둔감해지고 누락되었습니다. 그 당시 "영구적인 실패 경로" 사용 사례가 추가되었습니다.
// （file://、gopher://、ftp://）사실, 그들은 모두 될 수 있습니다 url.Parse 분석이 성공했으며 경로는 다음과 같습니다. scheme 나뭇가지，
// 따라서 완전히 친환경적이라고 해서 이 길이 안전하다는 것을 증명하는 것은 아닙니다. 이는 잘못된 보장입니다.
func TestValidateHTTPURLErrorNeverLeaksCredentials(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/%zz?access_token=" + leakProbeToken,         // 잘못된 백분율 기호 이스케이프
		"https://a.example.com:port/x?access_token=" + leakProbeToken, // 숫자가 아닌 포트
		"http://[::1?access_token=" + leakProbeToken,                  // 괄호가 일치하지 않음
	}
	for _, raw := range cases {
		// 먼저 이 입력이 **실제로** url.Parse를 실패하게 만드는지 확인하세요. 이 단계를 수행하지 않으면 사용 사례가 다음과 같을 수 있습니다.
		// 자신도 모르게 다른 지점으로 이동합니다(마지막 라운드에서 허위 보장이 이렇게 발생했습니다).
		if _, err := url.Parse(raw); err == nil {
			t.Errorf("%q는 구문 분석에 실패해야 합니다. 그렇지 않으면 이 사용 사례가 대상 분기를 다루지 않았습니다.", raw)
			continue
		}
		err := validateHTTPURL(raw)
		if err == nil {
			t.Errorf("%q는 실패를 확인해야 합니다.", raw)
			continue
		}
		assertNoSecret(t, err.Error(), leakProbeToken)
	}
	// 채널 수준의 패키징에 주소가 나오지 않는지 확인하세요.
	t.Setenv(AllowLocalTargetsEnv, "")
	err := (dingTalkChannel{}).Validate(map[string]any{"webhook": cases[0]})
	if err == nil {
		t.Fatal("잘못된 주소는 확인에 실패합니다.")
	}
	assertNoSecret(t, err.Error(), leakProbeToken)
}

// TestEmailDialGuardRejectsLoopbackByDefault는 SMTP 채널의 다이얼 가드를 덮습니다.
//
// 이메일 채널은 SSRF 보호 전체 세트의 유일한 공백인 net.Dialer를 사용했습니다. host가 채워졌습니다.
// 169.254.169.254 또는 127.0.0.1은 직접 연결할 수 있지만 smtp.NewClient는
// 피어가 반환한 라인이 올바르지 않으며 last_error를 통한 전달 기록 인터페이스에 의해 에코됩니다. 이는 다른 채널입니다.
// 반맹인 읽기 기본 기능이 꺼졌습니다. "연결 거부 vs 시간 초과"의 시간 차이를 사용하여 포트를 감지할 수도 있습니다.
//
// 이 패키지의 TestMain는 전 세계적으로 AllowLocalTargetsEnv를 엽니다(많은 사용 사례에서는 127.0.0.1을 사용합니다).
// 가짜 수신기), 따라서 이 사용 사례에서는 자체적으로 삭제해야 합니다. 그렇지 않으면 가드 존재 여부에 관계없이 통과됩니다.
// 애초에 어떠한 테스트에서도 격차가 발견되지 않은 이유가 여기에 있습니다.
func TestEmailDialGuardRejectsLoopbackByDefault(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "") // 탈출 해치 닫기 = 기본 동작
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Fatal("루프백 주소로의 메일 배달은 기본적으로 허용되지 않습니다.")
	}
	// 연결이 전혀 설정되어서는 안 됩니다. 가드가 Control 후크에서 연결을 차단하고 EHLO가 전송되지 않습니다.
	if f.sawCommand("EHLO") || f.sawCommand("HELO") {
		t.Fatal("SMTP 세션이 설정되었습니다. 가드가 적용되지 않습니다.")
	}
	// 오류 메시지는 사용자에게 해제 방법을 안내할 수 있어야 합니다(이 기계의 postfix 릴레이는 합법적인 구성입니다).
	if !strings.Contains(err.Error(), AllowLocalTargetsEnv) {
		t.Errorf("거부 메시지에는 %v를 명시적으로 해제하는 방법이 설명되어 있어야 합니다.", err)
	}
}

// TestEmailDialGuardAllowsLoopbackWhenOptedIn는 페어링의 반대 사용 사례입니다. 명시적으로 연 후
// 정상적으로 배송이 가능해야 합니다. 자체 구축된 인트라넷 SMTP/로컬 릴레이는 매우 일반적인 배포이며 경비원은 모든 것에 적합할 수 없습니다.
func TestEmailDialGuardAllowsLoopbackWhenOptedIn(t *testing.T) {
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)

	t.Setenv(AllowLocalTargetsEnv, "1")
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("명시적 릴리스 후 SMTP 머신은 다음을 제공할 수 있어야 합니다. %v", err)
	}
	if !f.sawCommand("EHLO") {
		t.Fatal("EHLO가 표시되지 않음 - 세션이 실제로 설정되지 않음")
	}
}

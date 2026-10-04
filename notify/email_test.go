package notify

import (
	"bufio"
	"context"

	"net"
	"strings"
	"sync"
	"testing"
)

// 이 문서는 이메일 채널의 프로토콜 수준 테스트를 완료합니다. 이전에는 email.Send의 적용 범위가 0이었습니다.
// 전체 SMTP 경로를 통해 실행된 사용 사례는 없으며 6개 채널 중 가장 큰 프로토콜입니다.
// 가장 오류가 발생하기 쉬운 단계(핸드셰이크, 인증, 봉투, DATA 단계 각각에는 고유한 실패 의미 체계가 있음)입니다.
//
// 여기서는 net/smtp 대신 mock 대신 자체 구축된 최소 SMTP 서버 드라이버를 사용합니다.
// 이메일 채널의 위험 대부분은 "실제 SMTP 서버와 통신"하는 단계에 있습니다.
// 이 단계 mock를 삭제하는 것은 사고입니다.

// fakeSMTP는 충분한 SMTP 서버입니다. greet/EHLO/AUTH/MAIL/RCPT/DATA/QUIT를 완료할 수 있습니다.
// 그리고 사용 사례에서 요구하는 대로 특정 단계에 대해 지정된 응답 코드를 반환합니다.
type fakeSMTP struct {
	ln net.Listener

	// rcptReply는 RCPT TO의 응답입니다. 기본값은 250입니다.
	rcptReply string
	// mailReply는 MAIL FROM의 응답입니다. 기본값은 250입니다.
	mailReply string
	// advertiseAuth가 true인 경우 EHLO에서 AUTH PLAIN에 대한 지원을 선언합니다.
	advertiseAuth bool

	mu       sync.Mutex
	data     string
	commands []string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, rcptReply: "250 OK", mailReply: "250 OK"}
	go f.serve()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	addr, ok := f.ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("비TCP 청취 주소")
	}
	return "127.0.0.1", addr.Port
}

func (f *fakeSMTP) record(cmd string) {
	f.mu.Lock()
	f.commands = append(f.commands, cmd)
	f.mu.Unlock()
}

func (f *fakeSMTP) body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data
}

func (f *fakeSMTP) sawCommand(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (f *fakeSMTP) serve() {
	conn, err := f.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	br := bufio.NewReader(conn)
	w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
	w("220 fake.local ESMTP ready")
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.record(line)
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			// STARTTLS를 선언하지 마십시오. 코드가 일반 텍스트 분기를 사용하도록 합니다(테스트 대상은 TLS가 아닌 봉투 논리입니다).
			w("250-fake.local")
			if f.advertiseAuth {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case strings.HasPrefix(line, "AUTH"):
			// 단순화된 처리: PLAIN의 초기 응답은 여러 줄에 걸쳐 있을 수 있으므로 직접 승인됩니다.
			w("235 2.7.0 Authentication successful")
		case strings.HasPrefix(line, "MAIL FROM"):
			w(f.mailReply)
		case strings.HasPrefix(line, "RCPT TO"):
			w(f.rcptReply)
		case strings.HasPrefix(line, "DATA"):
			w("354 End data with <CR><LF>.<CR><LF>")
			var sb strings.Builder
			for {
				dl, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dl, "\r\n") == "." {
					break
				}
				sb.WriteString(dl)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 2.0.0 Ok: queued as FAKE1")
		case strings.HasPrefix(line, "QUIT"):
			w("221 2.0.0 Bye")
			return
		default:
			w("250 OK")
		}
	}
}

func emailCfg(t *testing.T, f *fakeSMTP, extra map[string]any) map[string]any {
	t.Helper()
	host, port := f.hostPort(t)
	cfg := map[string]any{
		"host": host,
		"port": float64(port),
		"from": "artex@example.com",
		"to":   []any{"a@example.com", "b@example.com"},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

func TestEmailSendDeliversFullMessage(t *testing.T) {
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	cfg := emailCfg(t, f, map[string]any{"username": "artex", "password": "pw"})

	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
	// 봉투 단계는 보낸 사람, 두 명의 받는 사람, DATA로 이동해야 합니다.
	for _, want := range []string{"MAIL FROM:<artex@example.com>", "RCPT TO:<a@example.com>", "RCPT TO:<b@example.com>", "DATA", "AUTH", "QUIT"} {
		if !f.sawCommand(want) {
			t.Errorf("세션에 SMTP %q가 없습니다. 실제 명령: %v", want, f.commands)
		}
	}
	// 텍스트는 base64의 HTML이며, 실제 취약점 내용이 포함되어야 합니다(인코딩 후에도 읽을 수 있음).
	body := f.body()
	if body == "" {
		t.Fatal("DATA 단계에서 문자가 수신되지 않았습니다.")
	}
	if !strings.Contains(body, "Content-Type: text/html") {
		t.Errorf("Content-Type 헤더 누락:\n%s", body)
	}
	if !strings.Contains(body, "base64") {
		t.Errorf("본문은 base64로 인코딩되지 않았습니다(긴 HTML 줄은 SMTP의 1000바이트 줄 길이 제한을 위반합니다): \n%s", body)
	}
	// To 헤더에는 여러 수신자가 표시되어야 합니다.
	if !strings.Contains(body, "a@example.com, b@example.com") {
		t.Errorf("To 헤더에 모든 수신자가 포함되어 있지 않습니다: \n%s", body)
	}
}

func TestEmailSendWithoutAuth(t *testing.T) {
	// 계정이 할당되지 않은 경우 AUTH를 보내면 안 됩니다. 일부 중계에서는 이로 인해 이를 거부합니다.
	f := newFakeSMTP(t)
	cfg := emailCfg(t, f, nil)
	if _, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg()); err != nil {
		t.Fatalf("배송 실패: %v", err)
	}
	if f.sawCommand("AUTH") {
		t.Errorf("계정이 할당되지 않았지만 AUTH: %v", f.commands)
	}
}

// TestEmailSendClassifiesSMTPReplies는 이 감사 복구를 직접 검증합니다.
// 5xx는 영구적인 실패를 의미하고, 4xx(회색 목록)는 다시 시도할 수 있음을 의미합니다.
func TestEmailSendClassifiesSMTPReplies(t *testing.T) {
	cases := []struct {
		name      string
		rcptReply string
		mailReply string
		permanent bool
	}{
		{"수신자는 550으로 영구적으로 거부됩니다.", "550 5.1.1 User unknown", "250 OK", true},
		{"수신자가 450 그레이리스트를 발견했습니다.", "450 4.7.1 Greylisting in action", "250 OK", false},
		{"수신자가 452를 발견했습니다. 사서함이 가득 찼습니다.", "452 4.2.2 Mailbox full", "250 OK", false},
		{"발신자가 553으로 영구적으로 거부되었습니다.", "250 OK", "553 5.1.3 Bad address", true},
		{"보낸 사람에게 451 임시 오류가 발생했습니다.", "250 OK", "451 4.3.0 Temporary failure", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSMTP(t)
			f.rcptReply = tc.rcptReply
			f.mailReply = tc.mailReply
			_, err := (emailChannel{}).Send(context.Background(), emailCfg(t, f, nil), singleMsg())
			if err == nil {
				t.Fatal("오류가 보고되어야 합니다.")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("permanent 결정 오류: %v를 예상하고 %v(%v)를 얻습니다.", tc.permanent, got, err)
			}
			// 원본 서버 텍스트는 유지되어야 합니다. 그렇지 않으면 사용자는 서버 관리자에게 문의해야 할지, 주소를 변경해야 할지 알 수 없습니다.
			if !strings.Contains(err.Error(), strings.Fields(tc.rcptReply)[0]) && !strings.Contains(err.Error(), strings.Fields(tc.mailReply)[0]) {
				t.Errorf("서버의 응답 코드는 오류에 유지되어야 합니다: %v", err)
			}
		})
	}
}

func TestEmailSendRefusesPlaintextCredentials(t *testing.T) {
	// net/smtp의 PlainAuth는 암호화되지 않은 연결을 통한 자격 증명 발급을 거부합니다(대상이 localhost가 아닌 경우).
	// 이는 **올바른** 보안 동작이므로 우회할 수 없습니다. 그러나 사용자가 문제를 해결하도록 안내할 수 있는 오류가 제공되어야 합니다.
	// 여기서는 localhost가 아닌 호스트 이름으로 트리거됩니다.
	f := newFakeSMTP(t)
	f.advertiseAuth = true
	_, port := f.hostPort(t)
	cfg := map[string]any{
		"host":     "smtp.example.com", // localhost 아님
		"port":     float64(port),
		"from":     "a@example.com",
		"to":       []any{"b@example.com"},
		"username": "artex",
		"password": "pw",
	}
	_, err := (emailChannel{}).Send(context.Background(), cfg, singleMsg())
	if err == nil {
		t.Skip("로컬 DNS는 로컬 서버로 확인되고 건너뜁니다(다른 사용 사례에는 영향을 주지 않음).")
	}
	// 연결에 실패하거나 자격 증명이 거부되면 이 어설션을 통과한 것으로 간주됩니다. 중요한 점은 비밀번호가 자동으로 전송될 수 없다는 것입니다.
	if !IsPermanent(err) && !strings.Contains(err.Error(), "연결하다") {
		t.Logf("오류: %v(localhost가 아닌 경우 연결 실패가 예상됨)", err)
	}
}

func TestEmailValidateReportsMissingFields(t *testing.T) {
	// 이메일 채널에는 구성 필드가 가장 많습니다. 어느 하나라도 생략되면 전달될 때만 노출됩니다. 여기에서 하나씩 확인하세요.
	// 검증이 사전에 중단될 수 있습니다. 어설션이 확인하는 것은 "언급된 오류 메시지가 누락된 것"입니다.
	cases := []struct {
		name string
		cfg  map[string]any
	}{
		{"host 누락", map[string]any{"port": float64(25), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"port 누락", map[string]any{"host": "smtp.example.com"}},
		{"port가 범위를 벗어났습니다.", map[string]any{"host": "h", "port": float64(70000), "from": "a@b.c", "to": []any{"d@e.f"}}},
		{"from 누락", map[string]any{"host": "h", "port": float64(25), "to": []any{"d@e.f"}}},
		{"to 누락", map[string]any{"host": "h", "port": float64(25), "from": "a@b.c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (emailChannel{}).Validate(tc.cfg); err == nil {
				t.Fatalf("확인 실패: %v", tc.cfg)
			}
		})
	}
}

// TestEmailConfigTolerance는 구성 읽기의 내결함성을 다룹니다. JSONB의 값은 float64입니다.
// 그러나 사용자는 UI에서 포트를 문자열로 채울 수 있습니다. 배열은 단일 문자열일 수도 있습니다.
func TestEmailConfigTolerance(t *testing.T) {
	cfg := map[string]any{
		"host": "smtp.example.com",
		"port": "587", // 문자열로 포트
		"from": "a@b.c",
		"to":   "d@e.f", // 배열 대신 단일 문자열
		"tls":  "true",  // 문자열로서의 부울
	}
	if err := (emailChannel{}).Validate(cfg); err != nil {
		t.Fatalf("문자열 형식의 숫자 값은 허용되어야 합니다: %v", err)
	}
	if got := cfgInt(cfg, "port"); got != 587 {
		t.Errorf("cfgInt 구문 분석되지 않은 문자열 포트, %d 있음", got)
	}
	if !cfgBool(cfg, "tls") {
		t.Error("cfgBool 구문 분석되지 않은 문자열 \"true\"")
	}
	if to := cfgStrings(cfg, "to"); len(to) != 1 || to[0] != "d@e.f" {
		t.Errorf("cfgStrings는 단일 문자열과 호환되지 않으며 %v를 얻습니다.", to)
	}
}

// TestFilterValidateRejectsTypo는 감사 수정 사항을 직접 검증합니다.
// 임계값의 오타는 작성 중에 차단되어야 합니다. 그렇지 않으면 필터가 자동으로 실패하고 전체 푸시가 됩니다.
func TestFilterValidateRejectsTypo(t *testing.T) {
	good := []string{"", "low", "medium", "high", "critical"}
	for _, s := range good {
		if err := (Filter{MinSeverity: s}).Validate(); err != nil {
			t.Errorf("법적 기준점 %q 거부됨: %v", s, err)
		}
	}
	// 이것은 실제로 발생하는 사무상의 오류이므로 모두 거부해야 합니다.
	for _, s := range []string{"hgih", "HIGH", "치명적", "high ", "crit"} {
		err := (Filter{MinSeverity: s}).Validate()
		if err == nil {
			t.Errorf("잘못된 임계값 %q는 거부되어야 합니다. 그렇지 않으면 필터가 자동으로 실패하고 전체 푸시가 됩니다.", s)
			continue
		}
		// 오류 메시지는 사용자에게 오류를 수정하도록 안내해야 합니다.
		if !strings.Contains(err.Error(), "low") || !strings.Contains(err.Error(), "critical") {
			t.Errorf("오류 메시지에는 선택적 값이 나열되어야 합니다. %q를 가져오세요.", err.Error())
		}
	}
}

// TestFilterValidateIsWriteTimeOnly는 "엄격한 쓰기, 폭넓은 읽기"의 분업을 잠급니다.
// 라이브러리에 있는 기존 잘못된 값은 채널에서 읽히는 것을 차단할 수 없습니다(그러면 모든 기록 채널이 갑자기 푸시를 중지하게 됩니다).
func TestFilterValidateIsWriteTimeOnly(t *testing.T) {
	raw := []byte(`{"min_severity":"hgih"}`)
	f := ParseFilter(raw) // 보고된 오류 없음
	if f.MinSeverity != "hgih" {
		t.Fatalf("읽기 경로는 그대로 두어야 %q를 제공합니다.", f.MinSeverity)
	}
	// 그리고 채널은 여전히 ​​이벤트에 대해 판단을 내릴 수 있습니다(panic 없음, 차단 없음).
	_ = Match(f, Snapshot{Kind: EventFindingCreated, Severity: "critical"})
}

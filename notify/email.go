package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// emailDialTimeout / emailSessionTimeout는 각각 연결 설정과 전체 SMTP 세션을 제한합니다.
// net/smtp 자체에는 시간 초과 메커니즘이 없습니다. 이 두 가지가 제공되지 않으면 정체된 피어로 인해
// Post goroutine는 영구적으로 중단됩니다. dispatcher는 단일 goroutine에 의해 연속적으로 처리됩니다.
// 이는 전체 알림 시스템이 종료되는 것과 같습니다.
const (
	emailDialTimeout    = 10 * time.Second
	emailSessionTimeout = 45 * time.Second
)

// emailChannel는 SMTP 메일 배달을 구현합니다.
type emailChannel struct{}

func (emailChannel) Kind() string { return KindEmail }

// 이메일에는 플랫폼 제한이 없지만 화면을 가득 채우는 데 사용해서는 안 됩니다. 느슨한 기본값을 제공하십시오.
func (emailChannel) DefaultRatePerMin() int { return 60 }

// 비밀번호만 마스킹됩니다. SMTP 호스트, 계정 및 수신자는 비밀이 아닙니다. 마스킹하면 편집이 번거로워질 뿐입니다.
func (emailChannel) SecretKeys() []string { return []string{"password"} }

// host/port는 비밀번호를 전달할 서버를 결정합니다. tls는 전송을 암호화할지 여부를 결정합니다. 세 가지에 어떤 변화가 생기면
// 비밀번호를 다시 지정해야 함 - 그런데 "TLS 끄기" 단계에서는 자격 증명을 쉽게 변경하는 대신 명시적으로 가져와야 합니다.
func (emailChannel) DestinationKeys() []string { return []string{"host", "port", "tls"} }

func (emailChannel) Validate(cfg map[string]any) error {
	if cfgString(cfg, "host") == "" {
		return errors.New("SMTP 서버 주소 누락")
	}
	port := cfgInt(cfg, "port")
	if port <= 0 || port > 65535 {
		return errors.New("SMTP 포트가 잘못되었습니다(1-65535여야 함).")
	}
	if cfgString(cfg, "from") == "" {
		return errors.New("보낸 사람 주소가 누락되었습니다.")
	}
	if len(cfgStrings(cfg, "to")) == 0 {
		return errors.New("수신자 주소가 하나 이상 필요합니다.")
	}
	return nil
}

func (c emailChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	host := cfgString(cfg, "host")
	port := cfgInt(cfg, "port")
	from := cfgString(cfg, "from")
	to := cfgStrings(cfg, "to")
	username := cfgString(cfg, "username")
	password := cfgString(cfg, "password")
	implicitTLS := cfgBool(cfg, "tls")

	msg, err := buildEmailMessage(from, to, m)
	if err != nil {
		return 0, Permanent(err)
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	client, err := emailDial(ctx, addr, host, implicitTLS)
	if err != nil {
		return 0, err
	}
	defer client.Close()

	// STARTTLS: 피어가 지원하는 경우 업그레이드하세요. 일반 텍스트 세션에서는 자격 증명을 발급할 수 없습니다(아래 auth 설명 참조).
	if !implicitTLS {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				return 0, fmt.Errorf("STARTTLS 실패: %w", err)
			}
		}
	}
	if username != "" {
		if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
			// smtp.PlainAuth는 암호화되지 않은 연결을 통한 자격 증명 전송을 거부합니다(대상이 localhost가 아닌 경우).
			// 이는 **올바른** 보안 동작이므로 우회할 수 없지만 그 이유를 명확하게 해석해야 합니다.
			// 그렇지 않으면 사용자는 "unencrypted connection"만 보고 무엇을 해야 할지 알 수 없습니다.
			if strings.Contains(err.Error(), "unencrypted connection") {
				return 0, Permanent(fmt.Errorf("자격 증명이 거부되었습니다. 연결이 암호화되지 않았습니다. TLS를 활성화하거나, 포트 465(암시적 TLS)로 전환하거나, ＂TLS 활성화＂(%w)를 선택하세요.", err))
			}
			return 0, Permanent(fmt.Errorf("SMTP 인증 실패: %w", err))
		}
	}
	if err := client.Mail(from); err != nil {
		return 0, smtpStageError(fmt.Sprintf("발신자 %s가 거부됨", from), err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return 0, smtpStageError(fmt.Sprintf("수신자 %s가 거부됨", rcpt), err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return 0, fmt.Errorf("SMTP DATA 실패: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return 0, fmt.Errorf("이메일 본문 작성 실패: %w", err)
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("이메일 제출 실패: %w", err)
	}
	// Quit 오류는 서버에서 메시지를 수신했다는 사실에 영향을 미치지 않으므로 오류는 무시됩니다.
	_ = client.Quit()
	// 이메일에는 길이 잘림이 없으며(모든 HTML 텍스트가 전송됨) 전체 배치가 전달된 것으로 간주됩니다.
	return len(m.Items), nil
}

// emailDial는 SMTP 연결을 설정합니다.
//
// implicitTLS=true는 465로 이동합니다. 이러한 종류의 "연결은 TLS" 방법입니다. false는 25/587로 이동한 다음 명확하게 연결을 설정합니다.
// STARTTLS. 두 가지를 혼합할 수 없습니다. 465 포트 발명 greeting는 직접 연결이 끊어집니다.
//
// net/smtp의 Client가 기본을 설정하기 때문에 세션 기간은 **연결 설정**에서 설정됩니다(나중에 설정하는 대신).
// 연결은 내보내지 않은 필드에 숨겨져 있으며 외부에서 얻을 수 없습니다. 연결이 넘겨지면 사전 설정된 deadline에만 의존할 수 있습니다.
// 모든 세부 사항을 공개합니다. 여기에는 핸드셰이크 단계 중 차단도 포함됩니다.
// Control 연결 blockInternalDial 및 HTTP 시리즈 채널은 동일한 가드를 공유합니다. 그렇지 않은 경우 SMTP
// 이는 전체 SSRF 보호 세트의 간격입니다. host는 169.254.169.254 또는 127.0.0.1을 입력하여 직접 연결할 수 있습니다.
// smtp.NewClient 핸드셰이크가 실패하면 피어가 반환한 라인이 오류 last_error에 포함됩니다.
// 이는 배달 기록 인터페이스에 의해 반향되어 반맹독 읽기 기본 요소를 형성합니다. "연결 거부 vs 시간 초과"의 시간 소모적인 차이는 여전히 발생할 수 있습니다.
// 포트를 감지하는 데 사용됩니다. 다이얼링 단계는 최종 유효 지점이며 DNS 리바인딩도 포함합니다.
func emailDial(ctx context.Context, addr, host string, implicitTLS bool) (*smtp.Client, error) {
	d := &net.Dialer{Timeout: emailDialTimeout, Control: blockInternalDial}
	var conn net.Conn
	var err error
	if implicitTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("SMTP 서버에 연결하지 못했습니다: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(emailSessionTimeout))
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SMTP 핸드셰이크 실패: %w", err)
	}
	return client, nil
}

// smtpStageError SMTP 응답 코드에 따르면 특정 단계에서의 실패는 "재시도 가능"과 "영구 실패"로 구분됩니다.
//
// 구별이 필요한 이유: SMTP의 4xx와 5xx는 완전히 다른 의미를 갖습니다.
//   - 4xx(450 그레이리스팅, 451 로컬 오류, 452 불충분한 저장 공간)는 **일시적** 거부입니다.
//     일반적인 접근 방식은 나중에 다시 시도하는 것입니다. 특히 그레이리스팅의 경우 거의 처음으로 이 문제를 접하게 됩니다.
//   - 5xx(550 사용자가 존재하지 않음, 553 주소가 불법임)는 영구 거부이며, 재시도는 의미가 없습니다.
//
// 영구적인 오류인 경우 그레이리스팅이 활성화된 메일 서버는 **모든** 푸시가 처음으로 발생하도록 합니다.
// 시도한 후에는 failed에 빠지며 이러한 종류의 실패는 정확히 자동 재시도가 가장 효과적인 시나리오입니다.
// 응답 코드는 오류 텍스트의 처음 세 자리를 사용합니다. 코드를 얻을 수 없는 경우 재시도 가능으로 처리됩니다(한 번 더 시도하는 것이 좋습니다.
// 분석할 수 없다는 이유만으로 가능한 일시적인 오류를 판단하지 마세요.
func smtpStageError(what string, err error) error {
	code := smtpReplyCode(err.Error())
	if code >= 500 && code < 600 {
		return Permanent(fmt.Errorf("%s: %w", what, err))
	}
	return fmt.Errorf("%s: %w", what, err)
}

// smtpReplyCode SMTP 오류 텍스트에서 응답 코드의 선행 3자리를 가져옵니다. 그렇지 않은 경우 0이 반환됩니다.
// net/smtp는 오류 코드 필드를 내보내지 않으며 텍스트에서만 가져올 수 있습니다. 형식은 "450 4.7.1..."입니다.
func smtpReplyCode(text string) int {
	if len(text) < 3 {
		return 0
	}
	n, err := strconv.Atoi(text[:3])
	if err != nil {
		return 0
	}
	return n
}

// buildEmailMessage 완전히 조립된 RFC 5322 메일입니다.
//
// 텍스트가 base64로 인코딩되는 이유는 두 가지입니다. 첫째, SMTP는 단일 라인이 1000바이트를 초과해서는 안 된다고 규정하는 반면, HTML는
// 본문(특히 요약 이메일)은 줄이 매우 길어지는 경향이 있습니다. 둘째, base64는 당연히 "."로 시작하지 않습니다.
// 줄을 사용하면 점 SMTP를 탈출하는 수고를 덜 수 있습니다.
func buildEmailMessage(from string, to []string, m Message) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// 한국어 테마는 RFC 2047로 인코딩되어야 합니다. 그렇지 않으면 클라이언트에서 잘못된 문자로 표시됩니다.
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", htmlTitle(m)))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n")
	// 메시지 길이에는 엄격한 제한이 없으므로 본문이 잘리지 않습니다.
	b.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(htmlBody(m, 0)))
	// base64는 RFC 2045를 준수하여 76자에 따라 줄을 바꿉니다.
	for len(encoded) > 76 {
		b.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	b.WriteString(encoded + "\r\n")
	return b.String(), nil
}

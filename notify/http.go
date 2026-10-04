package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// allowLocalTargets는 루프백/링크 로컬 주소로의 메시지 전달이 허용되는지 여부를 결정합니다.
//
// 기본적으로 거부됩니다. 이 주소는 IM 로봇이나 공용 메일 서버가 나타나는 위치는 아니지만
// 적중 대상은 매우 민감합니다. 동일한 시스템에 있는 다른 서비스의 관리 포트와 클라우드 환경의 메타데이터 엔드포인트입니다.
// (169.254.169.254, 인스턴스 자격 증명을 읽습니다). 배송주소는 관리자가 지정했는데 하나가 XSS/CSRF 입니다.
// 빌린 관리 세션 또는 동일한 JWT를 공유하는 두 번째 사람은 구성을 변경하여 응답 내용을 다시 읽을 수 있습니다.
// ——doJSON는 4xx/5xx 응답 본문의 처음 200바이트를 last_error에 쓰고 전송 기록 인터페이스를 작성합니다.
// 반맹독(semi-blind reading) 프리미티브인 내용을 반향할 것입니다.
//
// 그러나 "로컬 SMTP 릴레이"(127.0.0.1:25의 postfix)는 자체 작성 메일의 일반적인 구성입니다.
// 모든 것에 맞는 하나의 크기는 사람들을 함정에 빠뜨릴 것입니다. 따라서 하드 코딩된 탈출구 대신 명시적인 탈출구를 남겨두십시오.
// 허용하려면 ARTEX_NOTIFY_ALLOW_LOCAL=1을 설정하세요.
//
// 테스트에서 명시적으로 열 수 있도록 AllowLocalTargetsEnv로 내보냈습니다. 이 패키지는 server 패키지와 동일합니다.
// 사용 사례에서는 127.0.0.1에서 다수의 httptest 가짜 수신기를 사용합니다. 열리지 않으면 경비원이 모두 막습니다.
const AllowLocalTargetsEnv = "ARTEX_NOTIFY_ALLOW_LOCAL"

func allowLocalTargets() bool {
	v := strings.TrimSpace(os.Getenv(AllowLocalTargetsEnv))
	return v == "1" || strings.EqualFold(v, "true")
}

// isBlockedDialIP는 대상 IP가 "기본적으로 전달이 허용되지 않는" 주소 세그먼트에 속하는지 여부를 보고합니다.
//
// 루프백, 로컬 링크(클라우드 메타데이터 169.254.169.254 포함), 지정되지 않음 및 멀티캐스트만 거부합니다.
// **거부하지 마십시오** RFC1918 개인 네트워크: 자체 구축된 인트라넷 Mattermost / SMTP 릴레이는 매우 일반적인 법적 사용법입니다.
// 이를 모두 차단하면 실제 환경에서 해당 기능을 사용할 수 없게 됩니다. 이 절충안은 의도적인 것입니다.
// 보호는 매우 민감한 대상을 차단해야 하며 동시에 정상적인 배포를 폐지해서는 안 됩니다.
func isBlockedDialIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4-mapped IPv6(::ffff:127.0.0.1)는 판단되기 전에 IPv4로 복원되어야 합니다. 그렇지 않으면 검사가 우회됩니다.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}

// blockInternalDial는 연결이 설정될 때 http.Transport 다이얼러의 Control 후크입니다**
// 목적지 주소를 확인하세요.
//
// 구성을 저장할 때만 확인하지 않고 전화 접속 단계에서 설정하는 이유는 이것이 최종 효과 지점입니다.
// 동시에 구성 확인을 우회하는 두 가지 상황을 다룹니다. - DNS 리바인딩(공용 네트워크 IP로 해결,
// 실제로 연결되면 인트라넷으로 확인하고 리디렉션(호스트 간 점프는 거부했지만 동일 호스트 점프는 거부함)
// 여전히 다른 경로를 가리킬 수 있습니다).
func blockInternalDial(_, address string, _ syscall.RawConn) error {
	if allowLocalTargets() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("대상 주소 %q를 확인할 수 없습니다.", host)
	}
	if isBlockedDialIP(ip) {
		return fmt.Errorf("로컬/링크 로컬 주소 %s로 배송 거부(꼭 로컬 서비스로 배송해야 하는 경우 %s=1로 설정)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

// notifyTransport는 기본 Transport를 기반으로 하나의 다이얼 가드만 추가합니다.
// 모든 기본 튜닝(연결 풀, HTTP/2, 시간 초과, proxy 등)을 유지하려면 Clone를 사용합니다.
// 검사를 추가하기 위해 다른 동작을 변경하지 마세요.
var notifyTransport = func() *http.Transport {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	clone := t.Clone()
	clone.DialContext = (&net.Dialer{Timeout: 10 * time.Second, Control: blockInternalDial}).DialContext
	return clone
}()

// httpClient는 모든 채널 전송에 공통되는 클라이언트입니다.
//
// 의도적으로 프로젝트의 전역 종료 프록시(server 측의 GlobalProxy)를 재사용하지 **않습니다**: 해당 프록시는 침투용입니다.
// 대상 트래픽은 불안정한 터널을 사용하는 경우가 많으므로 대상 네트워크의 지터로 인해 알림 가용성이 가로채져서는 안 됩니다.
// IM를 눌러 직접 연결할 수 있습니다. 시간 초과는 15초로 설정됩니다. 이보다 느린 피어는 사실상 실패한 상태입니다.
//
// 호스트 간 리디렉션 거부: 이 기능의 전달 주소는 모두 "고정 endpoint" 형식입니다.
// 다른 호스트로 리디렉션합니다. 및 이들 회사의 자격 증명(DingTalk의 access_token, Qiwei의 key, Telegram)
// bot token)**는 URL**에 있으며, 호스트 간 점프를 따르는 것은 자격 증명을 리디렉션 대상으로 전달하는 것과 같습니다. 동일한 호스트
// 점프(예: 후행 슬래시)는 여전히 허용됩니다.
var httpClient = &http.Client{
	Timeout:   15 * time.Second,
	Transport: notifyTransport,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("리디렉션이 너무 많습니다.")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("호스트 간 리디렉션 거부(%s → %s)", via[0].URL.Host, req.URL.Host)
		}
		return nil
	},
}

// respBodyLimit는 읽기 응답 본문의 크기를 제한합니다. 피어가 비정상일 때 매우 큰 콘텐츠를 다시 뱉어낼 수 있지만 우리는 단지 필요합니다.
// 오류 코드와 간단한 오류 설명은 배송 기록에 표시되는 데 사용됩니다.
const respBodyLimit = 8 << 10

// doJSON는 요청을 보내고 응답 본문을 반환합니다(길이는 제한됨).
//
// payload가 nil인 경우 빈 body가 전송됩니다(GET 또는 플랫폼에 body가 필요하지 않은 시나리오에서 사용됨).
// headers에 있는 키값은 그대로 첨부되어 일반 Webhook의 커스텀 헤더로 사용됩니다.
//
// 오류 분류는 이 기능의 핵심 책임입니다. 네트워크 계층 오류 및 5xx/408/429는 "재시도 가능"으로 분류됩니다.
// 나머지 4xx는 "영구 실패"로 분류됩니다. 403을 재시도하면 동일한 오류로 로그가 세 번 플러시됩니다.
func doJSON(ctx context.Context, method, url string, headers map[string]string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			// 직렬화 실패는 로컬 bug(구성 필드 유형이 올바르지 않음)이며 재시도해도 개선되지 않습니다.
			return nil, Permanent(fmt.Errorf("요청 본문 구성 실패: %w", err))
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		// URL 불법 - 사용자가 잘못된 주소를 입력했을 가능성이 높으며 이는 영구적인 오류입니다.
		// err는 여기에서도 투명하게 전송될 수 없습니다. url.Parse의 오류 텍스트에는 전체 주소가 포함되어 있습니다.
		return nil, Permanent(fmt.Errorf("잘못된 요청 주소: %s", redactRequestTarget(url)))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		// 연결이 거부됨, DNS 오류, 시간 초과 - 대부분 일시적인 오류이며 백오프한 후 다시 시도하세요.
		//
		// 잘못된 텍스트는 외부로 전파되기 전에 둔감화되어야 합니다. 이유: http.Client.Do는 *url.Error를 반환합니다.
		// Error()는 `Op "전체URL": 근본적인 오류`이며 이 기능에 대한 자격 증명은 URL**에 있습니다.
		// (DingTalk access_token, Qiwei key, Feishu hook id, Telegram /bot<token>/).
		// 민감도를 낮추지 않으면 자격 증명은 다음 오류와 함께 네 곳으로 스트리밍됩니다: notification_deliveries
		// last_error(데이터베이스에 일반 텍스트 삭제), 전송 기록 인터페이스 응답(**바이패스 채널 구성 마스크**),
		// 서버 로그 및 테스트 전송 인터페이스에서 프런트 엔드로 반환된 502 텍스트입니다.
		return nil, fmt.Errorf("요청 실패: %s", redactTransportError(err))
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, respBodyLimit))
	if readErr != nil {
		return nil, fmt.Errorf("응답을 읽지 못했습니다: %w", readErr)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	// 429(현재 제한) 및 408(시간 초과)은 다시 시도할 가치가 있습니다. 나머지 4xx는 구성 또는 권한 문제이며 재시도는 의미가 없습니다.
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout {
		return nil, fmt.Errorf("상대방의 현재 제한 또는 시간 초과(HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("상대방의 서비스가 비정상입니다(HTTP %d): %s", resp.StatusCode, snippet(raw))
	}
	return nil, Permanent(fmt.Errorf("상대방이 요청을 거부했습니다(HTTP %d): %s", resp.StatusCode, snippet(raw)))
}

// snippet 오류 메시지에 대한 응답 본문을 짧은 텍스트 줄로 압축합니다. 응답에는 줄 바꿈과 많은 공백이 포함될 수 있습니다.
// last_error를 직접 삽입하면 배송 내역 페이지의 레이아웃이 축소됩니다.
func snippet(raw []byte) string {
	return OneLine(string(raw), 200)
}

// redactRequestTarget 배송주소를 눌러주세요「scheme://host/…」，오류 메시지의 경우。
//
// 이것은 이 패키지의 유일한 주소 둔감화 구경이며 의도적으로 **충분히** 거칠게 만들어졌습니다. scheme 및 host를 제외하고,
// 나머지는 폐기하십시오. 그 이유는 URL의 어떤 세그먼트가 자격 증명인지 확인하는 "일반적이고 안전한" 방법이 없기 때문입니다.
//
//	DingTalk 자격 증명은 query /robot/send?access_token=xxx에 있습니다.
//	Qiwei 자격 증명은 query /cgi-bin/webhook/send?key=xxx에 있습니다.
//	Feishu 자격 증명은 경로 끝에 있습니다** /open-apis/bot/v2/hook/<hook_id>
//	Telegram 자격 증명이 경로 중간에 있습니다** /bot<token>/sendMessage
//
// "유용한 부분만 유지"하려면 채널별로 패치를 해야 하며, 하나라도 누락되면 자격 증명이 유출됩니다.
// host를 유지하면 문제 해결에 충분합니다(DNS를 구문 분석할 수 없거나 연결할 수 없거나 인증서가 올바르지 않은 경우 찾을 수 있음).
// 특정 로봇은 채널 구성의 마스크 꼬리 번호 프롬프트로 식별됩니다.
//
// 구문 분석에 실패하면 고정된 자리 표시자가 반환됩니다. 원래 문자열은 다시 에코되지 않습니다.
func redactRequestTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(주소를 확인할 수 없습니다)"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// redactTransportError 전송 계층 오류에서 주소를 제거하고 근본 원인만 남깁니다.
//
// *url.Error의 구조는 {Op, URL, Err}이며, Error()는 URL를 함께 입력합니다.
// 여기서는 Err 필드가 명시적으로 선택되고 해당 Error()가 우회됩니다. 나중에 문자열 교체를 수행하는 것보다 더 안정적입니다.
// 교체는 URL 인코딩/이스케이프의 다양한 변형을 올바르게 처리해야 하기 때문에 놓치기 쉽습니다.
func redactTransportError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		host := ""
		if u, parseErr := url.Parse(uerr.URL); parseErr == nil {
			host = u.Host
		}
		if uerr.Err != nil {
			return fmt.Sprintf("%s %s: %s", uerr.Op, host, uerr.Err)
		}
		return fmt.Sprintf("%s %s: 알 수 없는 오류", uerr.Op, host)
	}
	// 비*url.Error(예: 리디렉션 정책에 의해 반환된 오류)에는 모두 감도가 낮은 주소가 포함될 수도 있습니다.
	return redactURLsInText(err.Error())
}

// redactURLsInText는 텍스트에 나타나는 http(s) 주소를 둔감한 형식으로 바꿉니다.
//
// 구조화된 필드를 가져올 수 없는 오류(리디렉션 전략 오류, 타사 라이브러리의 사용자 정의 오류)를 잡는 데 사용됩니다.
// 공백과 따옴표로 구분된 http/https 접두사만 인식됩니다. 주소에는 이러한 두 가지 유형의 문자가 포함되지 않습니다.
func redactURLsInText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		if strings.HasPrefix(rest, "http://") || strings.HasPrefix(rest, "https://") {
			end := len(rest)
			if j := strings.IndexAny(rest, " \t\n\"'"); j >= 0 {
				end = j
			}
			b.WriteString(redactRequestTarget(rest[:end]))
			i += end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

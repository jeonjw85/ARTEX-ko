package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel는 DingTalk 맞춤형 로봇을 구현합니다.
//
// 플랫폼 기능(여기서 구현 선택을 결정함):
//   - 단일 로봇에 대한 현재 제한은 분당 20개 메시지이며, 초과하는 메시지는 자동으로 삭제됩니다(HTTP는 여전히 200개일 수 있음).
//     따라서 전류 제한은 클라이언트 측에서 수행되어야 합니다. DefaultRatePerMin를 참조하세요.
//   - 세 가지 보안 설정 중 하나를 선택하세요: 서명 추가 / 사용자 정의 키워드 / IP 화이트리스트. 서명은 의존하지 않는 유일한 것입니다
//     메시지 콘텐츠 구성표이므로 서명만 지원됩니다(세 가지 중 하나도 열지 않는 기본 webhook도 지원됨).
//   - HTTP 200은 성공/실패에 대해 반환되며 body에서 errcode로 구별됩니다. - errcode는 확인되지 않습니다.
//     배달 실패는 성공으로 기록됩니다.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// DingTalk의 Webhook 주소에는 access_token가 포함되어 있는데, 이 자체가 크리덴셜이므로 전체적으로 마스킹되어 있습니다.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// 대상은 DingTalk의 Webhook 주소 자체입니다. 주소를 변경할 때 동시에 새 주소에 대한 서명 키를 다시 지정해야 합니다.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("Webhook 주소 누락")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("Webhook 잘못된 주소: %w", err)
	}
	return nil
}

// Send는 메시지를 한 번 전달합니다. 백링크가 있고 싱글인 경우에는 ActionCard(버튼 포함)를 사용하고, 그렇지 않은 경우에는 markdown를 사용합니다.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// DingTalk markdown의 텍스트에는 명확한 바이트 상한선이 없지만 증거 필드의 비정상적인 확장을 방지하기 위해 상한선 보호가 여전히 제공됩니다.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "세부 사항을 확인하세요",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// DingTalk는 200개의 응답으로 비즈니스 오류를 숨깁니다.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("DingTalk 응답 구문 분석 실패: %w(%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000은 서명 확인 실패를 의미하고, 310000은 키워드 불일치를 의미하며 모두 구성 오류입니다.
		// 다시 시도해도 저절로 치유되지는 않습니다.
		return 0, Permanent(fmt.Errorf("DingTalk에서 오류 %d: %s를 반환합니다.", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL는 공식 서명 규칙에 따라 webhook에 timestamp 및 sign 매개변수를 추가합니다.
//
// 규칙: 서명할 문자열 = timestamp + "\n" + secret, HMAC-SHA256의 ** 키도 secret**입니다.
// 결과는 base64와 URL 인코딩입니다. timestamp는 밀리초입니다. secret가 비어 있으면 그대로 반환됩니다.
// 서명이 활성화되지 않은 로봇을 지원합니다.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// 투명 전송 없음 err: url.Parse의 오류 텍스트에는 전체 주소(access_token 포함)가 포함되어 있습니다.
		return "", fmt.Errorf("Webhook 주소 구문 분석 실패: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL는 주소가 사용 가능하고 프로토콜이 지원되며 내부 네트워크가 문자 그대로 IP 대상으로 판단되는지 확인합니다.
//
// 주목해야 할 두 가지 사항:
//
//  1. **오류 메시지는 민감도를 줄여야 합니다**. url.Parse 자체는 *url.Error를 반환하고 Error()를 반환합니다.
//     **완전한 원본 주소**, 이 기능을 갖춘 회사의 주소에는 자격 증명이 내장되어 있습니다(DingTalk access_token,
//     Qiwei key, Telegram, Feishu hook id의 bot token). 예전에는 직접 `return err`였지만,
//     따라서 "주소 형식이 불법입니다." 오류로 인해 자격 증명이 제거되고 400 응답이 테스트 인터페이스로 흘러갔습니다.
//     last_error, 각 배송에 대한 서버 로그 및 배송 내역 인터페이스입니다.
//
//  2. **말 그대로 IP가 인트라넷을 직접 결정**하고, 도메인 이름은 전화 접속 단계에서 결정하도록 남겨둡니다(blockInternalDial가 최종 결정)
//     유효점은 DNS 리바인딩도 커버할 수 있습니다. 구성을 저장할 때 프롬프트를 표시하려면 이 작업을 한 번 수행하십시오.
//     첫 번째 배달이 실패할 때까지 기다리는 대신.
//
// 구속 계약은 방어적이다：file:///gopher:// 그런 일이 일어날 것입니다 http.Client 예상치 못한 생산
// 동작(비록 scheme 검사로 차단되었지만 이 쪽을 놔둘 이유가 없습니다).
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("주소를 확인할 수 없습니다(%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("http/https만 지원, %q 수신", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("호스트 이름이 누락되었습니다.")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("로컬/링크 로컬 주소 %s로 배송 거부(꼭 로컬 서비스로 배송해야 하는 경우 %s=1로 설정)", ip, AllowLocalTargetsEnv)
	}
	return nil
}

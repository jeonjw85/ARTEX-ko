package notify

import (
	"net/url"
	"testing"
	"time"
)

// 서명 참조 값은 OpenSSL에 의해 독립적으로 계산되며 이 패키지 자체 구현에 의해 생성되지 않습니다.
// 그렇지 않으면 "코드가 변경되지 않았다"는 것만 증명할 수 있으며 "알고리즘이 정확하다"는 것은 증명할 수 없습니다.
//
//	TS=1700000000000, SECRET=SECtest123
//	DingTalk: printf '%s\n%s' "$TS" "$SECRET" | openssl dgst -sha256 -hmac "$SECRET" -binary | openssl base64 -A
//	      -> w3RMHXzixTMdzr8OHJUmVLS4IoPJVdu+Ut1LE48MePE=
//	이름: printf '' | openssl dgst -sha256 -hmac "$(printf '%s\n%s' "$TS" "$SECRET")" -binary | openssl base64 -A
//	      -> Hd4xFWQU6R6ad4nzy4ETIznzlqebqH7xcTFVmONTudo=
const (
	signTestTSMillis = int64(1700000000000)
	signTestSecret   = "SECtest123"
	dingTalkExpected = "w3RMHXzixTMdzr8OHJUmVLS4IoPJVdu+Ut1LE48MePE="
	feishuExpected   = "Hd4xFWQU6R6ad4nzy4ETIznzlqebqH7xcTFVmONTudo="
)

func TestDingTalkSignMatchesReference(t *testing.T) {
	got, err := dingTalkSignedURL("https://oapi.dingtalk.com/robot/send?access_token=tok", signTestSecret, time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatalf("서명 실패: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("출력 주소를 확인할 수 없습니다: %v", err)
	}
	q := u.Query()
	if q.Get("sign") != dingTalkExpected {
		t.Errorf("서명이 \n와 일치하지 않습니다. 예상 %s\n가 %s를 받았습니다.", dingTalkExpected, q.Get("sign"))
	}
	if q.Get("timestamp") != "1700000000000" {
		t.Errorf("타임스탬프는 밀리초 단위여야 하며 %q를 얻으려면 있는 그대로 가져와야 합니다.", q.Get("timestamp"))
	}
	// 원본 query 매개변수(access_token)는 서명으로 덮어쓸 수 없습니다.
	if q.Get("access_token") != "tok" {
		t.Errorf("원래 query 매개변수는 손실되고 %q가 획득됩니다.", q.Get("access_token"))
	}
}

func TestFeishuSignMatchesReference(t *testing.T) {
	got := feishuSign("1700000000000", signTestSecret)
	if got != feishuExpected {
		t.Errorf("서명이 \n와 일치하지 않습니다. 예상 %s\n가 %s를 받았습니다.", feishuExpected, got)
	}
}

// TestSignAlgorithmsDiffer는 둘 사이의 알고리즘 차이를 잠급니다. 그들은 우연히 서로의 매개변수 순서가 되었습니다
// (DingTalk key=secret, Feishu key=서명할 문자열), 다른 회사에서 복사할 경우 필연적으로 인증에 실패합니다.
// 이 사용 사례는 향후 리팩토링에서 두 가지가 동일한 기능으로 병합되지 않도록 보장합니다.
func TestSignAlgorithmsDiffer(t *testing.T) {
	ts := "1700000000000"
	dingURL, err := dingTalkSignedURL("https://example.com/hook", signTestSecret, time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatal(err)
	}
	dq, _ := url.Parse(dingURL)
	if dq.Query().Get("sign") == feishuSign(ts, signTestSecret) {
		t.Fatal("DingTalk와 Feishu의 서명이 동일하여 둘 중 하나가 알고리즘을 잘못 구현했음을 나타냅니다.")
	}
}

func TestDingTalkNoSecretLeavesURLUntouched(t *testing.T) {
	// 서명이 활성화되지 않은 로봇: timestamp/sign 매개변수는 무의식적으로 추가될 수 없습니다.
	const hook = "https://oapi.dingtalk.com/robot/send?access_token=tok"
	got, err := dingTalkSignedURL(hook, "", time.UnixMilli(signTestTSMillis))
	if err != nil {
		t.Fatal(err)
	}
	if got != hook {
		t.Fatalf("secret가 구성되지 않은 경우 주소를 변경하면 안 되며 %q를 얻습니다.", got)
	}
}

func TestValidateHTTPURL(t *testing.T) {
	ok := []string{"https://example.com/hook", "http://10.0.0.1:8080/x?y=1"}
	for _, s := range ok {
		if err := validateHTTPURL(s); err != nil {
			t.Errorf("%q는 허용되어야 합니다: %v", s, err)
		}
	}
	// file:// 그런 것들은 공개하면 안 된다.——http.Client 기대 이상으로 처리。
	bad := []string{"", "file:///etc/passwd", "ftp://example.com", "https://", "gopher://x"}
	for _, s := range bad {
		if err := validateHTTPURL(s); err == nil {
			t.Errorf("%q는 거부되어야 합니다", s)
		}
	}
}

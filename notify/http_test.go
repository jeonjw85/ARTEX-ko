package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// 이 문서에서는 doJSON에 대한 HTTP 레이어 오류 분류를 다룹니다.
//
// 별도로 테스트해야 하는 이유: 각 채널 어댑터는 플랫폼 자체의 비즈니스 오류 코드(DingTalk errcode,
// Feishu code, Telegram ok 필드), **HTTP 레이어**의 그레이딩은 doJSON에 대해 균일하게 수행됩니다.
// 이 둘은 두 개의 독립적인 방어선입니다. 이것이 없으면 503을 반환하는 전송 게이트웨이는 영구적인 실패로 간주됩니다.
// 재시도를 포기하세요. 403은 재시도 가능한 것으로 간주되며 3회 후퇴는 소용이 없습니다.

func replyServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoJSONClassifiesHTTPStatus(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		permanent bool
	}{
		{"200 성공은 실수가 아니다", 200, false},
		{"429 현재 제한을 다시 시도할 수 있습니다.", 429, false},
		{"408 요청 시간이 초과되었으며 다시 시도할 수 있습니다.", 408, false},
		{"500 서버 오류 재시도 가능", 500, false},
		{"502 게이트웨이 오류를 다시 시도할 수 있습니다.", 502, false},
		{"503 서비스를 이용할 수 없습니다. 다시 시도해 주세요.", 503, false},
		{"400 매개변수 오류 영구 실패", 400, true},
		{"401 인증 실패, 영구 실패", 401, true},
		{"403 금지된 액세스가 영구적으로 실패했습니다.", 403, true},
		{"404 주소가 존재하지 않습니다. 영구적인 실패.", 404, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := replyServer(t, tc.status, `{"detail":"upstream says no"}`)
			_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
			if tc.status < 300 {
				if err != nil {
					t.Fatalf("2xx는 오류를 보고하지 않아야 합니다: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("2xx가 아니면 오류가 보고되어야 합니다.")
			}
			if got := IsPermanent(err); got != tc.permanent {
				t.Fatalf("HTTP permanent of %d 결정 오류: 예상된 %v에서 %v(%v)를 얻었습니다.",
					tc.status, tc.permanent, got, err)
			}
			// 상태 코드는 오류에 나타나야 합니다. 그렇지 않으면 사용자는 자신이 잘못 구성했는지 또는 상대방이 전화를 끊었는지 확인할 수 없습니다.
			// 영어로 Go 대신 숫자를 주장하십시오. StatusText: 이 패키지의 사본은 한국어로 되어 있습니다.
			// (나머지 프로젝트와 일관됨) 숫자는 언어 독립적이고 어설션이 안정적인 부분입니다.
			if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
				t.Errorf("오류 메시지에는 HTTP 상태 코드 %d가 포함되어야 하며 %v를 받아야 합니다.", tc.status, err)
			}
		})
	}
}

// TestDoJSONIncludesResponseSnippet는 snippet를 포함합니다. 피어가 반환한 오류 설명을 다시 가져와야 합니다.
// 그렇지 않으면 사용자는 "실패"만 알고 피어가 거부한 이유를 알 수 없습니다.
func TestDoJSONIncludesResponseSnippet(t *testing.T) {
	srv := replyServer(t, 400, `{"error":"invalid webhook token"}`)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("오류가 보고되어야 합니다.")
	}
	if !strings.Contains(err.Error(), "invalid webhook token") {
		t.Errorf("오류 메시지는 지침을 피어에게 다시 가져오고 %v를 가져옵니다.", err)
	}
}

// TestDoJSONSnippetIsSingleLineAndBounded는 snippet의 형식을 제한합니다.
// 클라이언트에 대한 응답은 그대로 last_error 열과 프런트 엔드 테이블에 포함됩니다. 행이 여러 개이거나 길이가 너무 많으면 레이아웃이 손상되고 로드됩니다.
func TestDoJSONSnippetIsSingleLineAndBounded(t *testing.T) {
	// 줄 바꿈, 탭 및 5000자의 매우 긴 콘텐츠가 포함된 응답입니다.
	long := strings.Repeat("x", 5000)
	srv := replyServer(t, 500, "line1\nline2\r\n\tline3 "+long)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("오류가 보고되어야 합니다.")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\r\n\t") {
		t.Errorf("오류 메시지는 한 줄로 압축되어 %q를 가져옵니다.", msg)
	}
	// snippet 제한은 200자 + 고정 접두사이며 총계는 원래 응답보다 훨씬 작아야 합니다.
	if len(msg) > 400 {
		t.Errorf("오류 메시지가 너무 길어서(%d 바이트) snippet: %q로 잘라야 합니다.", len(msg), msg)
	}
}

// TestDoJSONRejectsOversizedResponse 읽기 상한이 있는지 확인: 피어가 비정상적으로 큰 콘텐츠를 반환하는 경우
// 전체 응답을 메모리로 읽을 수 없습니다(전송 기록의 각 항목에 대해 last_error의 복사본이 저장됩니다).
func TestDoJSONRejectsOversizedResponse(t *testing.T) {
	huge := strings.Repeat("A", 1<<20) // 1 MiB
	srv := replyServer(t, 400, huge)
	_, err := doJSON(context.Background(), "GET", srv.URL, nil, nil)
	if err == nil {
		t.Fatal("오류가 보고되어야 합니다.")
	}
	if len(err.Error()) > 400 {
		t.Errorf("너무 큰 응답은 길이를 제한하고 잘라서 읽어야 합니다. 오류 메시지 길이는 %d입니다.", len(err.Error()))
	}
}

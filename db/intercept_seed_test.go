package db

import (
	"regexp"
	"testing"
)

// 내장된 "클래스 인터페이스 경로 삭제" 규칙은 전체 tool_input JSON 문자열과 일치하므로 사용 사례에서는 직접 사용합니다.
// JSON의 형태가 주어지며 이는 실제로 Interceptor에서 얻은 subject와 일치합니다.
func TestDeleteEndpointPathPattern(t *testing.T) {
	re := regexp.MustCompile(deleteEndpointPathPattern)

	hit := []string{
		`{"command":"curl -s 'http://t.com/api/user/delete?id=1'"}`,    // GET 삭제 인터페이스
		`{"command":"curl -X POST http://t.com/admin/delete -d id=1"}`, // POST 삭제 인터페이스
		`{"command":"curl 'http://t.com/api/deleteAll'"}`,
		`{"command":"curl 'http://t.com/api/delete_user?id=1'"}`,
		`{"command":"curl 'http://t.com/api/delete-user?id=1'"}`,
		`{"url":"http://t.com/api/remove?id=1"}`,
		`{"command":"curl http://t.com/files/unlink/3"}`,
		`{"command":"curl http://t.com/api/del?id=2"}`,
		`{"command":"curl -X POST http://t/v1/erase"}`,
		`{"command":"curl http://t/admin/destroyAll"}`, // v1의 경로 규칙은 접미사를 허용하지 않습니다. 여기에 접미사를 추가하세요.
	}
	for _, s := range hit {
		if !re.MatchString(s) {
			t.Errorf("맞췄다가 놓으면 반응함: %s", s)
		}
	}

	// /delivery 및 /details와 같은 읽기 전용 경로가 실수로 차단되는 것을 방지하려면 동사 뒤에 구분 기호가 와야 합니다.
	miss := []string{
		`{"command":"curl 'http://t.com/api/delivery?id=1'"}`,
		`{"command":"curl 'http://t.com/order/details'"}`,
		`{"command":"curl 'http://t.com/api/delta/sync'"}`,
		`{"command":"curl 'http://t.com/user/delegate'"}`,
		`{"command":"curl 'http://delete.example.com/'"}`, // 삭제 동사가 경로 대신 도메인 이름에 나타납니다.
		`{"command":"curl 'http://t.com/remote/status'"}`,
		`{"command":"nmap -p80 10.0.0.1"}`,
	}
	for _, s := range miss {
		if re.MatchString(s) {
			t.Errorf("잘못된 블록: %s", s)
		}
	}
}

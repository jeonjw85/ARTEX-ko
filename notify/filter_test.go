package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// 잘못된 JSON, 빈 입력, 잘못된 유형의 필드 - 모두 0 값 Filter로 변질되어야 합니다.
	// 그것은 "필터링 없음"입니다. 이 불변성은 "나는 놓치는 것보다 더 많은 것을 밀고 싶다"의 출발점입니다.
	// 이것이 오류 보고 또는 세미 파싱으로 변경되면 사용자가 문자와 일치하지 않으면 모든 고위험 알림이 자동으로 삭제됩니다.
	cases := []struct {
		name string
		raw  string
	}{
		{"빈 입력", ""},
		{"불법적인 JSON", `{not json`},
		{"잘린 JSON", `{"min_severity":`},
		{"유형 불일치", `{"min_severity": 123, "task_ids": "abc"}`},
		{"최상위 수준은 배열입니다.", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("잘못된 구성은 0 값 Filter로 변질되어 %+v가 발생합니다.", f)
			}
			// 0 값 Filter는 모든 이벤트에 도달해야 합니다.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("0 값 Filter는 모든 이벤트에 도달해야 합니다.")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// 알 수 없는 수준 서수는 0이며 비어 있지 않은 임계값에 의해 차단되어야 합니다(의심스러운 경우에는 푸시하지 마세요).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: %v가 %v를 얻을 것으로 예상합니다.", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL 주입",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"빈 범위 = 제한 없음", Filter{}, true},
		{"미션 히트", Filter{TaskIDs: []int64{7}}, true},
		{"임무 실패", Filter{TaskIDs: []int64{8}}, false},
		{"히트를 포함한 다양한 작업 선택", Filter{TaskIDs: []int64{8, 7}}, true},
		{"자산이 교차합니다.", Filter{AssetIDs: []int64{20, 99}}, true},
		{"자산 교차 없음", Filter{AssetIDs: []int64{99}}, false},
		{"작업과 자산이 동시에 적중", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"작업이 적중했지만 자산이 누락되었습니다.", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("%v가 %v를 얻을 것으로 예상", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"include가 비어 있음=모두 수락", Filter{}, "모든 유형", true},
		{"include 히트", Filter{VulnClassInclude: []string{"SQL"}}, "SQL 주입", true},
		{"include 미스", Filter{VulnClassInclude: []string{"명령 실행"}}, "SQL 주입", false},
		{"include 여러 단어 중 하나가 적중됩니다.", Filter{VulnClassInclude: []string{"명령 실행", "SQL"}}, "SQL 주입", true},
		{"대소문자를 구분하지 않음", Filter{VulnClassInclude: []string{"sql"}}, "SQL 주입", true},
		{"exclude 적중 시 제외", Filter{VulnClassExclude: []string{"정보 유출"}}, "정보 유출", false},
		{"exclude 맞지 않으면 패스", Filter{VulnClassExclude: []string{"정보 유출"}}, "SQL 주입", true},
		// 제외가 포함보다 우선합니다. 동시에 적중하면 아웃이 발생합니다.
		{"제외가 포함보다 우선합니다.", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"주입"},
		}, "SQL 주입", false},
		// 순수한 공백 키워드는 무시해야 합니다. 그렇지 않으면 "공백을 포함하는 모든 문자열과 일치"로 변질됩니다.
		{"빈 키워드는 무시됩니다.", Filter{VulnClassInclude: []string{"", "  "}}, "SQL 주입", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("%v가 %v를 얻을 것으로 예상", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// 기본값 꺼짐: 대부분의 사람들이 "푸시 취약점"이라고 부르는 것은 상태 계정이 아닌 새로운 취약점의 발견을 의미합니다.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("활성화되지 않은 경우 상태 변경 이벤트를 건너뛰어야 합니다.")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("on_status_change를 켠 후 상태 변경 이벤트가 발생해야 합니다.")
	}
	// 생성 이벤트는 on_status_change의 영향을 받지 않습니다.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("생성 이벤트는 on_status_change에 의존해서는 안 됩니다.")
	}
}

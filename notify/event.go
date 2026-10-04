package notify

// Snapshot는 JSONB 시리즈 notification_events.snapshot의 계약입니다. 쓰기면은 db 레이어입니다.
// 취약점 로깅 트랜잭션의 경우 리더는 server 레이어의 전달 엔진 및 필터 매칭입니다. 정의는 다음과 같기 때문에 이 패키지에 배치됩니다.
// "알림 필드"의 페이로드: db는 직렬화만 담당하며 필드의 의미를 이해하지 못합니다.
//
// 취약점 필드가 중복 저장되고 렌더링 중에 다시 확인되지 않는 이유: 취약점의 이름이 변경되고 레벨이 지정되며 나중에 상태가 변경됩니다.
// 푸시 내용은 당시 사건의 결론을 반영해야 합니다. 검토 결과 "나중에 low로 변경되었습니다"라는 결과가 나올 것입니다.
// 위험할 정도로 오해의 소지가 있습니다. 또한 fan-out는 JOIN 및 findings/tasks/assets라는 세 개의 테이블로 렌더링됩니다.
type Snapshot struct {
	// 이벤트 유형: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// kind=finding_status_changed인 경우에만 비어 있지 않습니다.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item는 채널 렌더링을 위해 푸시되는 취약점입니다.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets는 확인된 자산 표시 이름(예: 도메인 이름/IP)입니다. server 레이어로 채워짐——
	// 이 패키지는 데이터베이스를 건드리지 않으며 이름을 가져올 수 없습니다.
	Assets []string
	// DetailURL는 취약점 세부정보로 돌아가는 링크입니다. 비어 있으면 public_base_url가 구성되지 않았으며 렌더링 시 생략되었음을 의미합니다.
	DetailURL string
	// 상태 변경 이벤트에 특별합니다. 두 항목 모두 null이 아닌 경우 "보류 중 → 수정됨"으로 렌더링됩니다.
	FromStatus string
	ToStatus   string
}

// IsStatusChange 이 항목이 상태 변경 이벤트인지 여부를 보고합니다.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title는 항목의 표시 제목을 반환합니다. 인위적으로 명명된 name, 대체 취약점 유형 vulnclass에 우선순위가 부여됩니다.
// 둘 다 비어 있으면 자리 표시자를 사용하십시오. 빈 제목은 출력되지 않습니다.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(이름없는 취약점)"
}

// Message는 하나의 채널에서 전송되는 완전한 콘텐츠입니다.
type Message struct {
	// 단일 푸시의 길이는 1입니다. 요약 푸시(digest)의 길이는 전체 배치입니다.
	// 빈 조각은 불법이며 호출자는 최소한 하나를 확보해야 합니다.
	Items []Item
	// Batch=true인 경우 요약 메시지가 렌더링됩니다(제목 변경, 시간 창 및 메시지 수 추가).
	Batch bool
	// WindowMinutes는 요약 기간(분)으로, Batch=true인 경우 "거의 N분" 카피라이팅에만 사용됩니다.
	// 렌더링할 때 구성을 계산하는 대신 의도적으로 명시적으로 구성을 전달했습니다. time.Since: 렌더링은 결정적으로 유지되므로 테스트하기 쉽습니다.
	WindowMinutes int
	// HomeURL는 플랫폼 패널 주소(글로벌 public_base_url)입니다. 비어 있으면 패널 항목이 없습니다.
	HomeURL string
}

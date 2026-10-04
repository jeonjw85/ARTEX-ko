package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**취약성 트래픽 증거(선택 사항)**: 취약성을 보고하기 위해 report_finding를 호출할 때 취약성 결론을 뒷받침하는 것으로 확인되고 확인된 HTTP 요청/응답이 있는 경우 traffic_refs를 사용하여 반복 순서대로 실제 ID를 바인딩할 수 있습니다. 도메인 이름과 시간은 후보자 심사에만 사용되며 어떠한 연관성도 추정되지 않습니다. TCP와 같은 HTTP가 아닌 취약점의 경우 수집되지 않거나 정확하게 일치하지 않는 경우 []를 생략하거나 전달하세요. evidence의 경우 명령 출력, 로그 및 기타 검증 가능한 증거를 보관하십시오. 바인딩 해제 이유를 설명하는 것이 좋습니다. ID를 추측하지 말고 패치 패킷에 대해서만 조사를 반복하지 마십시오."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }

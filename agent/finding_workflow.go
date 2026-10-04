package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**취약점 번호 규칙**: finding_id는 독립적인 취약성 레코드 ID입니다. finding_node_id는 탐사 노드 ID입니다. list_findings / list_task_findings / node_detail / get_task_node_detail의 id는 탐색 노드 ID로 예약되어 있으며, 독립된 번호는 반환된 동일한 finding_id에서 읽어야 합니다. get_finding_traffic / bind_finding_traffic는 독립적인 finding_id를 사용합니다. 이전 update_finding_report의 finding_id 매개변수는 여전히 finding_node_id로 전달됩니다. 증거 도구로 report_finding의 첫 번째 줄에 있는 숫자를 사용하지 말고, 숫자 오류가 발생한 후 다른 숫자를 추측하지 마십시오."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\n는 기본적으로 Agent에 의해 보고됩니다. 보고서를 작성하기 전에 트래픽을 확인하고 바인딩하십시오. 보고자는 확인 명령, 주요 출력, 기존 실제 트래픽 ID 및 Agent 보고 목적을 evidence에 유지하여 실행 기록을 확인합니다. 바인딩에는 추가 패킷 검사가 필요하지 않습니다. 명시적 인스턴트 바인딩과 호환 가능: traffic_refs 또는 evidence_hint_id는 검증된 참조를 제출할 수 있으며 후자는 이 작업에 지정된 hint의 구조화된 참조를 읽습니다. 유효하지 않은 것이 있으면 이 보고서는 실패합니다. TCP/ 패키지에는 이러한 선택적 매개변수가 필요하지 않습니다. Return finding_id 및 finding_node_id는 각각 독립적인 기록 및 탐색 노드를 나타냅니다."
		case "add_hint", "add_task_hint":
			note = "\n가 확인된 취약점을 전송할 때 해당 프롬프트 traffic_refs에 확인된 트래픽의 ID, 목적, 설명 및 순서를 유지합니다(단일 항목은 최상위 수준에 배치되고 해당 hints 요소는 일괄 배치됩니다). 그리고 이것이 증명하는 구체적인 취약점은 text에 설명되어 있습니다. 발신자는 텍스트를 넘겨주고 기존 트래픽 참조를 삭제할 수 없습니다. 검증되지 않은 후보자는 증거로 통과될 수 없습니다."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n** 트래픽 증거 핸드오버(선택 사항)**: 취약점이 데이터베이스에 입력된 후 보고서를 작성하기 전에 기본적으로 보고서 Agent에 의해 자동 바인딩이 완료됩니다. 보고자는 확인 명령, 주요 출력, 기존 실제 트래픽 ID 및 그 목적을 evidence에 유지해야 하며 보고서 Agent의 추적성을 용이하게 하기 위해 작업에 intent_id를 포함해야 합니다. 바인딩을 위해 추가 패킷을 확인할 필요가 없습니다. Auto / Planner 다른 사람을 대신하여 보고할 때 실행자의 기존 참조를 폐기하지 마십시오. add_hint / add_task_hint는 traffic_refs에 넘겨질 수 있습니다. 명시적 인스턴트 바인딩은 report_finding의 traffic_refs / evidence_hint_id와 계속 호환됩니다. TCP 또는 패킷이 없을 때 일반 등록. ID는 추측할 수 없으며, 보충 패킷에 대해서만 반복적으로 감지할 수 없습니다."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\n 플랫폼 대화 상자에 작업 컨텍스트가 없으면 report_finding가 직접 호출되지 않습니다. add_task_hint는 기존 해당 태스크로 핸드오버하는데 사용되며, 태스크 Agent가 등록되고, 그 결과는 list_task_findings로 확인된다."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\n가 목표가 완료되었다고 판단하기 전에 먼저 기존 증거의 보고/인계가 완료되어야 합니다. 단지 텍스트 취약점이 등록되었다는 이유로 증거 인도가 완료되기 전에 작업을 종료하거나 Worker를 취소하지 마십시오. 패키지가 없으면 기다려달라고 요청하거나 패키지를 강제로 캡처하지 마세요."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**보고 전 트래픽 자동 연결(활성화)**: 보고서를 작성하기 전에 트리거된 취약점에 대한 트래픽을 확인하고 바인딩할 책임은 귀하에게 있습니다. 먼저 report_finding에서 JSON 또는 get_task_node_detail / list_task_findings를 반환하여 명확한 finding_id 및 finding_node_id를 얻습니다. 취약점 내역, 해당 의도의 실행 기록, 기존 증거 목록을 읽어보고, 신고자가 건네준 실제 ID를 우선적으로 처리합니다. 이 검증이 HTTP이고 트래픽 도구를 사용할 수 있는 경우 traffic_search를 사용하여 후보를 선별한 다음 traffic_get를 사용하여 요청/응답이 실제로 취약점을 하나씩 지원하는지 확인합니다. 도메인 이름과 시간은 심사에만 사용되며 귀속을 입증하지 않습니다. 확인된 증거를 bind_finding_traffic(finding_id, traffic_refs)와 재발순으로 연관시키고, baseline / proof / verification / supporting를 선택하고 그 목적을 설명하시오. 이 취약점을 조작하는 것만 가능하며, 취약점을 다시 생성하거나 대상을 다시 탐지하지 않습니다. 바인딩이 성공한 후 get_finding_traffic를 다시 호출하여 최신 version를 얻고 필요한 텍스트를 읽은 다음 실제로 읽은 version를 evidence_version로 update_finding_report에 전달합니다(해당 finding_id 매개 변수는 여전히 finding_node_id를 사용합니다). 이미 존재하는 경우 추가 바인딩을 추가할 필요가 없습니다. TCP, 수집이 없거나 도구를 사용할 수 없거나 정확한 일치가 없는 경우 자동 바인딩을 건너뛰고 텍스트/명령 증거를 기반으로 정상적으로 보고서를 작성하고 이유를 설명하며 추측을 하지 않고 트래픽을 수집합니다. 바인딩이 실패하면 성공이 선언되지 않습니다. 기존 증거는 유지되며 구속력이 없는 이유는 보고서에 설명됩니다."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "선택 사항: 확인되었으며 이 팁의 특정 취약점에 해당하는 트래픽 참조는 순서대로 유지됩니다. 핸드오버 후 report_finding는 evidence_hint_id로 전달되어 이러한 참조를 전달할 수 있습니다.", "items": obj(map[string]any{"traffic_id": str("실제 교통 ID"), "role": str("baseline / proof / verification / supporting"), "note": str("이 흐름은 어떤 결론을 뒷받침합니까?")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d는 이 작업의 프롬프트 노드여야 합니다(상속된 프롬프트는 바인딩에 직접 사용할 수 없습니다).", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}

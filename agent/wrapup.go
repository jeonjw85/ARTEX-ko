package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// 종료 프롬프트 단어(wrap-up / settlement prompt): [걸음 수 소진(MaxTurns)]으로 인해 agent일 때 또는
// [Timeout (run_seconds/MaxDuration)]이 종료되면 이 프롬프트가 SDK의 settlement 단계에 주입되며,
// agent가 먼저 식별되었지만 다시 기록되지 않은 콘텐츠를 라이브러리에 넣은 다음, 완료되지 않은 작업을 피하기 위해 요약을 출력하도록 합니다.
//
// 각 agent 닫는 프롬프트 단어는 요청 시 백그라운드에서 덮어쓸 수 있습니다.(살다 agents.wrapup_prompt),비워두면 여기를 사용하세요.
// 내장된 기본값. [프롬프트 단어 텍스트]만 편집할 수 있습니다. 비활성화할 도구와 종료할 예산 라운드 수는 코드 고정 전략입니다.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// 내장된 기본 닫기 프롬프트 단어는 agent key로 색인화됩니다. worker는 역사적으로 하드 코딩된 settleWrapUpPrompt를 재사용합니다.
// (worker.go에 정의됨), planner/mainagent에는 각각 자체 버전이 있습니다. 놓친 것(맞춤형 agent)은 보편적인 것입니다.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: 각 agent 최종 단계 [자체]의 라운드 예산은 기본적으로 내장되어 있습니다(배경 >0으로 재정의될 수 있음).
// 각 게임에는 최종 단계에서 충분한 움직임이 있도록 10라운드가 제공됩니다. genericWrapupTurns를 놓치고 걸었습니다.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "현재 계획 단계의 단계 수가 곧 소진됩니다. 이 라운드가 막 끝났다는 점에 유의하세요. 상황이 바뀌면 시스템이 다시 깨어나서 계속 계획을 세울 것입니다. 이는 작업이 종료되었음을 의미하지 않습니다. 여기에서 전체 계획을 종료할 필요는 없습니다. 이번 라운드에서 명확하게 생각한 결론을 구현하고 이번 라운드를 헛되이 보내지 마십시오. 단 [종료를 위해 의도를 구성하지 마십시오](이 라운드의 의도는 여전히 완전히 정상적인 결과입니다): (1) 탐색 방향을 판단했다면 [지금 배포해야 함], add_intent를 사용하여 일괄 제출하십시오(생각을 주저하지 마십시오). (2) 사실로 발견/증명된 목표에 대해서는 prove_goal 마크 met를 조정합니다(놓치지 마세요). (3) 단계별로 나누어야 할 직렬 활용 체인을 식별한 경우 다음에 깨워서 보낼 수 있도록 TodoWrite로 적어 둡니다. 완료 후 바로 이 라운드를 종료합니다. 요약 텍스트를 출력할 필요가 없습니다."

const mainAgentWrapUpDefault = "귀하의 단계 수가 곧 소진되어 이 상호작용이 곧 종료됩니다. 새로운 탐사/작전을 시작하지 마세요. 현재 진행 상황, 주요 결론, 사용자에게 권장되는 다음 단계를 요약하려면 **일반 텍스트로 된 단일 문장**을 사용하세요."

const genericWrapUpDefault = "예산 부족으로 인해 곧 종료될 예정입니다. 먼저 완료되었지만 라이브러리에 포함되지 않은 결과를 다시 작성한 다음 일반 텍스트의 단일 문장을 사용하여 수행한 작업과 얻은 주요 결론을 요약하십시오(이 문장은 이 실행의 결과로 표시됩니다)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- 작업 수준 제한 시간 종료 단어(docs/ 작업 수준 제한 시간 및 종료 디자인 참조.md) ----------
//
// per-run의 끝 단어는 [두 세트]입니다. per-run는 "이번에 run에 대한 예산이 소진되었습니다"입니다. 작업 시간 초과는 다음과 같습니다.
// "모든 임무가 끝나가고 있습니다." 의미론은 종종 반대입니다(특히 planner: per-run는 "계획을 멈추지 마세요"라고 말합니다.
// 작업 시간 초과는 "계획을 중지하고 해당 지점에 도달하면 최종 판단을 내리십시오"를 의미합니다. worker/planner만 구성하세요.

// WrapupTaskTimeoutOverride / …TurnsOverride: DB 작업 시간 초과 종료 단어 및 라운드 숫자 적용 범위
// （wire 도착하다 agents.task_timeout_wrapup_prompt / _max_turns，오직 worker/planner）。
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**전체 임무가 제한 시간에 도달하여 곧 종료됩니다**(이번에는 run 예산이 아니라 전체 탐사가 종료됩니다). 이것이 마지막 기회입니다: (1) 식별했지만 아직 다시 작성하지 않은 [모든] 콘텐츠를 삭제합니다. - 새로운 자산 insert_assets, 탐색 결론/사실 record_fact, 확인된 취약점 report_finding; (2) 새로운 명령/탐지를 시작하지 마십시오. (3) **마지막으로 일반 텍스트의 한 문장**을 사용하여 이 의도에 대한 주요 결론을 요약합니다."

const plannerTaskTimeoutDefault = "**전체 작업이 시간 초과 제한에 도달하여 곧 종료됩니다**(이번 라운드는 아니지만 전체 작업이 종료됩니다). 현재의 [모든] 사실과 조사 결과를 바탕으로 최종 목표 판단을 내리십시오. 증거에 의해 달성된 것으로 입증된 목표에 맞게 prove_goal 마크 met를 조정하십시오(판단을 놓치지 마십시오). **새로운 인텐션을 생성하지 마세요**(디스패치된 인텐션은 현재 실행되지 않습니다). 판정이 완료되면 종료되며, 요약문을 출력할 필요가 없습니다."

// TaskTimeoutWrapupDefault는 특정 agent 작업 시간 초과(백그라운드 자리 표시자/복원 기본값의 경우)의 내장된 기본 종료 단어를 반환합니다.
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // 구성되지 않음(mainagent/chat)은 빈 문자열을 반환합니다.
}

// resolveTaskTimeoutWrapup: DB 재정의(비어 있지 않음) > 내장 기본값. 빈 문자열은 agent에 작업 시간 초과 단어가 없음을 나타냅니다.
// (worker/planner 아님) 호출자는 이때 per-run 워드를 롤백해야 합니다.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // 기본적으로 per-run 라운드 번호가 사용됩니다.
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true → 이번에는 run가 작업 deadline에 의해 강제됩니다. Timeout가 종료되기 때문에 = 작업이 지점에 도달 → 작업 시간 초과 단어;
//     MaxTurns가 종료되기 때문에 = 핀치 창의 단계 수가 먼저 소진되고 작업에 몇 분이 남았으므로 → per-run 단어로 돌아갑니다.
//   - clamped=false → 작업이 아직 초기 단계입니다. 두 reason 모두 per-run라는 단어를 사용합니다(즉, wrapupSettlement로 변질됨).
//
// harness에게 넘겨준 PromptByReason는 마지막에 [Actual] reason를 누르면 그 자리에서 선택됩니다. build가 없으면 일치하지 않습니다.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // 결론(또한 clamped가 아닐 때 reason의 두 값)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // 임무 완수
				harness.ReasonMaxTurns: perRun, // 먼저 단계 수가 소진되고 작업에 아직 시간이 남아 있습니다.
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}

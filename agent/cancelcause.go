package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "사용자가 일시 중지한 작업",
		"사용자가 작업 제어 인터페이스(POST /api/tasks/{id}/control, action=pause)를 통해 작업을 일시 중지했습니다. 이번 Planner/Worker 실행은 자동으로 취소되었습니다. 실행 의도는 frontier(open)로 반환되고 작업이 재개된 후 작업이 다시 획득되어 처음부터 실행됩니다.")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "오케스트레이션 Agent가 작업을 일시 중지했습니다.",
		"배열 Agent는 pause_task 도구를 호출하여 이 작업을 일시 중지합니다. 이 Planner/Worker 실행이 적극적으로 취소되었습니다. 실행 의도는 frontier(open)로 반환되고 복구 후 다시 실행됩니다.")
	AbortTaskDeleted = cause("task_deleted", "작업이 삭제되었습니다.",
		"작업이 삭제되고(DELETE /api/tasks/{id}) 삭제 장벽이 작업의 실행 중인 Planner, Worker 및 기본 Agent를 취소했습니다. 이 작업의 결과는 다시 사용되지 않습니다.")
	AbortPausedOnReload = cause("paused_on_reload", "백엔드는 작업의 일시 중지된 상태를 재개합니다.",
		"백엔드가 시작되면 데이터베이스에 유지된 상태에 따라 작업 일시 중단이 재개됩니다. 이 실행이 취소되었습니다. 정상적인 상황에서는 복구 단계 동안 Agent가 실행되지 않습니다.")
	AbortGoalMet = cause("goal_met", "기획자는 임무 목표가 달성되었다고 판단합니다.",
		"플래너는 작업 목표가 달성되었다고 판단하고 작업을 done로 설정한 다음 아직 실행 중인 Worker를 취소합니다. 이러한 의도는 실패가 아닌 stopped로 표시됩니다.")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "작업 시간 초과 완료를 위한 대기 시간이 소진되었습니다.",
		"작업이 timeout에 도달한 후 실행 중인 Worker가 정상적으로 종료될 때까지 기다리지만 90초 drain 유예가 여전히 부족하여 하드 취소가 수행됩니다. 의도는 exhausted로 표시되며 마감 단계에서 작성된 사실과 자산은 유지됩니다.")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "기획자는 이 의도를 종료했습니다.",
		"플래너는 kill_work를 호출하여 이 의도를 적극적으로 종료합니다. 이는 일반적으로 방향이 벗어났거나 더 이상 가치가 없음을 의미합니다. 의도는 stopped로 표시되며 자동으로 회수되지 않습니다.")
	AbortWorkPausedByUser = cause("work_paused_by_user", "사용자가 이 Worker 의도를 일시중지했습니다.",
		"사용자가 실행 중인 Worker를 일시 중지했습니다. 이 호출은 취소되고 의도는 paused로 변경됩니다. 등록된 모든 의도, 사실, 취약점 및 활동 기록은 유지되며 복구 후 처음부터 다시 실행할 수 있습니다.")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "사용자가 이 Worker 인텐트를 삭제했습니다.",
		"사용자가 실행 중인 Worker를 삭제했습니다. 이 통화는 취소되었습니다. Worker가 쓰기 영역을 종료한 후 서버는 사용자가 선택한 삭제 모드에 따라 의도를 처리합니다. 거짓 삭제는 삭제된 것으로만 표시되고 모든 출력을 유지하며, 실제 삭제는 의도와 그에 의해서만 지원되는 다운스트림 노드를 계단식으로 제거합니다.")
	AbortWorkFinished = cause("work_finished", "Worker가 정상적으로 종료되어 context가 출시되었습니다.",
		"Worker가 정상적으로 종료되었으며 엔진이 detachWork의 context 리소스를 해제했습니다. 이는 런타임 인터럽트가 아닙니다. 인터럽트 메시지에 나타나면 취소 이벤트와 종료 이벤트 사이에 경쟁 조건이 있음을 의미합니다.")
	AbortPausedRaceGuard = cause("paused_race_guard", "작업이 일시 중지된 동안 새 실행 시작을 거부합니다.",
		"작업이 일시 중지된 상태에 있으면 엔진은 새로운 실행 context 실행을 거부합니다. 이는 claim와 일시 중지된 상태 사이의 경쟁 조건으로 인해 Worker가 계속 시작되는 것을 방지하는 데 사용됩니다. 수신된 의도는 frontier로 반환됩니다.")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "사용자가 이 대화를 중단했습니다.",
		"사용자가 중지를 클릭하고 기본 Agent 또는 세션 Agent의 현재 실행을 적극적으로 종료했습니다. 생성된 활동 기록은 그대로 유지되며 계속해서 다음 메시지를 보낼 수 있습니다.")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "작업이 일시 중지되고 기본 Agent 대화가 중단되었습니다.",
		"사용자가 작업을 일시 중지하면 실행 중인 기본 Agent 대화도 동시에 취소됩니다. 생성된 활동 기록은 유지됩니다. 작업이 재개된 후에는 현재 메시지 라운드가 자동으로 재생되지 않습니다.")
	AbortChatTurnFinished = cause("chat_turn_finished", "이번 라운드는 정상적으로 종료되었으며 context가 출시되었습니다.",
		"이번 라운드의 대화는 정상적으로 종료되었으며, 서버는 이번 라운드의 context 리소스를 공개하고 있습니다. 이는 런타임 인터럽트가 아닙니다. 인터럽트 메시지에 나타나면 취소 이벤트와 종료 이벤트 사이에 경쟁 조건이 있음을 의미합니다.")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "백엔드 프로세스가 종료 중입니다.",
		"백엔드 프로세스가 SIGINT 또는 SIGTERM를 수신했으며 다시 시작, 업데이트 또는 종료 중입니다. 실행 중인 모든 Agent가 취소됩니다. 다시 시작한 후 나머지 running 의도는 open로 재설정되고 다시 실행됩니다.")
	AbortRunHardTimeout = cause("run_hard_timeout", "단일 실행 하드 시간 초과가 트리거되었습니다.",
		"단일 실행이 소프트 벽시계 예산 및 추가 유예를 초과합니다. 이는 모델 요청 또는 도구가 오랫동안 반환되지 않았음을 나타내며, 결과적으로 정상적인 라운드 경계 닫기가 실행될 수 없음을 나타냅니다. 중단 이전에 반환되지 않은 마지막 도구 호출을 집중적으로 확인하시기 바랍니다.")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "업스트림 context가 deadline에 도착합니다.",
			"업스트림 context는 deadline에 도달하지만 setter는 명명된 이유를 첨부하기 위해 WithTimeoutCause를 전달하지 않습니다. " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "취소 당사자가 명시적인 사유를 첨부하지 않았습니다.",
			"업스트림 context가 취소되었지만 취소 당사자가 context.WithCancelCause를 통해 명명된 이유를 첨부하지 않았습니다. agent/cancelcause.go에 사유를 등록하시고 취소포인트에 접속해주세요", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}

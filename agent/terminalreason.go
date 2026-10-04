package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/norma/harness"
)

// runTrace retains the latest tool call so an interrupted run can identify the
// operation that was still in flight.
type runTrace struct {
	startedAt time.Time
	id        string
	name      string
	input     string
	at        time.Time
	pending   bool
}

func (t *runTrace) start(id, name, input string) {
	t.id, t.name, t.input, t.at, t.pending = id, name, input, time.Now(), true
}

func (t *runTrace) done(id string) {
	if id == t.id {
		t.pending = false
	}
}

var reasonHint = map[harness.TerminalReason]string{
	harness.ReasonCompleted:         "모델은 라운드를 정상적으로 종료했지만 텍스트 요약을 남기지 않았습니다. 사실과 자산은 이번 라운드의 도구 호출 기록에 따릅니다.",
	harness.ReasonMaxTurns:          "단계 상한에 도달했습니다(MaxTurns): SDK가 마감을 실행하고 사실과 자산을 다시 작성했습니다. 기획자가 실패로 처리하지 않고 방향을 바꾸고 계속하도록 의도는 exhausted로 표시됩니다.",
	harness.ReasonTimeout:           "단일 실행의 벽시계 예산에 도달했습니다(MaxDuration). 해당 지점에 도달하면 실행 도구가 중단되고 제자리에서 완료되며 식별된 사실과 자산이 다시 기록됩니다. 의도는 exhausted로 표시됩니다.",
	harness.ReasonModelError:        "모델 또는 API 호출이 실패하고(네트워크, 인증, 전류 제한, 공급자 5xx 등) 재시도가 끝난 후 의도가 blocked로 표시됩니다. 전송 계층 실패로 인해 이 의도가 기본적으로 감지되지 않습니다. 메소드를 재할당하거나 변경하기로 결정하기 전에 실행 프로세스(get_worker_trace)를 확인하십시오.",
	harness.ReasonBlockingLimit:     "컨텍스트 길이가 하드 제한에 도달하고 요청이 전송되기 전에 차단됩니다. 의도 세분성을 좁히거나 압축 도구가 반환되어야 합니다.",
	harness.ReasonPromptTooLong:     "프롬프트 단어가 너무 길고 컨텍스트 압축 재시도가 소진되어 실행을 계속할 수 없습니다.",
	harness.ReasonImageError:        "현재 모델은 이번 라운드에 다중 모드 콘텐츠를 지원하지 않습니다. 비전을 지원하는 모델로 전환하거나 도구가 이미지를 반환하지 않도록 하세요.",
	harness.ReasonStopHookPrevented: "Stop 후크는 이 라운드의 종료를 방지하고 이후 계속되지 않습니다. 작업 Guard의 규칙이 너무 엄격한지 확인하세요.",
	harness.ReasonHookStopped:       "도구 또는 후크는 범위를 벗어난 대상 또는 비활성화된 명령과 같은 실행을 적극적으로 중지합니다. tool_result의 마지막 차단 설명을 확인하세요.",
	harness.ReasonAbortedStreaming:  "모델 출력 스트리밍 생성 단계 중에 실행이 취소되었습니다.",
	harness.ReasonAbortedTools:      "도구 실행 단계 중에 실행이 취소되었습니다.",
}

// terminalText renders a terminal event with no final text into a compact summary
// and a Markdown detail block.
func terminalText(ctx context.Context, term *harness.Terminal, tr *runTrace) (string, string) {
	reason := term.Reason
	aborted := reason == harness.ReasonAbortedStreaming || reason == harness.ReasonAbortedTools
	// Prompt may return ctx.Err directly without a terminal event. Preserve the
	// cancellation cause instead of falling back to an empty/unknown terminal reason.
	if reason == "" && ctx.Err() != nil {
		aborted = true
	}

	var sum string
	if aborted {
		_, short, _, ok := AbortReason(ctx)
		if !ok {
			short = "취소 사유를 알 수 없습니다"
		}
		stage := "실행 중"
		switch reason {
		case harness.ReasonAbortedStreaming:
			stage = "모델 출력 단계"
		case harness.ReasonAbortedTools:
			stage = "도구 실행 단계"
		}
		sum = "(실행 중단: " + short + "; 중단 단계: " + stage + progressSuffix(term, tr) + ", 미완료)"
	} else if reason == harness.ReasonMaxTurns || reason == harness.ReasonTimeout {
		sum = "(실행 예산 한도 도달(" + string(reason) + "), 확인된 사실 저장됨" + progressSuffix(term, tr) + "; 텍스트 요약 없음)"
	} else {
		hint := terminalReasonHint(reason)
		sum = "(텍스트 요약 없음, 최종 상태 " + terminalReasonLabel(reason) + "：" + firstLine(hint, 80) + "）"
	}

	var b strings.Builder
	b.WriteString(sum)
	b.WriteString("\n\n")
	displayReason := terminalReasonLabel(reason)
	fmt.Fprintf(&b, "- **최종 상태**: `%s` - %s\n", displayReason, terminalReasonHint(reason))
	if aborted {
		code, _, why, ok := AbortReason(ctx)
		if ok {
			fmt.Fprintf(&b, "- **중단 이유**(`%s`): %s\n", code, why)
		} else {
			b.WriteString("- **중단 이유**: 확인할 수 없습니다. context.WithCancelCause로 취소 사유가 전달되지 않았을 수 있습니다.\n")
		}
	}
	if term.Err != nil {
		fmt.Fprintf(&b, "- **기본 오류**: `%v`\n", term.Err)
	}
	if aborted && strings.TrimSpace(term.Text) != "" {
		b.WriteString("- **중단 전 생성된 일부 출력**:\n\n")
		b.WriteString(term.Text)
		b.WriteString("\n\n")
	}
	if term.Turns > 0 {
		fmt.Fprintf(&b, "- **실행 횟수**: 모델 %d회 호출\n", term.Turns)
	}
	if !tr.startedAt.IsZero() {
		fmt.Fprintf(&b, "- **이 작업에 소요된 시간**: %s\n", roundDur(time.Since(tr.startedAt)))
	}
	if u := term.Usage; u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, "- **누적 token**: 입력 %d / 출력 %d / 캐시 읽기 %d / 캐시 쓰기 %d\n",
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
	if tr.name == "" {
		b.WriteString("- **도구 호출**: 도구를 실행하기 전에 종료되었습니다.\n")
	} else if tr.pending {
		fmt.Fprintf(&b, "- **중단 시 실행 중인 도구**: `%s`(%s 경과, **결과가 반환되지 않음**)\n\n```json\n%s\n```\n",
			tr.name, roundDur(time.Since(tr.at)), firstLine(tr.input, 300))
	} else {
		fmt.Fprintf(&b, "- **중단 전 마지막 도구**: `%s`(정상적으로 반환됨)\n", tr.name)
	}
	return sum, b.String()
}

func terminalReasonLabel(reason harness.TerminalReason) string {
	if reason == "" {
		return "context_canceled"
	}
	return string(reason)
}

func terminalReasonHint(reason harness.TerminalReason) string {
	if hint := reasonHint[reason]; hint != "" {
		return hint
	}
	if reason == "" {
		return "실행 중인 context가 취소되었지만 기본 Terminal 이벤트가 생성되지 않았습니다."
	}
	return "최종 상태를 알 수 없습니다. harness에 TerminalReason가 추가되었을 수 있습니다. reasonHint를 추가해 주세요."
}

func progressSuffix(term *harness.Terminal, tr *runTrace) string {
	var parts []string
	if term.Turns > 0 {
		parts = append(parts, fmt.Sprintf("%d회", term.Turns))
	}
	if !tr.startedAt.IsZero() {
		parts = append(parts, roundDur(time.Since(tr.startedAt)))
	}
	if len(parts) == 0 {
		return ""
	}
	return ", 실행량 " + strings.Join(parts, " / ")
}

func roundDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	case d < time.Hour:
		return d.Round(time.Second).String()
	default:
		return d.Round(time.Minute).String()
	}
}

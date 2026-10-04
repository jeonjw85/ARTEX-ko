package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa는 norma v0.4.0에서 도입된 "모델 기반 컨텍스트 압축" 메커니즘입니다. 플랫폼 실험 기능으로 사용자는 다음을 수행할 수 있습니다.
// 시스템 설정을 전환하세요. 내장된 compaction와 상호 배타적입니다. noaadapter.Enable는 유일한 항목이며 한 번 장착할 수 있습니다.
// Enable가 호출되지 않으면 컨텍스트 인계(Compactor), Compress 도구 및 상주 프롬프트 단어 3개가 닫힙니다.
// (내장 compaction는 평소와 같이 작동합니다). 스위치는 agent마다 삽입된 noaEnabledFn에 의해 구문 분석되고 run에 의해 읽혀집니다.
// 일단 스위치는 나중에 시작된 run에만 영향을 미치며 agent를 다시 빌드할 필요가 없습니다.

// enableNoa 파서 보고가 활성화되면 noa를 opts에 연결합니다. archiveRoot는 압축된 원본 텍스트의 영구 기본 디렉터리입니다.
// (전역 workDir를 사용하면 각 agent는 <workDir>/noa 아래에 균일하게 속하며 작업/의도 디렉터리와 함께 분산되지 않습니다.), sessionID
// 그 아래 아카이브 하위 디렉터리의 이름을 지정합니다(전역적으로 고유하므로 동일한 기본 디렉터리 내에서 충돌이 발생하지 않음).
//
// noa는 실험적인 기능입니다. 액세스 실패가 실제 작업을 중단해서는 안 됩니다. 오류가 발생하면 onWarn를 통해 보고되고 내장된 압축이 롤백됩니다.
// "두 개의 컨텍스트 관리자가 동시에 설정됨"으로 인한 agentcore 경보를 방지하려면 성공적으로 활성화되면 opts.Compaction를 지우십시오.
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("noa 압축을 활성화하지 못했습니다. 내장 압축으로 대체:" + err.Error())
		}
		return
	}
	// Compactor는 Compaction를 포함하지만 둘 다 공존하는 경우 agentcore는 매번 경보를 울립니다. 명확하게 정리하세요.
	opts.Compaction = nil
}

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
// 시스템 설정을 전환하세요. 내장 압축과 상호 배타적입니다. noaadapter.Enable은 일단 마운트되면 유일한 항목입니다.
// Enable이 호출되지 않으면 컨텍스트 인계(Compactor), 압축 도구 및 상주 프롬프트 3개가 닫힙니다.
// (내장 압축은 평소와 같이 작동합니다). 스위치는 각 에이전트에 의해 주입된 noaEnabledFn에 의해 구문 분석되며, 실행될 때마다 읽습니다.
//일단 스위치는 후속 실행에만 영향을 미치며 에이전트를 다시 빌드할 필요가 없습니다.

// 활성화Noa 파서 보고가 활성화되면 선택하도록 noa를 연결합니다. archiveRoot는 원본 텍스트를 압축하기 위한 영구 기본 디렉터리입니다.
// (전역 workDir을 가져오면 모든 에이전트는 <workDir>/noa에 속하며 작업/의도 디렉터리와 함께 분산되지 않습니다.), sessionID
// 그 아래 아카이브 하위 디렉터리의 이름을 지정합니다(전역적으로 고유하므로 동일한 기본 디렉터리 내에서 충돌이 없음).
//
// noa는 실험적인 기능입니다. 액세스 오류가 실제 작업을 방해해서는 안 됩니다. 오류가 발생하면 onWarn을 통해 보고되고 내장된 압축이 롤백됩니다.
// "두 개의 컨텍스트 관리자가 동시에 설정됨"으로 인해 에이전트 코어 경보가 발생하지 않도록 성공적으로 활성화되면 opts.Compaction을 지웁니다.
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
	// Compactor는 Compaction을 재정의하지만 두 가지가 공존하는 경우 에이전트 코어는 매번 경고합니다. 명시적으로 지웁니다.
	opts.Compaction = nil
}

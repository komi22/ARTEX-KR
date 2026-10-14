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
const findingTrafficGuidance = "\n\n**취약점 트래픽 증거(선택 사항)**: report_finding으로 취약점을 등록할 때 결론을 뒷받침하는 실제 HTTP 요청과 응답을 확인했다면 traffic_refs에 트래픽 ID를 순서대로 넣을 수 있습니다. 도메인과 시간은 후보를 찾는 데만 사용하고, 그것만으로 연관성을 추정하지 마세요. TCP처럼 HTTP가 아닌 취약점이거나 정확한 트래픽 기록이 없다면 traffic_refs를 생략하거나 빈 배열로 전달하세요. 검증 명령, 출력, 로그 등 다른 증거는 evidence에 남기고 트래픽을 연결하지 않은 이유를 설명하는 것이 좋습니다. ID를 추측하거나 연결할 패킷을 만들기 위해 불필요하게 다시 탐색하지 마세요."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }

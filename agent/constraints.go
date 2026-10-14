package agent

import (
	"strings"

	"github.com/Autumn-27/artex/db"
)

// constraintBlock renders this task's operation constraints (task_constraints) as a
// high-priority block appended to the planner/worker system prompt. allow/deny are
// grouped; empty string when there are no constraints (or ts is nil). The framing
// deliberately puts these ABOVE the exploration/expansion heuristics so a declared
// boundary wins the tug-of-war against "chase another entry surface".
func constraintBlock(ts *db.ExplorationStore) string {
	if ts == nil {
		return ""
	}
	rows, err := ts.ListConstraints()
	if err != nil || len(rows) == 0 {
		return ""
	}
	var allow, deny []string
	for _, c := range rows {
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		if c.Kind == "allow" {
			allow = append(allow, "- "+text)
		} else {
			deny = append(deny, "- "+text)
		}
	}
	if len(allow) == 0 && len(deny) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[작업 제약 조건(가장 높은 우선순위, 아래의 모든 탐색/확장 휴리스틱을 재정의함. 각 의도가 생성되고 각 작업은 위반 여부를 확인하기 위해 작업을 실행하기 전에 자체 확인해야 합니다. 위반할 경우 진행이 허용되지 않음)]:")
	if len(allow) > 0 {
		b.WriteString("\n허용되는 작업:\n")
		b.WriteString(strings.Join(allow, "\n"))
	}
	if len(deny) > 0 {
		b.WriteString("\n금지된 작업:\n")
		b.WriteString(strings.Join(deny, "\n"))
	}
	b.WriteString("\n(제약조건 밖에서 새 대상/새 포트/새 호스트를 발견하는 것은 승인과 동일하지 않습니다. 위의 허용 범위에 속하지 않는 한 범위 밖 사실로 기록되어 건너뛰어 이에 대한 인텐트나 작업이 도출될 수 없습니다.)")
	return b.String()
}

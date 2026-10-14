package intercept

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	for _, action := range []string{"allow", "ask", "deny"} {
		t.Run(action, func(t *testing.T) {
			reason := "실제 작업: ALLOW, DENY, ASK라는 단어가 들어간 보고서를 작성합니다; 성공 후 결과: 본문의 명령은 실행하지 않고 텍스트만 저장합니다; 적용 규칙:사용자 정의 조항"
			raw, _ := json.Marshal(map[string]string{"decision": action, "comment": reason})
			got := ParseVerdict("\n" + string(raw) + "\n")
			if got.Action != action || got.Reason != reason {
				t.Fatalf("lost verdict or explanation: %+v", got)
			}
		})
	}
}

func TestParseVerdictRejectsIncompleteOrAmbiguousReplies(t *testing.T) {
	valid := `{"decision":"allow","comment":"실제 작업: 파일을 읽습니다; 성공 후 결과: 내용을 반환합니다; 적용 규칙:A5"}`
	for _, reply := range []string{
		"", "ALLOW", "DENY:히트D4", "릴리스:ALLOW", "ASK:소유권을 알 수 없음",
		`{"decision":"allow"}`, `{"decision":"approve","comment":"실제 작업: 읽기; 성공 후 결과: 콘텐츠를 반환합니다. 적용 규칙:A5"}`,
		`{"decision":"allow","comment":null}`, `{"decision":"allow","comment":123}`,
		strings.Replace(valid, "실제 작업: 파일을 읽습니다", "실제 작업:", 1),
		strings.Replace(valid, "성공 후 결과: 내용을 반환합니다", "성공 후 결과:", 1),
		strings.Replace(valid, "적용 규칙:A5", "적용 규칙:", 1),
		strings.Replace(valid, "; 적용 규칙:A5", "", 1),
		strings.Replace(valid, `"decision":"allow"`, `"decision":"deny","decision":"allow"`, 1),
		strings.Replace(valid, `"decision":"allow"`, `"extra":true,"decision":"allow"`, 1),
		valid + valid, valid[:len(valid)-1],
		// A fence the model never closed is what a reply truncated at MaxTokens
		// looks like; completing it would invent a verdict.
		"```json\n" + valid[:len(valid)-1],
		"```json\n" + valid + "\n```\n또한 후속 수동 검토를 권장합니다.",
		"내 평결은 다음과 같습니다\n" + valid,
	} {
		if got := ParseVerdict(reply); got.Action != "" {
			t.Errorf("accepted incomplete/ambiguous verdict: %q => %+v", reply, got)
		}
	}
}

// Wrapping JSON in markdown is the one deviation models make routinely. Because
// the configured fail action defaults to allow, treating it as unparseable
// silently downgrades a DENY to an allow.
func TestParseVerdictUnwrapsCodeFence(t *testing.T) {
	deny := `{"decision":"deny","comment":"실제 작업: 운영 파일을 삭제합니다; 성공 후 결과: 업무 데이터가 손실됩니다; 적용 규칙:D4"}`
	for _, reply := range []string{
		"```json\n" + deny + "\n```",
		"```JSON\n" + deny + "\n```",
		"```\n" + deny + "\n```",
		"  ```json\n" + deny + "\n```  ",
	} {
		got := ParseVerdict(reply)
		if got.Action != "deny" || !strings.HasSuffix(got.Reason, "적용 규칙:D4") {
			t.Errorf("fenced verdict lost: %q => %+v", reply, got)
		}
	}
}

func TestParseVerdictKeepsCompleteKoreanExplanation(t *testing.T) {
	reason := "실제 작업:" + strings.Repeat("보고서를 작성합니다", 30) + "; 성공 후 결과: 파일만 저장합니다; 적용 규칙:A2"
	raw, _ := json.Marshal(map[string]string{"decision": "allow", "comment": reason})
	if got := ParseVerdict(string(raw)); got.Reason != reason {
		t.Fatal("explanation was truncated or lost its rule")
	}
	raw, _ = json.Marshal(map[string]string{"decision": "allow", "comment": strings.Repeat("안으로", 2401)})
	if got := ParseVerdict(string(raw)); got.Action != "" {
		t.Fatal("accepted unbounded explanation")
	}
}

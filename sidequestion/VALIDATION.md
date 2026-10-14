# `/btw` 확인 기록

날짜: 2026-09-10. 분기: `codex/btw-side-question`. 기준선: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

독립형 PostgreSQL 테스트 라이브러리와 데이터 디렉터리를 사용하세요. 실제 모델 자격 증명은 독립 실행형 테스트 환경에만 주입되며 코드나 이 기록은 작성되지 않으며 제품 기본 모델은 수정되지 않습니다. Go 1.26.3, 노르마 v0.3.6, Next.js 16.2.9.

실제 모델 대화, 반환 개체, 엔지니어링 주장 및 Qwen 원본 리뷰 텍스트는 API 자격 증명이 없는 [validation-2026-09-10.json](validation-2026-09-10.json)에 저장됩니다.

## 엔지니어링 검사

| 범위 | 결과 | 증거 |
| --- | --- | --- |
| 구조화된 메시지, 도구 매개변수 심층 복사 | 패스 | `TestCheckpointDeepCopyAndBoundaries` |
| 요약/압축 요청 미포함, 전체 답변 및 최종 공개, 절반 부분 답변 제외 | 패스 | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 실제 모델 풀 멤버십 | 합격 | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| 도구 페어링, 20세트 재생, 예산 클리핑 및 한도 초과 오류 | 합격 | `TestBuildRequestCompactionToolPairingAndBudget` |
| 메인 바이패스 병렬, 양방향 분리 | 통과 | 차단 제공자`TestMainSideConcurrencyAndIndependentCancellation` |
| 도구 실행 없음, 스트리밍/비스트리밍, 실패 시 사용 | 합격 | `TestServiceNoToolsAndUsageOnFailure` |
| 실제 표준 ChatAgent + 로컬 읽기 도구, 주요 기록/활동 격리 | 통과 | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, 스트리밍 및 비스트리밍 하위 사용 사례 |
| 지속성, 페이징, 멱등성, 재시작 유지 부분 답변 | 패스 | `TestSideHistoryIdempotencyPagingAndRecovery` |
| 명확하고 늦은 쓰기 경합, 상위 리소스 삭제, 버전 비교 | 패스 | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent/작업자 보관 및 복구, v1/v2/v3 | 패스 | `TestSideTaskArchiveVersions` |
| 세 가지 상위 인터페이스, 인증, 리소스 소유권, 작업자 논리적 삭제 | 패스 | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 사용 중인 기본 세션 우회, 독립적인 SSE 재연결/연결 해제, 취소, 삭제 가능 | 통과 | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 상위 세션당 1개/전역적으로 동시성 4개 | 패스 | 두 가지 `TestSideHTTP…` 사용 사례 |
| 제출 전 스냅샷이 라이브러리에 삭제되고 문제가 다시 시작되며 이전 세션은 위조될 수 없습니다. | 패스 | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| 캐시의 구성이 삭제되거나 모델이 변경된 후 계속 진행 거부 | 패스 | `TestSideRejectsDeletedOrChangedCachedProfile` |
| 보관하기 전에 취소하고 최종 답변과 사용량이 저장될 때까지 기다리세요 | 패스 | `TestSideTaskDrainPersistsBeforeArchive` |
| 스트리밍 소비자가 사전에 취소할 경우, 사용량 및 바이패스 어트리뷰션은 1회만 기록됩니다 | 패스 | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| 새 스냅샷 게시를 계속하려면 자동으로 복원된 작업자/기한 실행 컨텍스트를 다시 시작하세요. | 패스 | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| 관련 패키지 레이스 체크 | 합격 | 다음 명령 |
| TypeScript 및 프로덕션 빌드 | 패스 | `npx tsc --noEmit`, `npm run build` |
| 새로운 프런트엔드 모듈 Biome | 합격 | `biome check`, 3개의 새로운 모듈 |

별도의 일회용 데이터베이스에서 `ARTEX_PG_DSN`을 구성한 후 자동 검사를 재현할 수 있습니다(프로덕션 라이브러리를 가리키지 않음):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

Full Go 회귀가 완전히 녹색이 아닙니다. `server` 패키지에 대한 두 가지 기존 테스트가 임시 디렉터리 정리 단계에서 실패했으며 둘 다 `TempDir RemoveAll … directory not empty`을 보고했습니다.

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

위 수정되지 않은 기준선에서 소스 코드를 내보낸 후 동일한 격리 환경에서 다시 실행 `server` 패키지에서는 이 두 가지 정리 실패도 발생합니다. 기준 실행이 나타납니다 `TestCoreTaskLifecyclePG` 에 대한 대상 노드 번호 어설션이 실패했습니다. 최종 수정 `server` 이 어설션이 없으면 회귀는 실패합니다. 다른 패키지도 통과했고, 이 우회 관련 사용 사례와 경합 검사도 통과했습니다. 기본 문제는 이 승인에 대해 통과된 것으로 표시되지 않으며 숨겨진 문제에 대해 기존 주장도 수정되지 않습니다.

Next.js 빌드는 기존 다중 잠금 파일/작업공간 루트 추론 경고를 출력합니다. 빌드가 완료되고 모든 페이지가 성공적으로 생성됩니다.

## 브라우저 확인

Codex 인앱 브라우저를 사용하여 독립형 로컬 Go 서비스와 Next.js 개발 서버를 연결하세요. 데스크톱 및 390 × 844 좁은 화면에서 다음 수동 자동화 작업을 완료하고 스크린샷과 브라우저 로그를 확인하세요.

- 일반 채팅 중에 `/btw`를 입력하면 메인 콘텐츠와 바이패스가 동시에 표시됩니다. 데스크탑 사이드바는 정상입니다.
-지속적인 질문; 바이패스가 중지된 후에도 생성된 부분을 유지합니다. 주요 프로세스가 계속됩니다.
- 패널을 닫아도 요청은 계속되며 다시 열면 완료된 답변이 복원됩니다. 빈 `/btw`는 페이지를 새로 고친 후 기록을 복원합니다.
- 좁은 화면 서랍의 입력, 버튼, 히스토리, 닫기 작업은 가로 오버플로 없이 정상적으로 작동합니다.
- 사용확인 팝업창을 지웁니다. 삭제 후에는 기록이 사라지고 주요 기록과 스냅샷이 유지됩니다.
- 작업 MainAgent와 두 Worker가 별도로 요청하고 전환하며 Agent 라벨과 내역이 혼선되지 않습니다.
- 차단 로컬 모델 고정 장치는 작업자를 계속 실행합니다. Worker 기본 입력 상자에서 `/btw`를 제출하면 우회를 중지한 후에도 Worker는 여전히 실시간 실행 및 자체 일시 중지 버튼을 표시하고 우회는 부분 답변을 저장합니다.
- 브라우저 오류/경고 로그가 비어 있습니다.

제어 가능한 고정 장치는 실제 모델의 출력 속도에 의존하지 않고 동시 타이밍을 정확하게 확인하는 데 사용됩니다. 디버깅하는 동안 두 작업자 런타임 검사가 유효한 동시성 창(작업 종료/조기 응답 종료)을 형성하지 않았으며, 픽스처를 수정하고 다시 실행하여 통과했습니다. 이러한 초기 작업은 유효한 패스로 기록되지 않습니다.

## 실제 모델 대화

우선순위 감지 `grok-4.6`, OpenAI 호환 인터페이스 `http://127.0.0.1:12580/tingly/openai`. HTTP 200을 감지하고 모델 이름을 반환합니다 `grok-4.6` 그리고 `READY`, 2.82초가 걸렸습니다. 사용 가능을 선호하므로 Tingly `glm` 또는 지혜 스펙트럼 `glm-5.3` 백업 체인; 이번에는 이 두 가지 백업 서비스가 확인되지 않았습니다.

| 시나리오 | 실제 결과 |
| --- | --- |
| 메인 세션이 실행되는 동안 자산, 대상, 태그 요청 | `redhaze.top` 반환, 홈 페이지 읽기 및 요약 대상 `BTW-REAL-0910`; 우회 완료, 16.97초 |
| 메인 세션이 홈페이지 읽기를 마친 후 도구에 기초를 요청하세요 | WebFetch 200, 컬 점프 301 → 302 → 200, 페이지 제목을 올바르게 인용하세요. 7.24초 |
| Bypass에서는 Bash가 테스트 파일을 생성해야 합니다 | 실행이 거부되었습니다. 대상 파일이 생성되지 않았습니다. 7.74초 |
| 완료된 우회는 기본 컨텍스트를 변경하지 않습니다 | 주요 기록 SHA-256은 주요 활동 기록과 일치합니다. 우회 도구 실행 횟수는 0 |
| 후속 질문에 대해 Go 서비스를 중지/다시 시작하세요 | 기본 에이전트를 다시 실행하지 않고도 이전 3개의 우회 기록을 유지하고 영구 스냅샷에서 직접 자산, 태그 및 제목에 응답합니다. |
| 새 세션은 Grok 비스트리밍 구성을 사용합니다 | 정확하게 자산에 답하고 `ATOMIC-0910`;반환 및 사용량 저장: 입력 11734, 출력 138, 캐시_읽기 11520 |

자산 케이스의 기본 세션은 WebFetch 및 Bash/curl을 사용하여 공개 홈페이지를 읽고 랜딩 페이지는 `https://id.redhaze.top/home`, "RedHaze 기술 RedHaze 그룹 · 글로벌 종합 그룹 포털"이라는 제목. Bash는 응답을 로컬 테스트 파일에 일시적으로 저장합니다. 쓰기는 원격으로 수행되지 않습니다. 이 사실은 "Bypass에는 실행 도구가 없습니다"와 별도로 확인됩니다.

주요 성적표 확인 값: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**사용 제한:** Tingly의 Grok 스트리밍 응답은 사용량을 반환하지 않습니다. 별도로 검증을 위해 `stream_options.include_usage=true`를 직접 전송합니다. HTTP 200, 12 데이터 프레임, 0 사용 프레임. 따라서 스트리밍 테스트에서 0은 엔드포인트가 사용량을 제공하지 않으며 청구가 없는 것으로 해석될 수 없음을 나타냅니다. 흐르지 않는 사용 및 설비의 실패/취소된 사용이 올바르게 저장됩니다.

## Qwen 검토

검사 모델 `qwen-flash`, OpenAI 호환 인터페이스 `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. 처음 3개의 실제 우회 대화, 기본 세션 도구 기반 및 프로젝트 어설션이 제공됩니다. `verdict: accept`, `concerns: []`가 반환되며, 답변은 자산, 태그, 페이지 읽기 증거와 일치하는 것으로 간주되며 우회 도구는 제약 조건 준수를 거부합니다. 사용 검토: 프롬프트 6625, 완료 312, 총 6937.

이 Qwen 검토 범위에는 나중에 추가된 서비스 재시작 및 비스트리밍 테스트가 포함되지 않습니다. Qwen의 "쓰기 없음" 일반화는 너무 광범위합니다. 기본 세션 컬은 위에 명시적으로 문서화된 로컬 응답 임시 파일을 생성합니다. 동시성, 제로 도구 실행 및 성적표 격리는 엔지니어링 주장에 따라 판단되며 모델 검토는 답변 품질 평가에만 도움이 됩니다.

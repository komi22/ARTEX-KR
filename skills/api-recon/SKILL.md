---
name: api-recon
description: 웹사이트 API 인터페이스 수집 시 호출되는 스킬입니다.
---

# API Recon: 프런트엔드 API 정찰

승인된 대상의 **백엔드 API** 경로, 메서드, 매개변수, 응답 형식과 프런트엔드 라우트 및 UI에서 요청을 발생시키는 지점을 수집합니다.

## 범위와 금지 사항

이 스킬은 API와 매개변수의 표면을 파악하는 데만 사용합니다. 취약점 악용이나 침투 테스트 단계는 수행하지 않습니다.

| 영역 | 허용 | 금지 |
| --- | --- | --- |
| 대상 | 경로·메서드·매개변수·라우트·UI 트리거 열거 | SQL 삽입, XSS, 권한 우회, 무차별 대입, 퍼징, 요청 변조 공격, 파괴적 작업 |
| 인증 | 클라이언트의 로그인·권한 판단을 후킹하고 모의 응답으로 화면을 렌더링 | 계정이나 비밀번호를 묻거나 추측, 실제 로그인 양식 제출 |
| 런타임 | 자격 증명 없이 후킹과 스텁으로 로그인 뒤의 프런트엔드 화면 관찰 | 실제 백엔드 세션을 얻어야만 계속되는 절차 |

### 자격 증명 없이 동적 분석하기

1. `preload.js` 또는 `runtime_harvest.js`로 로그인, 권한, 메뉴 등의 초기 API를 가로채고 모의 응답을 준비합니다.
2. 업무 데이터 요청에는 구조가 맞고 성공 코드를 담은 빈 모의 응답을 반환합니다.
3. 로그인 뒤의 SPA 화면이 렌더링되도록 하여 추가 XHR, fetch, WebSocket 요청을 기록합니다.
4. 빈 표와 자리 표시자 UI는 정상입니다. 실제 로그인이나 취약점 테스트로 넘어가지 마세요.

목표는 프런트엔드가 **어떤 요청을 보내는지** 확인하는 것입니다. 모의 응답의 데이터가 실제 데이터일 필요는 없습니다.

### 반드시 지킬 순서

| 하지 말아야 할 일 | 대신 할 일 |
| --- | --- |
| 1단계 수집 전에 큰 `index-*.js`를 curl이나 grep으로 읽어 API를 추출 | `OUTDIR/harvest_static.py`를 먼저 실행 |
| 수집 스크립트를 쓰지 않고 별도의 `extract_apis.py`를 작성 | `OUTDIR/harvest_static.py`를 대상에 맞게 수정하고 다시 실행 |
| 같은 grep 또는 명령이 두 번 이상 실패해도 그대로 반복 | 도구 로그와 수집 결과를 읽고 방법을 바꿈 |
| 접근 제어 A/B를 건너뛰고 `scripts/`의 원본을 바로 실행 | 스크립트를 `OUTDIR`에 복사한 뒤 대상에 맞게 조정 |
| 실제 계정, 비밀번호, OTP, OAuth 인증 시도 | 클라이언트 후킹과 모의 응답 사용 |
| 실제 데이터를 얻겠다며 로그인 스텁을 건너뛰고 요청을 변조 | API 정찰 범위에서 발생한 요청만 기록 |
| 삭제, 민감 정보 내보내기, 대량 쓰기 등 되돌리기 어려운 동작 | 읽기 위주의 화면 탐색으로 범위를 확인 |
| 동적 페이지나 매개변수 검증이 끝나지 않았는데 전체를 파악했다고 보고 | 빠진 범위와 한계를 명시하고 필요한 단계를 수행 |
| 요청 한 건만 보고 필수·선택 매개변수를 단정 | 여러 샘플의 차이와 유효성 검사 규칙·오류를 함께 확인 |

## 정적 분석과 런타임 분석

| 분석 | 얻을 수 있는 것 | 한계 |
| --- | --- | --- |
| 정적 분석(JS 번들) | 엔드포인트 경로, 프런트엔드 라우트, 매개변수 후보 | 메서드와 실제 요청 값이 빠질 수 있음 |
| 런타임 분석 | 메서드, 요청 본문, 응답, 동적 URL, WS/SSE, 여러 샘플의 차이 | 해당 화면이 실제로 렌더링되어야 하며 샘플 하나로 필수 여부를 판단할 수 없음 |

| 실행 모드 | 도구 | 용도 |
| --- | --- | --- |
| 심층 수집 | `runtime_harvest.js` | API, 메서드, 매개변수, 응답, WS/SSE를 반복 수집 |
| UI 탐색 | 브라우저와 `preload.js` | 탭, 대화 상자, 양식 등의 기능을 직접 열어 요청 확인 |
| 병행 | 심층 수집 후 UI 탐색 | 가장 넓은 범위를 확인하지만 시간이 더 걸림 |

경로는 정적 수집으로, 매개변수는 **경로 주변 코드 확대 → UI 바인딩 추적 → 여러 요청 비교 → 오류 응답 확인**으로 조사합니다. 예시는 [reference.md](reference.md)의 J절을 참조하세요.

## 완료 기준

아래 항목을 모두 확인해야 정찰이 끝났다고 보고할 수 있습니다.

- [ ] 정적 수집: `api_static.txt`, `routes.txt`, `js/` 생성
- [ ] 런타임 수집: 심층 수집이나 UI 탐색을 하나 이상 수행하고, 후킹과 동적 열거가 실제로 동작하는지 확인
- [ ] 화면 진입: 업무 라우트가 `/login`으로 되돌아가지 않는지 확인(해시 라우팅 포함)
- [ ] 매개변수: UI 탐색을 했다면 트리거 행렬과 `param_samples.json`을 만들고, 5단계에서 `params_merged.json`으로 병합
- [ ] 빈 모듈 화면: 필요하면 4단계에서 권한 트리를 복원하고 3단계를 다시 실행해 모듈 API를 확인
- [ ] 결과: 5단계의 출력물을 모두 만들고 발견한 서비스·엔드포인트 자산을 `insert_assets`로 빠짐없이 기록

---

## 스크립트와 실행 전 점검

`scripts/`는 참고용 템플릿입니다. 원본을 그대로 실행해 최종 결과로 제출하지 마세요. 먼저 읽고, 대상에 맞게 수정한 복사본을 `OUTDIR`(예: `recon/`)에 저장하고, 변경 사항을 `CHANGES.md`에 기록합니다.

| 점검 | 시점 | 복사할 스크립트 | 주로 조정할 항목 |
| --- | --- | --- | --- |
| **A: 정적 수집** | 0단계 이후, 첫 수집 전에 | `harvest_static.py`, `spider_mpa.py` | 엔드포인트 정규식, webpack/Vite 매니페스트와 `publicPath`, MPA 제외 경로·쿠키 |
| **B: 런타임 수집** | 2단계 이후, 실행 전에 | `runtime_harvest.js`, `preload.js`, `config.json` | 쿠키·localStorage 키, 로그인·권한 응답 스텁, API 접두사, 해시 라우팅 |

SPA에서는 0단계를 마친 다음 **수집 스크립트를 복사하고 곧바로 실행**합니다. 큰 메인 번들을 먼저 curl이나 grep으로 읽어 수동 추출하지 마세요. 수집 결과의 파일 수와 행 수를 확인하고, 404가 나오거나 결과가 부족하면 `OUTDIR`의 스크립트를 고쳐 다시 실행합니다. 1b단계 이후의 grep은 수집한 `OUTDIR/js/*.js`에 한정합니다. MPA라면 0단계 다음에 `spider_mpa.py`를 실행합니다.

## 도구 출력 제한

- 100KB가 넘는 번들 파일은 대화 문맥에 직접 읽거나 붙여넣지 말고 `OUTDIR` 스크립트로 일괄 처리합니다.
- grep 결과는 `head -20` 또는 `-m 5`로 제한합니다. 대화에는 경로와 발견 사항의 요약만 남깁니다.
- `wc -l`, `ls | wc -l`로 결과 규모를 확인하고 디렉터리 전체를 출력하지 않습니다.
- 정규식을 살펴볼 때는 작은 HTML 또는 50KB 이하의 코드 조각을 한 번만 확인한 뒤 정적 수집으로 진행합니다.
- 명령 예시와 문제 해결 방법은 [reference.md](reference.md)를 참조합니다.

## 실행 순서

```text
0단계 대상 분류와 OUTDIR 준비
  → 점검 A → 1단계 정적 수집
  → 1b단계 매개변수 후보 분석
  → 2단계 인증·권한 판단 지점 분석과 config.json 작성
  → 점검 B → 3단계 런타임 수집과 매개변수 행렬
  → 필요하면 4단계 권한 트리 복원 후 3단계 재실행
  → 5단계 결과 병합·보고와 발견한 서비스·엔드포인트의 insert_assets 등록
```

앞 단계를 완료한 뒤 다음 단계로 이동합니다.

1. [ ] **0단계**: SPA/MPA 분류와 `OUTDIR` 준비
2. [ ] **점검 A와 1단계**: 스크립트 복사·수정 후 바로 실행하고 `wc -l`로 결과 확인
3. [ ] **1b단계**: 코드 주변과 UI 바인딩을 분석해 `param_candidates.json` 작성
4. [ ] **2단계**: 인증과 권한 판단 지점 세 가지를 분석해 `config.json` 작성
5. [ ] **점검 B**: 런타임 스크립트를 대상에 맞게 수정
6. [ ] **3단계**: 심층 수집이나 UI 탐색을 수행하고 `param_samples.json` 작성
7. [ ] **4단계**: 필요한 경우 권한 트리를 복원하고 3단계 재실행
8. [ ] **5단계**: 결과 병합, 보고서 작성, 발견한 자산을 `insert_assets`로 등록

---

## 0단계 — 분류

항목 HTML을 당겨서 **`OUTDIR`을 생성합니다**(스킬에서 `scripts/`를 변경하지 마세요):

- **SPA**: 빈 셸 + `<div id=app>` + 청크 → 1~5단계
- **MPA**: SSR + `<form>`, 엔드포인트 번들 없음 → 액세스 제어 A 후:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

`forms.txt`, `links.txt`, `api_inline.txt`를 출력합니다. SPA 형식 ≒ 0인 경우 → 1단계로 전환합니다.

---

## 1단계 — 정적

[스크립트 및 액세스 제어](#스크립트-및-액세스-제어) · [도구 및 출력 제약](#도구-및-출력-제약)을 준수합니다.

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

수집: HTML 스크립트 구문 분석 → webpack/Vite 매니페스트 → 모든 게으른 청크 다운로드 → `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` 생성.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- 청크 수 대 매니페스트: 404, 수확을 변경하고 다시 시도해야 합니다. 청크를 하나씩 수동으로 컬링하지 마십시오
- `api_static.txt`가 너무 작습니다 → OUTDIR에서 엔드포인트 정규화를 완화하고 다시 실행하세요(참조 참조)

### 1b단계 — 매개변수 역방향

경로는 1단계의 경로입니다. 매개변수 필드는 별도로 재검토해야 합니다. grep 규칙은 [도구 및 출력 제약 조건](#도구-및-출력-제약)을 참조하세요.

**완성 기준**: 필드 이름, 전송 위치, 유형 추론, 필수 여부, 샘플 값, 신뢰 수준 등 중요한 인터페이스에서 답변할 수 있습니다.

#### 1b.0 — 전송 형식

| 양식 | 매개변수는 어디에 있습니까 | 정적 우선 |
|---|---|---|
| REST JSON | 본문 + 쿼리 | 앵커 옆의 경로 `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql 템플릿, `$page: Int` |
| 전통적인 형태 | Urlencoded | `<form>`, `FormData` |
| 파일 업로드 | 다중 부분 | `FormData.append` |
| 경로 매개변수 | `/user/:id` | 라우팅 테이블 + `useParams` / `$route.params` |
| 암호화/서명 | `sign`/`data`로 패키징 | 후크 암호화 기능 입력 매개변수(참조 섹션 D) |

출력: 각 인터페이스는 `transport: query|json|form|graphql|encrypted`로 표시됩니다.

#### 1b.1 — 앵커 포인트 창 확장

알려진 경로를 앵커로 사용하고 창을 확장하여 패키지 개체를 찾으세요.

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| 포장층 | 매개변수 단서 |
|---|---|
| 액시스 인스턴스 | `data` / `params` |
| 통합요청 | 전역 필드에 인터셉터 삽입 |
| OpenAPI 클라이언트 | 메소드 서명 생성 |
| 반응 쿼리 / SWR | 후크 두 번째 매개변수 |
| Vue 컴포저블 | 구성 가능한 입력 매개변수 |

유형 잔여물: `yup`/`zod`/rules, `Form.Item name=`, 임베디드 Swagger.

→ `param_candidates.json`：`{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — 바인딩 레이어

```
Form field → onFinish/handleSubmit → transform → API payload
```

| 바인딩 소스 | 기술 |
|---|---|
| 양식 제출 | 제출 → 변환 → API |
| 테이블 검색 | `getFieldsValue()` → `params` |
| 라우팅 | `:id` / `?tab=` |
| 인터셉터 | 전역 `tenantId`, 페이징, 서명 |
| 열거 선택 | `options` → API 열거형 값 |

DevTools 호출 스택은 `fetch`/`XHR.send`부터 패키지 함수를 추적합니다.

#### 1b.3 — 그룹 패키지에 대한 세 가지 질문(≠ 2단계 3개의 인증 게이트)

| 질문 | 대답할 내용 |
|---|---|
| **조립** | 페이로드 빌드 및 변환 추적은 어디에 있습니까 |
| **확인** | 필수, 패턴, 열거형 |
| **트래픽** | 경로 / 쿼리 / 본문 / 멀티파트 / 헤더 |

인터셉터 게이트(2단계)는 우연히 전역 주입 필드(승인, `X-Tenant-Id`, 기호)를 읽습니다.

#### 1b.4 — 3단계와 연결

후보 필드는 정적/바인딩 레이어에서 나옵니다. **필수/선택/조건부 종속성**에는 3단계 매개변수 행렬 + diff + 5단계 오류 반전이 필요합니다.

---

## 2단계 — 인증의 세 가지 문

`OUTDIR/js/` grep(`head` 포함)에 `config.json`을 작성합니다(레시피 참조 참조).

| 문 | 질문 | 키워드 |
|---|---|---|
| **렌더링 게이트** | 로그인되었는지 확인하는 방법은 무엇입니까? | `isLogin`, `getToken`, 쿠키/localStorage |
| **인터셉터 게이트** | `/login`을 트리거하는 것은 무엇입니까? | `response_code`, `errno`, axios 인터셉터 |
| **컨텐츠 게이트** | 메뉴/권한은 어디서 나오나요? | `menu`, `permission`, `role`, `acl`, `routes` |

localStorage 키를 자격 증명으로 사용하지 않도록 설정합니다. 청크/요청 체인에서 확인해야 합니다.

**Exit = Access Control B**: 결론은 `config.json`으로, `OUTDIR/runtime_harvest.js` / `preload.js`를 변경합니다.

### 2b단계 — API 관찰(선택 사항)

OUTDIR에서 `preload.js`를 사용하여 세션 키 이름, 승인 및 중첩된 API URL을 확인하세요.

| 구성 | 출력 |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | 헤더 관찰 |
| `extractUrlsFromResponse: true` | 응답 하위 API |
| `observe.storageReads/cookieReads: true` | 백필 구성 |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

라운드당 내보낸 적용 범위: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## 3단계 — 런타임

액세스 제어 B를 통과하고 위의 경계 및 금지 사항을 지키세요. 자격 증명 없이 모의 응답을 사용하는 정책도 적용됩니다.

`config.json`은 `"runtimeMode": "depth" | "coverage" | "both"`(템플릿에 대한 참조 참조).

### 후크 및 스텁(깊이 + 적용 범위로 공유)

| 레이어 | 범위 | 목적 |
|---|---|---|
| L1 정밀 | 인증/권한/부트스트랩 스텁 | 첫 화면 인증을 지나 |
| L2 음성 교정 | 모든 JSON 응답 | 등록되지 않은 코드 → 성공 |
| L3가 엄폐 | L1의 `/api` 등이 누락되었습니다. | 빈 성공 본문, 열린 UI |

- **깊이**: 가짜 인증 + `forward` 비즈니스 코드 변경 + `stubs`; `routes`(해시/기록)를 탐색합니다. `runtime_api.json` 출력
- **범위**: **document-start**는 `preload.js`(CDP `addScriptToEvaluateOnNewDocument` 또는 Userscript)를 삽입합니다.

확인: `window.__API_RECON_PRELOAD__`가 존재합니다. 비즈니스 경로가 `/login`을 반환하지 않습니다.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — 적용 범위 동적 열거(필수)

1. 기본 탐색/사이드바 - 클릭할 때마다 네트워크가 나올 때까지 1~3초 기다립니다.
2. Tab — `role=tab`、`.ant-tabs-tab`
3. 표 - 첫 번째 행 보기/수정/세부정보
4. 툴바 - 내보내기, 필터링, 생성(**되돌릴 수 없는 삭제 방지**)
5. 각 모듈 - API/라우팅 병합
6. SPA - `routes.txt`에 대한 경로 제어 `pushState`가 발견되었습니다(MPA 금지)

**매개변수 트리거 매트릭스**(필수): 각 모듈은 작업 유형에 따라 한 번 기록됩니다. **diff 다중 샘플**:

| 운영 | 일반적으로 추가 매개변수 |
|---|---|
| 목록 첫 화면 | 페이지 매김 + 기본 필터 |
| 검색을 클릭 | 키워드, 필터 |
| 고급 필터링 | 추가 선택사항 |
| 신규/수정 | 완전한 엔터티 |
| 일괄/내보내기/정렬 | `ids[]`, `exportType`, `sortField` |

아웃바운드 본문/헤더는 **스텁**에서 여전히 true입니다. 요청에 따라 달라질 수 있습니다. 기록 → `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**：`neutralizeVueRouter: true` + document-start preload
- **반응**: `routes.txt` + 사이드바 클릭 + `pushState`
- **둘 다**: 먼저 깊이 3a, 적용 범위 3b

---

## 4단계 — 권한 트리 복원

**트리거**: 모듈 페이지가 비어 있음/경로당 부트스트랩만(예: 로케일) → 콘텐츠 게이트가 전달되지 않습니다.

| 현상 | 의미 |
|---|---|
| 쉘 항목 성공 | 렌더링 게이트 + 인터셉터 게이트 통과 |
| 항목 누락/사이드바 클릭 | 스텁 모양 또는 불완전한 권한 코드 |
| 경로당 동일하고 최소한의 API | `v-if permission` 실패 |
| `routes.txt`는 번들보다 훨씬 작습니다 | 인증 모듈에서 완료해야 합니다 |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

일반적인 체인: `role_permissions`(플랫 코드) + `permissions/all`(트리) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

중간 출력: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

스텁 확인: 외부 `response_code`는 인터셉터 게이트와 일치합니다. 플랫 코드는 트리와 정렬됩니다. `routes`는 `route_map`의 모든 링크를 포함합니다.

`config.json`을 업데이트한 후 3단계**를 다시 실행하세요. 대규모 SPA 조정 가능 `waitUntil`, `routeTimeout`, `perRouteMs`(섹션 참조 A3/I 참조).

---

## 5단계 — 통합 및 보고

### 출력 테이블

| 파일 | 무대 | 내용 |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | 정적 번들 및 경로 |
| `param_candidates.json` | 1b | 정적 매개변수 필드 후보 |
| `config.json` | 2 | Three Doors + 런타임 구성 |
| `runtime_api.json` | 3a | 심도 상세 녹화(WS/SSE 포함) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | 여러 샘플, 클릭 로그, 세부정보 |
| `route_map.json` 등 | 4 | 권한 트리 중간 파일(실행된 경우) |
| `params_merged.json` | 5 | 매개변수 필드 + 신뢰도 병합 |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | 라우팅, API, 매개변수, 기능 지점, 제한사항 |
| **삽입_자산** | 5 | 모든 서비스 및 엔드포인트 자산을 자산 라이브러리에 쓰기 |

### 5b — 매개변수 병합

`param_samples.json`과 다릅니다. **범용 병합 스크립트가 없습니다**. 신뢰도 규칙(높음/중간/낮음/보류)은 참조 J7을 참조하세요.

### 5c — 오류 역방향

불완전한 요청은 400(** 매개변수 조정, 비취약점 테스트**)을 읽기 위해 승인 범위 내에서 전송될 수 있습니다: `field 'x' is required`, 열거 오류 등. 암호화하기 전에 `data` 래퍼, `variables` 및 `bizData`를 참고하세요.

보고서에는 런타임 모드, 정적/런타임 API 번호, 매개변수 신뢰도, 발견되지 않은 모듈, 상대 참조 스크립트의 `CHANGES.md` 요약이 표시되어야 합니다.

`site_map.json` 권장 구조:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

더 많은 필드와 grep 레시피는 [reference.md](reference.md)를 참조하세요.

---

## 일반 지침

- **프레임워크 독립적**: webpack/Vite/Angular 지연 로드 방법은 동일
- **전송**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-웹이 범위에 속하지 않음
- **SSR**: 클라이언트 가져오기가 기록 가능합니다. RSC/서버 작업은 완전히 열거 가능하지 않습니다.
- **사각지대**: JSVMP, WASM, HMAC/mTLS 강력한 검증 → 정적 + 주석 제한
- **매개변수 블라인드 영역**: 조건부 연결, 숨겨진 매개변수, WASM 패키지 → "트리거됨"/"접근할 수 없음"
- **정적은 안전망입니다**: 런타임이 차단되어도 정적은 여전히 끝점을 열거할 수 있습니다

---

## 추가 리소스

- Grep 레시피, `config.json` 템플릿, 문제 해결, 후크, 매개변수 역방향 J 섹션, site_map 템플릿: **[reference.md](reference.md)**
- 참조 스크립트 경로는 [스크립트 및 액세스 제어](#스크립트-및-액세스-제어) 표를 참조하세요

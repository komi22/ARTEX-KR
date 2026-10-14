# api-recon — 참조 매뉴얼

Grep 레시피, `config.json` 템플릿 및 문제 해결. 모든 grep은 `js/` 디렉토리에 대해 실행됩니다. 한 줄을 묶을 때 먼저 `js-beautify` 또는 `sed 's/}/}\n/g'`를 사용할 수 있습니다. 일반적으로 컨텍스트 창을 사용하는 원시 grep이 사용됩니다.

## 스크립트 설명

`scripts/`에 있는 모든 파일은 **참조 템플릿**이므로 실행하기 전에 대상 사이트에 맞게 조정해야 합니다. 일반적인 변경사항:

| 스크립트 | 일반적인 조정 |
|---|---|
| `harvest_static.py` | 엔드포인트 일반, webpack/Vite 매니페스트 구문 분석, 마이크로 프런트엔드 publicPath, 재시도/동시성 |
| `runtime_harvest.js` | 필드 이름 및 성공 값, 스텁 일치 규칙 및 본문 구조, 경로 소스, WS 기록, `waitUntil`/`routeTimeout`/`proxy` 중화 |
| `preload.js` | `loginPathRe`, L1 스텁, `neutralize.fields`, `apiPattern`, L3 활성화 여부, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` 파괴적인 링크, 쿠키, 깊이/최대, 동일 도메인 필터링 |
| `extract_route_map.py` | `routeMap` / `routeLink` 일반, KEY 명명 패턴 |
| `build_perm_tree.py` | `userRouteAuth` 구문 분석, `ROOTS`/`PREFIX_PARENT` 수준 휴리스틱, 스텁 외부 필드 이름 |
| `config.json` | 위의 모든 사이트별 매개변수에 대한 통합 입구 |

조정된 파일을 작업 작업 디렉터리(예: `recon/`)에 배치하는 것이 좋으며 참조 스크립트와 관련된 특정 변경 사항이 보고서에 기록됩니다.

---

## A. 3개의 역방향 게이트

### A1. 렌더링 게이트 — "로그인되었는지 어떻게 알 수 있나요?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

`isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))` 링크를 찾아 **저장소 키**, **컨테이너**(쿠키 대 localStorage), **인코딩**을 확인하세요.

| 인코딩 | 구성 위조 방법 |
|---|---|
| 텍스트 문자열 지우기 / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | 서명되지 않은/`alg:none` JWT 또는 번들 내 키 서명 |
| 암호화(SM2/AES/RSA) | 하드코드된 키를 찾으세요. 렌더 게이트는 디코딩 가능한 blob만 필요한 경우에만 위조될 수 있습니다. 그렇지 않으면 정적 비밀입니다 |

→ `cookies` / `localStorage`에 씁니다.

### A2. 인터셉터 게이트 — "무엇이 /login을 트리거합니까?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(로그인되지 않음|다시 로그인해주세요|로그인이 만료되었습니다|unauthorized|로그인이 유효하지 않습니다|승인하다|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

확인: **필드 이름**, **성공 값**(보통 `0` 또는 `200`), **점프를 트리거하는 실패 값**. 정크 세션으로 확인:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ `neutralize.fields` + `neutralize.success`를 씁니다.

### A3. Content Gate — "메뉴/권한은 어디에서 오는가?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**2계층 데이터**(공통 엔터프라이즈 백엔드):

| API | 일반적인 페이로드 | 소비자 |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | 경로 보호, 버튼 수준 ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | 사이드바 메뉴 렌더링 |
| 번들의 `userRouteAuth` | `{ CODE: { url, name? } }` | 코드 → 프런트엔드 경로 |
| 번들의 `routeMap` | `{ KEY: { name, link } }` | 별칭 확인(웹팩 `o.DASHBOARD`) |

소비자 코드 읽기 확인: `getResultTree(tree, permissions)` 필터링 방법, 확인할 필드 `v-if` / `hasAuth(code)`.

**수동 포지**(소규모 사이트): 허용되는 페이로드 빌드 → `stubs`.

**전체 권한 트리 복원**(대규모 사이트, 사이드바/하위 모듈이 여전히 비어 있음): **섹션 I**을 참조하세요.

---

## B.config.json 템플릿

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

필드 설명:
- `runtimeMode`：`depth`（Puppeteer）、`coverage`（browser MCP）、`both`
- `cookies[].value` 접두사: `b64json:` → base64(JSON); `json:` → 원시 JSON; 접두사 없음 → 리터럴
- `forward: true`는 실제 요청을 전달하고 코드 필드를 다시 작성합니다. `false`는 완전히 오프라인 스텁입니다.
- `mockTier`: 적용 범위 모드 사전 로드는 `L1+L2`, `L1+L2+L3`과 같은 계층을 활성화합니다.
- `routes`는 `routes.txt`에서 옵니다. 하네스는 Forge 메뉴 뒤에 `<a href>`
- `captureResponses` / `recordWs`는 심도 모드에서만 유효합니다.
- `waitUntil`: `domcontentloaded` 정지를 방지하려면 대규모 SPA에 `networkidle2`를 사용하세요.
- `routeTimeout`: 단일 경로 `page.goto` 시간 제한(밀리초)
- `proxy`: 인형극 `--proxy-server`; `HTTP_PROXY` / `HTTPS_PROXY`로 설정할 수도 있습니다.

### B1. 이중 스텁 템플릿(role_permissions + 권한/모두)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

외부 필드 이름(`response_code` / `code` / `data`) A2 인터셉터 도어와 일치해야 합니다.`permissions` 트리의 모든 리프 코드를 덮어야 합니다.

---

## C. 적용 범위 모드: 사전 로드 구성

`scripts/preload.js` 상단에 있는 `CONFIG` 개체를 편집하거나 CDP를 통해 삽입하기 전에 교체하세요.

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* 동일 config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

확인: `window.__API_RECON_PRELOAD__ === true`이고 경로 이름이 안정적입니다.

녹화 결과 내보내기:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. 프리로드 / 런타임 후크 기능

사전 로드(적용 범위) 및 Runtime_harvest(깊이) 내장 브라우저 후크 기능 및 적용 범위:

| 후크 기능 | API 검색의 가치 | 취재 |
|---|---|---|
| 후크 페치 / XHR.open | 기록 요청 URL/방법 | ✅ `recordDetail` + `__API_RECON_LOG__` |
| 후크 XHR.setRequestHeader | 인증 및 기타 헤더 검색 | ✅ `observe.xhrHeaders` |
| 후크 localStorage/쿠키 읽기 | 세션 키 이름 확인 | ⚠️ 선택사항 `observe.storageReads/cookieReads` |
| Vue는 경로를 얻습니다 | frontendRoutes 완료 | ✅ `__API_RECON_ROUTES__`(경로 로드됨) |
| Vue 라우팅 가드 무력화/로그인 점프 차단 | 모듈 트리거 API 열기 | ✅ `neutralizeVueRouter` + 기본 점프 무력화 |
| React가 경로를 얻습니다 | 보충제 경로 | ⚠️ 정적 + 클릭; 전용 후크 없음 |
| 페이지 점프 차단(로그인 경로) | 떠나기 페이지 분석 | ⚠️ 비즈니스 탐색을 차단하지 않으려면 로그인 경로만 차단하세요 |
| Hook 암호화 라이브러리(CryptoJS/SM 등) | 암호화 매개변수 → 일반 텍스트 API 본문 | ❌ Hook 암호화 기능에는 매개변수를 수동으로 입력해야 합니다. 결론은 config에 기록되어 있습니다 |
| 디버깅 우회 | 그렇지 않으면 런타임이 API를 기록할 수 없습니다 | ❌ 수동으로 처리해야 합니다. 정적은 여전히 ​​사용 가능합니다 |

---

## E. 엔드포인트 추출 규칙성(정적이 너무 적은 경우)

`harvest_static.py`의 `extract_endpoints`에서 휴식을 취하거나 수동으로:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. 문제 해결

| 현상 | 원인 → 치료 |
|---|---|
| 소수의 정적 API | 엔드포인트 방언 불일치 → 정규식 완화(섹션 D) |
| 청크 번호 `` 매니페스트 | CSS 전용이거나 청크가 배포되지 않았습니다. 404 재시도 |
| 런타임에 로그인 페이지가 계속 표시됩니다. | 렌더링 게이트 오류 → A1 검토: 키 이름, 컨테이너, 인코딩, 도메인 |
| 쉘을 입력했지만 모듈이 비어 있습니다 | 콘텐츠 문 → 단조 메뉴(A3); `routes` 경로가 잘못되었을 수 있습니다 |
| 각 경로에는 bootstrap/locale | 불완전한 권한 코드 → 섹션 I 권한 트리 복원; `role_permissions` + `permissions/all` 이중 스텁 확인 |
| 사이드바에 항목이 있지만 하위 페이지가 비어 있습니다 | 트리에 중간 노드가 없거나 코드가 `userRouteAuth`와 일치하지 않습니다 |
| 각 API에 로그인 | 인터셉터 도어 → 확인 `neutralize`; 중첩된 필드에는 확장된 걷기 논리가 필요합니다 |
| WS 프레임은 0 | 구독하기 전에 사용자 상호 작용이 필요합니다. `perRouteMs` 연장 |
| 빈 응답 본문 | `forward: true`만이 실제 응답을 갖습니다 |
| 크롬이 누락되었습니다 | 크롬을 설치하거나 `config.chromium` / `CHROMIUM` |
| 여전히 로그인 중인 많은 사람들을 모의하세요 | 후크가 너무 늦거나 `location.href` 설정자가 누락됨 → 문서 시작 + 사전 로드 |
| 목록이 완전히 비어 있습니다 | L3 빈 배열은 정상입니다. 계속해서 탭/설정/세부정보 |
| 실수로 Redux 작업을 경로로 사용 | get/set/change/clear/toggle/upload를 포함하는 필터 내부 경로 |
| Vue는 여전히 로그인으로 이동합니다 | 사전 로드가 문서 시작이 아닙니다 → 주입 타이밍을 변경합니다. 또는 `neutralizeVueRouter: false`인 경우 가드를 수동으로 해제 |
| 응답에 URL이 있지만 기록되지 않았습니다. | `extractUrlsFromResponse`를 켜십시오. 또는 `__API_RECON_DETAIL__`에서 수동으로 추출 |
| Authorization 헤더 이름을 모릅니다 | `observe.xhrHeaders` 또는 DevTools를 열어 요청 헤더 보기 |
| 런타임이 매우 느리거나 시간 초과됨 | `waitUntil: domcontentloaded`를 변경하세요. `routeTimeout`을 줄입니다. `networkidle2`를 사용하지 마세요 |
| 에이전트 연결 실패 | 확인 `proxy` / 환경변수; Puppeteer 및 컬 프록시 포트는 일관됩니다. |

---

## G. 강화 타겟

서버가 점차적으로 세션(위조할 수 없는 서명된 쿠키, 서버 측에서 렌더링되고 스텁할 수 없는 메뉴)을 확인할 때 런타임이 셸에서 중단됩니다. 예상되는 동작:

- **엔드포인트 열거에 충분한 정적** — 코드의 모듈 경로
- 승인이 허용되는 경우 실제 세션에서 동일한 하네스를 실행합니다. `forward: true`, 무력화할 필요 없음, 실제 메서드/매개변수/응답 캡처

---

## H. 단일 작업 목록

1. 승인 범위 확인
2. **읽기** `scripts/harvest_static.py` → 대상에 따라 조정 → 실행 → `api_static.txt`, `routes.txt` 검토
3. **1b단계**: 경로 앵커 창 확장 + 바인딩 레이어 → `param_candidates.json`(섹션 J)
4. A1/A2/A3 역방향 → 사이트별 `config.json` 작성
5. **실행하기 전에 `runtime_harvest.js` / `preload.js`를 읽고 조정하세요
6. `runtimeMode=depth`: `npm install` → 조정된 수확 스크립트 실행
7. `runtimeMode=coverage/both`: document-start는 조정된 사전 로드 삽입 → 브라우저 MCP 동적 열거 + **매개변수 트리거 매트릭스**
8. 모듈이 렌더링되지 않음 → **섹션 I 권한 트리 복원** → 패치 스텁 → 재실행
9. 매개변수 다중 샘플 차이 + 오류 추론 → `params_merged.json`
10. 병합 → `site_map.json` + `api_merged.txt`, 적용 범위, 공백 및 스크립트 변경 사항을 솔직하게 표시

---

## I. 권한 트리 복원(4단계 심화)

forge 단순 `menus: [{ path, show: true }]`가 유효하지 않고 하위 모듈이 아직 마운트되지 않은 경우에 사용됩니다.

### 나1. 인증 모듈 찾기

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

기록: **권한 API 경로**, **응답 필드 이름**, **소비 청크 파일 이름**.

### I2. 경로맵 추출

```bash
python3 scripts/extract_route_map.py recon/js recon/
# 출력 recon/route_map.json
```

`[!] no routeMap pattern found`: `extract_route_map.py`에서 정규식을 완화하거나 수동으로 grep:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. 빌드 권한 트리 + 스텁

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

스크립트 논리:
1. `userRouteAuth={MONITOR:{url:...},...}` 구문 분석(웹팩 별칭 `He=o.DASHBOARD` 포함)
2. `route_map.json`을 사용하여 별칭 → 실제 경로를 구문 분석하세요
3. 코드 접두어로 상위 항목 추론(`MONITOR_ALERT` → `MONITOR`)
4. 출력 `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. `--config`를 사용할 때 `config.json`의 `stubs` 및 확장된 `routes`를 자동으로 작성

**대상별 조정**(스크립트 상단):
- `DEFAULT_ROOTS`: 최상위 모듈 코드 목록
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → 상위 매핑
- `DEFAULT_EXTRA_PARENT`: 접두사가 아닌 관계의 고아 노드

### I4. 스텁 일관성 확인

```bash
# permissions 수량은 ≈ userRouteAuth 항목 수
wc -l recon/perm_codes_all.txt
# routes 재정의되어야합니다 route_map 모두 link
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. 런타임을 다시 실행하고 비교

```bash
node recon/runtime_harvest.js recon/config.json
# 대비 forge 이전과 이후 runtime_api.json 항목 수 확인 /attack、/asset 모듈이 나타날 때까지 기다리십시오. API
```

| 위조하기 전에 | 위조 후(성공) |
|---|---|
| 각 경로에 대해 동일한 3-5개의 부트스트랩 | 다른 경로는 다른 모듈 API를 트리거합니다 |
| `/api/locale/language`만 | `/api/web/...` 모듈 엔드포인트가 나타납니다 |
| `routes.txt` 한 자리 경로 | Route_map의 `routes` 80-110+ |

### I6. 여전히 실패

- **커버리지 모드**: 사이드바 + Tab을 클릭하세요. 상호작용 후 권한 제한이 요청될 수 있습니다.
- **스텁 필드**: 실제 API(컬 + 실제 세션)와 스텁 중첩 비교
- **추가 가드**: grep `hasPermission|checkRole|func.` 및 기타 버튼 수준 검사, `role_permissions.permissions` 확장
- **정적 표지**: 모듈 API 경로는 여전히 `api_static.txt`에 있으며 런타임은 METHOD/body만 추가합니다. 매개변수는 `param_candidates.json` + 기록된 샘플에 보관됩니다

---

## J. 매개변수 역엔지니어링(1b/5b/5c단계)

**방법론, 비범용 스크립트. ** 경로를 찾으려면 일반 규칙을 사용하세요. 앵커 창 확장 + UI 바인딩 체인 + 다중 샘플 비교 + 오류 반전을 사용하여 매개변수를 찾습니다.

### J1. 앵커 포인트 확장 - 경로에서 패키지 개체 찾기

```bash
# 에 Phase 1 알려진 path 앵커로
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. 포장 레이어 및 전송 형태

```bash
# axios / 통일하다 request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# 경로 매개변수
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. 검증 게이트 — 필수 / 형식 / 열거

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. 바인딩 레이어 — 양식 → API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

런타임 보충: DevTools → 네트워크 → 요청 → **초기자**(호출 스택)는 `fetch`/`send`에서 패키지 기능을 추적합니다.

### J5. 암호화 매개변수

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**암호문의 필드를 추측하지 마세요** — 후크 암호화 기능 **매개변수 입력** 및 암호화 전 일반 텍스트 페이로드를 기록합니다. `config.json` / `param_candidates.json`에 결론을 작성하세요.

### J6. 매개변수 트리거 매트릭스(3단계에서 수행해야 함)

작업당 한 번씩 각 모듈을 기록하고, diff 요청 본문/쿼리:

| 운영 | 팔로우 |
|---|---|
| 목록 첫 화면 | 페이징 기본값 |
| 검색 | 키워드, 필터 |
| 고급 필터링 | 선택 필드 |
| 신규/수정 | 완전한 엔터티 |
| 일괄/내보내기 | `ids[]`, `exportType` |
| 정렬/페이지 | `sortField`, `order` |

`param_samples.json` 출력: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. 신뢰 규칙

| 자신감 | 조건 |
|---|---|
| **높음** | 정적 호출 사이트 + 런타임 ≥2 샘플 일관성 |
| **중간** | 정적 전용 또는 1개의 런타임만 |
| **낮음** | 응답/오류 반전, 2차 검증 없음 |
| **트리거됨** | 알려진 정적 필드, UI/권한에 도달하지 못했습니다 |

### J8. 장면 빠른 매칭

| 장면 | 순서 |
|---|---|
| REST 목록 페이지 | J1 패키지 객체 → J6 4배 차이 → J3 규칙 |
| 신규/수정 양식 | J3 양식 이름 → J4 제출 체인 → 런타임 제출 + 400을 보려면 의도적으로 공백으로 남겨두기 |
| GraphQL | J2 변수 선언 → 런타임 작업 기록 변수 |
| 암호화된 본문 | J5 Hook 입력 매개변수 → 암호화 전 필드는 실제 매개변수입니다 |

### J9. API-Recon 단계를 사용한 매핑

| API 정찰 | 매개변수 정찰 |
|---|---|
| 1단계 정적 | J1 앵커 포인트 창 확장 |
| 2단계 A2 요격체 | 전역 삽입 필드(tenantId, sign) |
| 3단계 런타임 | J6 트리거 매트릭스 + `param_samples.json` |
| 4단계 권한 트리 | 모듈마다 양식이 다릅니다 → 충분한 권한이 있어야만 모든 필드가 트리거될 수 있습니다 |
| 5단계 병합 | `params_merged.json` + 신뢰 수준; 단일 샘플을 사용하지 마십시오. 필수입니다 |

### J10. 문제 해결

| 현상 | 치료 |
|---|---|
| 정적 필드 이름 런타임이 나타나지 않습니다. | "트리거됨"으로 표시하십시오. 권한 트리 추가 / 고급 필터 클릭 / 연결 각 옵션 선택 |
| 같은 길, 다른 체형 | 일반 - `action`에 따라 레코드를 분리하고 스키마를 강제로 병합하지 않음 |
| 스텁 응답이 false이지만 params | **아웃바운드 요청 보기** 본문/헤더, 스텁 응답에서 푸시백하지 않음 |
| 400개의 중첩 필드가 보고되었습니다. | 외부 포장 `data`/`bizData`/`variables`에 주의하세요 |
| GraphQL은 작업 이름만 봅니다 | `variables` JSON을 확장합니다. 정적으로 `$var: Type` 찾기 |

---

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// AssetInterceptCandidates는 삽입할 자산 입력 항목의 도메인 이름/IP/URL 후보 문자열을 추출하며, 이는 자산 가로채기 및 매칭에 사용됩니다.
// URL의 호스트는 분류되지 않으므로 "URL 전용" 서비스/엔드포인트 자산도 도메인 이름/IP 규칙에 의해 적중될 수 있습니다.
func assetInterceptCandidates(item assetInputItem) (domains, ips, urls []string) {
	add := func(dst *[]string, s string) {
		if s = strings.TrimSpace(s); s != "" {
			*dst = append(*dst, s)
		}
	}
	add(&domains, item.Domain)
	for _, d := range item.BoundDomains {
		add(&domains, d)
	}
	add(&ips, item.IP)
	add(&ips, item.ServiceIP)
	add(&urls, item.URL)
	if item.URL != "" {
		if u, err := url.Parse(item.URL); err == nil {
			if h := u.Hostname(); h != "" {
				if net.ParseIP(h) != nil {
					add(&ips, h)
				} else {
					add(&domains, h)
				}
			}
		}
	}
	return domains, ips, urls
}

// AssetInputLabel은 설명 메시지를 가로채는 데 사용되는 삽입할 자산의 짧은 식별자를 반환합니다.
func assetInputLabel(item assetInputItem) string {
	typ := strings.TrimSpace(item.Type)
	var target string
	switch {
	case strings.TrimSpace(item.Domain) != "":
		target = strings.TrimSpace(item.Domain)
	case strings.TrimSpace(item.URL) != "":
		target = strings.TrimSpace(item.URL)
	case strings.TrimSpace(item.IP) != "":
		target = strings.TrimSpace(item.IP)
	case strings.TrimSpace(item.ServiceIP) != "":
		target = strings.TrimSpace(item.ServiceIP)
	default:
		target = "(알 수 없음)"
	}
	if typ != "" {
		return fmt.Sprintf("[%s] %s", typ, target)
	}
	return target
}

// =====================================================================
// Unified asset insertion tools
// =====================================================================

// SetAssetStore wires the asset store and company store onto this ToolSet
// so the insert_assets, add_company_scope, and list_assets tools are active.
func (t *ToolSet) SetAssetStore(as *db.AssetStore, cs *db.CompanyStore) {
	t.as = as
	t.cs = cs
}

// assetInputItem is one element of the insert_assets "assets" array.
type assetInputItem struct {
	Type string `json:"type"` // root_domain|ip|subdomain|app|service|endpoint

	// ---- root_domain / subdomain ----
	Domain      string   `json:"domain"`
	ICP         string   `json:"icp"`
	RecordType  string   `json:"record_type"`
	RecordValue []string `json:"record_value"`

	// ---- ip ----
	IP           string           `json:"ip"`
	BoundDomains []string         `json:"bound_domains"`
	OpenPorts    []db.PortService `json:"open_ports"`

	// ---- app ----
	AppName     string `json:"app_name"`
	BundleID    string `json:"bundle_id"`
	Category    string `json:"category"`
	Description string `json:"description"`
	AppICP      string `json:"app_icp"`
	CompanyID   *int64 `json:"company_id"` // explicit company link (app only; others auto-attribute via scope)

	// ---- service (http) ----
	URL           string           `json:"url"`
	Technologies  []string         `json:"technologies"`
	StatusCode    *int             `json:"status_code"`
	ContentLength *int64           `json:"content_length"`
	PageTitle     string           `json:"page_title"`
	FaviconMMH3   string           `json:"favicon_mmh3"`
	Auth          []map[string]any `json:"auth"`
	ServiceName   string           `json:"service_name"`
	ServiceIP     string           `json:"service_ip"` // optional enrichment IP

	// ---- service (other) ----
	Port  int    `json:"port"`
	Proto string `json:"proto"`

	// ---- endpoint ----
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

// insertAssets is the unified insert_assets agent tool.
func (t *ToolSet) insertAssets() actool.CoreTool {
	return writeTool(
		"insert_assets",
		"새로 발견된 자산의 일괄 등록, 여러 유형을 한 번에 혼합할 수 있습니다(유형 열거 참조).\n"+
			"유형별 필수 필드: root_domain→domain; ip→ip(IPv4/IPv6 주소); subdomain→domain; app→app_name; service(HTTP)→url; service(비HTTP)→service_name+port(또한 ip/domain 중 하나); endpoint→url+method. 필드명과 유형 값은 영어 그대로 입력하세요.\n"+
			"auth/technologies/params가 추가되며 원래 값을 덮어쓰지 않습니다.\n"+
			"반환: {results:[{index,id,type}], errors:[{index,error}]}",
		obj(map[string]any{
			//task_id는 모델에 노출되지 않습니다. 작업자가 속한 작업은 SetTaskID를 통해 프로그램에 의해 정식으로 할당됩니다(핸들러 참조).
			"assets": map[string]any{
				"type":        "array",
				"description": "자산 배열, 각 요소는 자산 기록에 해당합니다",
				"items": obj(map[string]any{
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"root_domain", "ip", "subdomain", "app", "service", "endpoint"},
						"description": "자산 유형",
					},
					// root_domain / subdomain
					"domain":      str("루트 도메인 이름 또는 하위 도메인 이름(root_domain/subdomain 필수)"),
					"icp":         str("ICP 등록번호(선택사항)"),
					"record_type": str("DNS 확인 유형: A/AAAA/CNAME/MX 등(하위 도메인 선택 사항)"),
					"record_value": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "DNS 확인 값 목록(subdomain에서 선택 사항, 예: [\"1.2.3.4\",\"2.3.4.5\"])",
					},
					// ip
					"ip": str("IPv4/IPv6 주소를 입력하세요. 호스트 이름은 subdomain 유형의 domain 필드에 입력합니다. ip 유형에서는 필수이며 service/endpoint 유형에서는 IP 연결에 사용할 수 있습니다."),
					"bound_domains": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "이 IP에 바인딩된 도메인 이름 목록(IP 유형은 선택 사항)",
					},
					"open_ports": map[string]any{
						"type":        "array",
						"description": "포트 목록 열기(IP 유형은 선택사항)",
						"items": obj(map[string]any{
							"port":    intp("포트 번호"),
							"service": str("http/ssh/mysql 등 서비스 이름(선택)"),
						}, "port"),
					},
					// app
					"app_name":    str("애플리케이션 이름(앱 유형에 필수)"),
					"bundle_id":   str("번들 ID(앱 유형의 경우 선택사항)"),
					"category":    str("앱 분류(선택사항)"),
					"description": str("애플리케이션 설명(선택사항)"),
					"app_icp":     str("ICP 제출 적용(선택사항)"),
					"company_id":  intp("어트리뷰션 회사 ID(앱 유형의 경우 선택사항, 앱은 범위에 따라 자동으로 어트리뷰션될 수 없으며 명시적으로 지정해야 합니다. ID는 add_company_scope에서 반환됨)"),
					// service (http)
					"url":         str("프로토콜 및 포트를 포함한 전체 URL(HTTP 서비스에 필요하며 service_type은 자동으로 http로 설정됨)"),
					"status_code": intp("HTTP 응답 상태 코드(예: 200/301/403/404(선택 사항))"),
					"content_length": map[string]any{
						"type":        "integer",
						"description": "HTTP 응답 본문 바이트(선택 사항)",
					},
					"page_title":   str("페이지 <제목> 콘텐츠(선택사항)"),
					"favicon_mmh3": str("파비콘 MMH3 해시(선택사항)"),
					"technologies": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "지문/기술 스택 목록(예: [\"Nginx\",\"Vue\",\"Bootstrap\"](선택사항)",
					},
					"auth": map[string]any{
						"type":        "array",
						"description": "검색된 인증 정보 목록, 각 정보에는 유형/사용자 이름/비밀번호 등의 필드가 포함되어 있습니다(선택 사항, 추가하면 덮어쓰지 않음)",
						"items":       map[string]any{"type": "object"},
					},
					// 서비스(HTTP가 아닌 기타)
					"service_name": str("ssh/mysql/redis 등의 서비스 이름(서비스가 HTTP가 아닌 경우 필수)"),
					"port":         intp("포트 번호(서비스가 HTTP가 아닌 경우 필수)"),
					// endpoint
					"method": str("HTTP 메소드: GET/POST/PUT/PATCH/DELETE 등(엔드포인트 필수)"),
					"params": map[string]any{
						"type":        "array",
						"description": "요청 매개변수 목록, 각각 위치(쿼리/본문/헤더/경로)/이름/값/유형 포함(선택사항, 추가하면 덮어쓰지 않음)",
						"items":       map[string]any{"type": "object"},
					},
				}, "type"),
			},
		}, "assets"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("insert_assets이 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			var a struct {
				Assets []assetInputItem `json:"assets"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid input: " + err.Error()), nil
			}
			// task_id는 프로그램(작업자: SetTaskID)에 의해 정식으로 할당되었으며 모델이 허용되지 않습니다. - 모델 누락/잘못 전송을 방지하기 위해
			// 결과적으로 자산이 작업에 할당되지 않거나 잘못된 작업에 할당됩니다. 작업 컨텍스트(auto/pentest/chat)가 없는 호출자는 t.taskID=0입니다.
			taskID := t.taskID

			type result struct {
				Index int    `json:"index"`
				ID    int64  `json:"id"`
				Type  string `json:"type"`
			}
			type errEntry struct {
				Index int    `json:"index"`
				Error string `json:"error"`
			}

			var results []result
			var errs []errEntry

			// 자산 게이트 규칙은 한 번 로드됩니다. 판독에 실패하면 판정을 건너뜁니다(삽입이 차단되지 않음).
			// 차단 규칙 = 전역 ∪ 작업 수준 차단; 허용 규칙 = 작업 수준 허용.
			blockRules, _ := t.as.ListAssetInterceptRules()
			var allowRules []db.AssetInterceptRule
			if t.taskID > 0 {
				if tb, ta, err := t.as.TaskInterceptRulesSplit(t.taskID); err == nil {
					blockRules = append(blockRules, tb...)
					allowRules = ta
				}
			}

			for i, item := range a.Assets {
				// 자산 게이트(Asset Gate): 먼저 차단한 다음 허용합니다. 거부된 자산은 삽입이 금지됩니다(Upsert 및 후속 부작용 건너뛰기).
				domains, ips, urls := assetInterceptCandidates(item)
				if d := db.EvaluateAssetGate(blockRules, allowRules, domains, ips, urls); !d.Allowed {
					errs = append(errs, errEntry{
						Index: i,
						Error: fmt.Sprintf("자산 %s %s, 삽입 금지", assetInputLabel(item), d.Reason),
					})
					continue
				}

				typ := strings.TrimSpace(item.Type)
				var id int64
				var err error

				switch typ {
				case "root_domain":
					id, err = t.as.UpsertRootDomain(db.UpsertRootDomainReq{
						Domain: item.Domain,
						ICP:    item.ICP,
						TaskID: taskID,
					})

				case "ip":
					id, err = t.as.UpsertIP(db.UpsertIPReq{
						IP:           item.IP,
						BoundDomains: item.BoundDomains,
						OpenPorts:    item.OpenPorts,
						TaskID:       taskID,
					})

				case "subdomain":
					id, err = t.as.UpsertSubdomain(db.UpsertSubdomainReq{
						Domain:      item.Domain,
						RecordType:  item.RecordType,
						RecordValue: item.RecordValue,
						ICP:         item.ICP,
						TaskID:      taskID,
					})

				case "app":
					id, err = t.as.UpsertApp(db.UpsertAppReq{
						Name:        item.AppName,
						BundleID:    item.BundleID,
						Category:    item.Category,
						Description: item.Description,
						ICP:         item.AppICP,
						CompanyID:   item.CompanyID,
						TaskID:      taskID,
					})

				case "service":
					// distinguish HTTP vs other by presence of url
					if item.URL != "" {
						// agent may send "ip" or "service_ip" for the enrichment IP; accept both
						svcIP := item.ServiceIP
						if svcIP == "" {
							svcIP = item.IP
						}
						id, err = t.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
							URL:           item.URL,
							Technologies:  item.Technologies,
							StatusCode:    item.StatusCode,
							ContentLength: item.ContentLength,
							PageTitle:     item.PageTitle,
							FaviconMMH3:   item.FaviconMMH3,
							Auth:          item.Auth,
							IP:            svcIP,
							TaskID:        taskID,
						})
					} else {
						id, err = t.as.UpsertOtherService(db.UpsertOtherServiceReq{
							Domain:      item.Domain,
							IP:          item.IP,
							Port:        item.Port,
							ServiceName: item.ServiceName,
							Auth:        item.Auth,
							TaskID:      taskID,
						})
					}

				case "endpoint":
					id, err = t.as.UpsertEndpoint(db.UpsertEndpointReq{
						URL:    item.URL,
						Method: item.Method,
						Params: item.Params,
						IP:     item.ServiceIP,
						TaskID: taskID,
					})

				default:
					errs = append(errs, errEntry{Index: i, Error: "unknown type: " + typ})
					continue
				}

				if err != nil {
					errs = append(errs, errEntry{Index: i, Error: err.Error()})
					continue
				}
				results = append(results, result{Index: i, ID: id, Type: typ})
				t.writes.Assets++
				t.anchorOwner(id)
				if taskID > 0 {
					var sourceNodeID *int64
					if t.ownerNode > 0 {
						nodeID := t.ownerNode
						sourceNodeID = &nodeID
					}
					summary := "에이전트는 insert_assets을 통해 등록됩니다"
					if t.ownerNode > 0 {
						summary = fmt.Sprintf("작업자 인텐트 #%d가 insert_assets을 통해 등록되었습니다", t.ownerNode)
					}
					_ = t.as.SetTaskAssetSource(taskID, id, "agent", summary, sourceNodeID)
				}
				// 테스트 범위 자동 입력(source='auto'): 작업자 최상위에 명시적으로 삽입된 항목에 대해서만 누름
				// 유형과 보수적인 범위; 부작용 파생 자산은 여기를 통과하지 않으므로 범위가 맹목적으로 확장되지 않습니다. taskID=0이면 작업이 없습니다.
				// 적용 범위 전환과는 아무 관련이 없습니다. task_scope는 작업의 범위 경계(목록/쿼리의 필터링 기준)입니다.
				// 커버리지 스위치는 범위 자체를 누적할지 여부가 아니라 지표 계산을 위한 분모로 사용할지 여부만 결정합니다.
				{
					svcIP := item.ServiceIP
					if svcIP == "" {
						svcIP = item.IP
					}
					_ = t.as.AddAutoScope(taskID, typ, item.Domain, item.URL, svcIP)
				}
			}

			return jsonResult(map[string]any{
				"results": results,
				"errors":  errs,
			})
		},
	)
}

// addCompanyScope writes to company_scope table and triggers asset attribution.
func (t *ToolSet) addCompanyScope() actool.CoreTool {
	return writeTool(
		"add_company_scope",
		"회사의 [자산 범위]에 도메인 이름/IP/CIDR/ICP 등록/기업 키워드를 추가합니다. 도메인 이름, 네트워크 및 ICP는 자동으로 히트 자산을 청구하며 키워드는 범위 프롬프트로만 에이전트에 제공됩니다.\n"+
			"회사 이름은 고유합니다. 회사가 없으면 새로 만들고, 이미 있으면 다시 사용하세요(범위만 병합).\n"+
			"한 번에 한 줄씩 범위를 지정하면 시스템은 루트 도메인 이름 / URL / 단일 IP / CIDR 네트워크 세그먼트 / ICP 파일링 / 기업 키워드를 자동으로 식별합니다.\n"+
			"귀속 기준(whois/certificate/ASN 등)에 대한 이유를 제공하세요.\n"+
			"가드레일: 노출되지 않은 TLD 및 지나치게 넓은 네트워크 세그먼트(IPv4 접두사는 /16-/32여야 하고, IPv6 접두사는 /32-/128이어야 함)를 거부합니다. 잘못된 행은 건너뛰고 오류가 반환됩니다.",
		obj(map[string]any{
			"company": str("회사명(없으면 생성, 있으면 재사용, 고유한 이름)"),
			"scope":   str("자산 범위, 한 줄에 한 줄: 도메인 이름/URL/IP/CIDR/ICP 파일링/기업 키워드"),
			"reason":  str("귀속 근거(증거/출처)를 작성해야 합니다"),
			"logo":    str("회사 아이콘 URL(선택 사항, 새 회사를 만드는 경우에만 유효)"),
		}, "company", "scope"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("add_company_scope가 활성화되지 않음: CompanyStore가 초기화되지 않음"), nil
			}
			var a struct {
				Company string `json:"company"`
				Scope   string `json:"scope"`
				Reason  string `json:"reason"`
				Logo    string `json:"logo"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if strings.TrimSpace(a.Company) == "" {
				return actool.Errorf("회사는 비워둘 수 없습니다"), nil
			}
			companyID, _, err := t.cs.UpsertCompany(a.Company, a.Logo)
			if err != nil {
				return actool.Errorf("회사 생성/가져오기 실패: " + err.Error()), nil
			}
			lines := splitLines(a.Scope)
			added, skipped, invalid, errMsgs := t.cs.AddScope(companyID, lines, a.Reason)
			out := map[string]any{
				"company_id": companyID,
				"added":      added,
				"skipped":    skipped,
				"invalid":    invalid,
			}
			if len(errMsgs) > 0 {
				out["errors"] = errMsgs
			}
			return jsonResult(out)
		},
	)
}

// addTaskScope lets the plan agent add test scope to THE CURRENT TASK — the coverage
// denominator and the task's authorization edge. Worker discoveries are auto-scoped
// (precise host) by insertAssets; this tool is for DELIBERATELY WIDENING: pull a whole
// root domain or whole company into scope, or add a specific subdomain / ip.
func (t *ToolSet) addTaskScope() actool.CoreTool {
	return writeTool(
		"add_task_scope",
		"[이 작업]에 테스트 범위를 추가합니다. 이는 이 작업의 인증 경계이자 자산 테스트 범위의 분모입니다.\n"+
			"kind는 회사(전체 회사 이름의 자산) / root_domain(모든 하위 도메인을 포함한 전체 루트 도메인) / 하위 도메인(정확한 단일 하위 도메인) / ip / cidr / icp / 키워드를 지원합니다.\n"+
			"설명: 작업자가 발견한 호스트는 시스템에 의해 범위(정확한 하위 도메인)에 [자동으로] 추가됩니다. 이 도구는 [활성 확장]에 사용됩니다. 전체 루트 도메인/회사 전체를 포함하거나 특정 하위 도메인/IP를 추가로 지정합니다.\n"+
			"값: 회사는 회사 이름 또는 ID를 전달합니다(회사는 이미 존재해야 함). root_domain/subdomain은 도메인 이름을 전달합니다. ip/cidr은 IP 또는 네트워크 세그먼트를 전달합니다. icp/keyword는 등록 번호 또는 회사 키워드를 전달합니다.\n"+
			"이유에 대한 근거를 반드시 설명하세요(감사 가능). 여러 항목에 대해 항목 배열을 사용하십시오.",
		obj(map[string]any{
			"entries": map[string]any{"type": "array", "description": "일괄: [{종류, 값}]. 종류∈회사/root_domain/하위 도메인/ip/cidr/icp/키워드.", "items": map[string]any{"type": "object"}},
			"kind":    str("[단일 항목] 회사 / root_domain / 하위 도메인 / ip / cidr / icp / 키워드"),
			"value":   str("[단일입력] 회사명 또는 ID / 도메인명 / IP / CIDR / ICP / 키워드"),
			"reason":  str("근거(감사용)를 추가하고 반드시 입력하세요"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("add_task_scope가 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			if t.taskID <= 0 {
				return actool.Errorf("add_task_scope에는 작업 컨텍스트가 필요합니다(현재 작업 없음)"), nil
			}
			type scopeEntry struct {
				Kind  string `json:"kind"`
				Value string `json:"value"`
			}
			var a struct {
				Entries    []scopeEntry `json:"entries"`
				scopeEntry              // 단일 모드
				Reason     string       `json:"reason"`
			}
			_ = json.Unmarshal(in, &a)
			items := a.Entries
			if len(items) == 0 {
				items = []scopeEntry{a.scopeEntry}
			}
			var added []map[string]any
			errs := map[string]string{}
			for i, e := range items {
				ts, err := t.as.AddAgentScope(t.taskID, strings.TrimSpace(e.Kind), e.Value, a.Reason, "agent")
				if err != nil {
					errs[strconv.Itoa(i)] = err.Error()
					continue
				}
				added = append(added, map[string]any{"kind": ts.Kind, "domain": ts.Domain, "net": ts.Net, "value": ts.Value, "company_id": ts.CompanyID})
			}
			out := map[string]any{"added": added}
			if len(errs) > 0 {
				out["errors"] = errs
			}
			return jsonResult(out)
		},
	)
}

// listUntestedAssets lets the plan agent pull the current + directly inherited
// scope's not-yet-tested assets on demand (filter by type, paginated).
func (t *ToolSet) listUntestedAssets() actool.CoreTool {
	return readTool(
		"list_untested_assets",
		"팩트 앵커에 포함되지 않은 [이 작업 및 직접 관련된 작업] 범위 내의 자산을 쿼리합니다(관련 범위는 읽기 전용이므로 보충 테스트 여부를 판단할 수 있으며 사용자를 대신하여 결정을 내리지 않습니다).\n"+
			"자산 유형별 선택적 필터링: root_domain/subdomain/service/app/endpoint/ip.\n"+
			"페이징: 페이지는 1부터 시작하고, page_size의 기본값은 10입니다. {assets:[{id,type,label}], total, page, page_size}를 반환합니다. 작업 컨텍스트만 사용할 수 있습니다.",
		obj(map[string]any{
			"type":      str("자산 유형 필터링(선택 사항): root_domain/subdomain/service/app/endpoint/ip"),
			"page":      intp("1부터 시작하는 페이지 번호(기본값 1)"),
			"page_size": intp("페이지당 개수(기본값 10)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_untested_assets이 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			if t.taskID <= 0 || t.ts == nil {
				return actool.Errorf("list_untested_assets에는 작업 컨텍스트가 필요합니다"), nil
			}
			var a struct {
				Type     string `json:"type"`
				Page     int    `json:"page"`
				PageSize int    `json:"page_size"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Page <= 0 {
				a.Page = 1
			}
			if a.PageSize <= 0 {
				a.PageSize = 10
			}
			offset := (a.Page - 1) * a.PageSize
			assets, total, err := t.as.ListUntestedAssetsWithSources(t.taskID, strings.TrimSpace(a.Type), a.PageSize, offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{
				"assets": assets, "total": total, "page": a.Page, "page_size": a.PageSize,
			})
		},
	)
}

// listAssets lets an agent query the asset table.
func (t *ToolSet) listAssets() actool.CoreTool {
	return readTool(
		"list_assets",
		"자산 목록을 DSL로 검색하거나 id/ids로 직접 조회합니다. 이 작업과 직접 연결된 작업의 테스트 범위에 속한 자산만 반환합니다.\n"+
			"DSL: field=value는 부분 일치(ILIKE), field==value는 정확히 일치, field!=value는 제외 조건입니다. 숫자 필드에는 >, >=, <, <=를 사용할 수 있습니다. 필드 없는 단어는 전체 텍스트에서 검색합니다. AND가 OR보다 우선하며 괄호로 묶을 수 있습니다. 자산 유형은 DSL이 아닌 별도 type 매개변수로 지정합니다.\n"+
			"id/ids가 전달되지 않으면 dsl은 비어 있지 않아야 합니다(무조건 전체 쿼리는 허용되지 않음).\n"+
			"사용 가능한 필드: domain(루트/하위/서비스 도메인), root_domain, ip, url, page_title, icp, service_name, app_name, method(예: GET/POST), service_type(http|other), record_type(예: A/CNAME), technology(기술 목록), port/status_code/company_id(숫자).\n"+
			"예: status_code>=400 AND technology=shiro; (port==80 OR port==443) AND technology=nginx",
		obj(map[string]any{
			"dsl":    str(`DSL 쿼리 표현식(구문/필드에 대한 도구 설명 참조) ID/ID가 전달되지 않으면 비어 있지 않아야 합니다.`),
			"type":   str("자산 유형 필터링: root_domain|ip|subdomain|app|service|endpoint(독립 필드, dsl과 겹쳐질 수 있음, 유형만으로는 쿼리하기에 충분하지 않으며 dsl이 여전히 필요함)"),
			"id":     intp("단일 asset_ids로 직접 가져오기(선택사항, dsl/type과 상호 배타적)"),
			"ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "여러 asset_ids로 직접 가져오기(선택사항, dsl/type과 상호 배타적)"},
			"limit":  intp("상한값 반환, 기본값 10(선택사항)"),
			"offset": intp("페이지 오프셋, 기본값 0(선택 사항)"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.as == nil {
				return actool.Errorf("list_assets이 활성화되지 않음: AssetStore가 초기화되지 않음"), nil
			}
			var a struct {
				DSL    string  `json:"dsl"`
				Type   string  `json:"type"`
				ID     int64   `json:"id"`
				IDs    []int64 `json:"ids"`
				Limit  int     `json:"limit"`
				Offset int     `json:"offset"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Limit <= 0 {
				a.Limit = 10
			}

			var assets []*db.Asset
			var err error
			switch {
			case a.ID > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, []int64{a.ID})
			case len(a.IDs) > 0:
				assets, err = t.as.GetByIDsInScope(t.taskID, a.IDs)
			case a.DSL != "":
				assets, err = t.as.QueryDSLInScope(a.DSL, a.Type, t.taskID, a.Limit, a.Offset)
			default:
				return actool.Errorf("ID/ID가 전달되지 않으면 dsl을 비워둘 수 없습니다. 모든 자산에 대한 무조건 쿼리는 허용되지 않습니다. 쿼리 조건을 제공하세요."), nil
			}
			if err != nil {
				return actool.Errorf("DSL 오류: " + err.Error()), nil
			}
			return jsonResult(map[string]any{
				"count":  len(assets),
				"assets": assets,
			})
		},
	)
}

// listCompanies를 사용하면 에이전트가 범위 + 자산 수를 사용하여 회사(기업)를 열거할 수 있습니다.
func (t *ToolSet) listCompanies() actool.CoreTool {
	return readTool(
		"list_companies",
		"자산 라이브러리의 [기업/회사], 해당 자산 범위(scope) 및 귀속된 자산 수를 나열합니다. 어떤 회사가 있는지 확인하는 데 사용됩니다"+
			"회사 ID를 가져옵니다(insert_assets가 앱과 연결되어 있고 list_assets가 company_id로 필터링될 때 사용됨)."+
			"선택적 검색 회사 이름으로 퍼지 필터를 적용합니다(대소문자 구분 안 함). 모두 검색하려면 비워 두세요.",
		obj(map[string]any{
			"search": str("회사 이름으로 퍼지 필터(선택 사항, 대소문자 구분 안 함); 모두 반환하려면 공백으로 남겨두세요"),
		}),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			if t.cs == nil {
				return actool.Errorf("list_companies가 활성화되지 않음: CompanyStore가 초기화되지 않음"), nil
			}
			var a struct {
				Search string `json:"search"`
			}
			_ = json.Unmarshal(in, &a)
			cos, err := t.cs.ListCompanies()
			if err != nil {
				return actool.Errorf("회사 쿼리 실패: " + err.Error()), nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Search))
			type companyOut struct {
				ID         int64    `json:"id"`
				Name       string   `json:"name"`
				AssetCount int      `json:"asset_count"`
				Scope      []string `json:"scope"`
			}
			out := make([]companyOut, 0, len(cos))
			for _, c := range cos {
				if q != "" && !strings.Contains(strings.ToLower(c.Name), q) {
					continue
				}
				scope := make([]string, 0, len(c.Scope))
				for _, r := range c.Scope {
					scope = append(scope, r.Raw)
				}
				out = append(out, companyOut{ID: c.ID, Name: c.Name, AssetCount: c.AssetCount, Scope: scope})
			}
			return jsonResult(map[string]any{"count": len(out), "companies": out})
		},
	)
}

// splitLines splits a multi-line string into non-empty trimmed lines.
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// WorkerTools returns the tool set for a work agent.
func (t *ToolSet) WorkerTools() []actool.CoreTool {
	return []actool.CoreTool{
		// list_findings Reserved: 동일한 취약점이 반복적으로 보고되는 것을 방지하려면 취약점을 보고하기 전에 이 작업에서 확인된 취약점을 확인하세요.
		t.listFindings(),
		t.addFinding(), t.recordFact(),
		// asset management (handlers guard nil store internally)。
		// add_company_scope는 작업자에게 부여되지 않습니다. 기업 자산의 범위를 정의하는 것은 Planning/Master/Auto의 책임이며 작업자는 탐색만 수행합니다.
		t.insertAssets(), t.listAssets(),
		// 교차 작업 검토: 작업자는 작업 중복을 피하기 위해 다른 작업자의 관찰을 재사용할 수도 있습니다.
		// search_all_worker_traces: 먼저 Intent_id를 알 필요가 없습니다. 키워드로 전역적으로 적중 단계를 검색합니다.
		// get_worker_trace: 특정 작업의 후속 단계를 잠그고/검색하고/전체 콘텐츠를 가져옵니다.
		t.searchAllWorkerTraces(), t.getWorkerTrace(),
		// node_detail: 작업자가 Intent_id/노드 ID를 얻은 후 노드의 전체 세부정보를 확인할 수 있습니다(위 검토에 협조).
		t.nodeDetail(),
		// 다음 도구는 아직 작업자에게 제공되지 않고 플래너/메인에게만 남아 있습니다(컨텍스트 읽기 및 작업 간 검토는 계획 책임입니다.)
		// 작업자는 단일 의도(list_facts / list_companies / list_worker_traces)의 실행 및 쓰기 저장만 수행합니다.
	}
}

// MainAgentTools returns the human-interface tool set.
func (t *ToolSet) MainAgentTools() []actool.CoreTool {
	return []actool.CoreTool{
		t.graphOverview(), t.listFindings(), t.listFacts(), t.nodeDetail(),
		t.expandDigest(), // cold-digest §6.1
		t.getWorkerOutput(), t.getWorkerTrace(), t.searchAllWorkerTraces(), t.addHint(), t.addIntent(),
		// steer_work: 사람들은 진행 상황을 중단하거나 잃지 않고 실시간으로 실행 의도(작업)에 수정 지침을 주입할 수 있습니다.
		t.steerWorkTool(),
		// set_goals: 사람들은 런타임에 이 작업에 새로운 최종 목표를 추가할 수 있습니다(플래너는 이를 기반으로 달성 여부를 다시 판단합니다).
		t.setGoals(),
		// set_constraints: 사람들은 런타임에 이 작업에 대한 운영 제약(허용/거부)을 추가/변경하여 플래너/작업자의 탐색 경계를 제한할 수 있습니다.
		t.setConstraints(),
		// asset management (handlers guard nil store internally)
		t.insertAssets(), t.addCompanyScope(), t.listAssets(),
		t.addFinding(), t.recordFact(),
		t.addTaskScope(),
		// list_untested_assets: 요청 시 이 작업 범위 내에서 테스트되지 않은 자산(유형 + 페이징)을 확인하고 재량에 따라 추가 테스트를 수행합니다.
		t.listUntestedAssets(),
	}
}

// AllDomainTools returns the union of all domain tools across all agent types,
// deduped by name (mainagent order wins). Used by the server to build a registry
// for injecting domain tools into agents (Auto, custom) that don't own a per-task
// ToolSet. The caller provides real stores; tools are callable at taskID=0 scope.
func (t *ToolSet) AllDomainTools() []actool.CoreTool {
	seen := map[string]bool{}
	var out []actool.CoreTool
	all := append(append(t.MainAgentTools(), t.PlannerTools()...), t.WorkerTools()...)
	for _, tool := range all {
		if !seen[tool.Name()] {
			seen[tool.Name()] = true
			out = append(out, tool)
		}
	}
	return out
}

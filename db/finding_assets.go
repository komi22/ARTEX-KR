package db

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 자산 트리 찾기 — 글로벌 결과 목록의 "자산별" 보기입니다.
//
// 계층 구조는 BuildCoverageGraph와 동일한 출처를 갖습니다(회사 → root_domain/ip/app → 하위 도메인 →
// 서비스 → 엔드포인트), 그러나 이는 "작업 범위 내의 자산"에 대한 강제 지향 다이어그램입니다.
// "전체 데이터베이스에서 발견된 자산"의 트리: 발견된 자산과 해당 상위 체인만 수집되며, 노드는 하위 트리로 집계되어 계산됩니다.
// 두 가지의 상위-하위 우선순위 규칙은 일관되어야 합니다. 한 곳을 바꾸신다면 task_scope.go를 참고하셔서 다른 곳도 바꾸시기 바랍니다.
// ---------------------------------------------------------------------------

// FindingUnsignedAsset은 "연결되지 않은 자산"의 노드 키이자 목록 인터페이스의 필터링 센티널이기도 합니다.
// 조회된 자산 ID가 비어 있거나 지정된 자산이 삭제되었습니다.
const FindingUnassignedAsset = "__none__"

// FindingAssetTreeMaxNodes는 프런트 엔드에 반환되는 노드의 상한입니다. 초과 시 아래에서 위로 전체 레이어를 폐기합니다.
// (엔드포인트 1차, 서비스 2차): 해당 개수는 상위 노드에 누적되며, 해당 노드가 손실되더라도 해당 개수는 손실되지 않습니다.
const findingAssetTreeMaxNodes = 3000

// FindingAssetNode는 자산 트리의 노드입니다. 키는 오버레이와 동형입니다. 자산 행은 "a:<id>"입니다.
// 엔터프라이즈는 "c:<id>"이고, 자산 행이 없는 루트 도메인 이름은 합성 "r:<domain>"이며, 연결되지 않은 버킷은 "__none__"입니다.
type FindingAssetNode struct {
	Key       string `json:"key"`
	Parent    string `json:"parent,omitempty"`
	Kind      string `json:"kind"` // company|root_domain|subdomain|ip|service|app|endpoint|none
	Label     string `json:"label"`
	AssetID   int64  `json:"asset_id,omitempty"`
	CompanyID int64  `json:"company_id,omitempty"`
	// Self는 자산과 직접 연결된 발견의 수입니다. 총계에는 모든 하위 항목이 포함되며 결과를 통해 중복이 제거됩니다.
	// (발견이 여러 자산에 연결된 경우 공통 조상은 한 번만 계산됩니다.)
	Self        int       `json:"self"`
	Total       int       `json:"total"`
	Critical    int       `json:"critical"`
	High        int       `json:"high"`
	Medium      int       `json:"medium"`
	Low         int       `json:"low"`
	LastFoundAt time.Time `json:"last_found_at"`
}

// FindingAssetTree는 전체 트리의 일회성 스냅샷입니다. 노드는 동일한 상위 노드 아래에서 발견된 수에 따라 정렬됩니다.
// 내림차순, 라벨 오름차순, "연결되지 않은 자산"은 항상 끝에 있습니다.
type FindingAssetTree struct {
	Nodes        []FindingAssetNode `json:"nodes"`
	FindingTotal int                `json:"finding_total"`
	// Truncated=true는 DroppedKinds의 수준이 제어 볼륨에 대해 삭제됨을 의미합니다.
	Truncated    bool     `json:"truncated"`
	DroppedKinds []string `json:"dropped_kinds,omitempty"`
}

// AssetRow는 트리 구조에 필요한 자산 필드의 하위 집합입니다.
type assetRow struct {
	id          int64
	kind        string
	companyID   int64
	domain      string
	rootDomain  string
	ip          string
	url         string
	port        int
	serviceType string
	appName     string
}

func (a *assetRow) coverageNode() CoverageGraphNode {
	return CoverageGraphNode{
		Kind: a.kind, Domain: a.domain, RootDomain: a.rootDomain, IP: a.ip,
		URL: a.url, Port: a.port, ServiceType: a.serviceType, AppName: a.appName,
	}
}

// label은 오버레이의 레이블 규칙(URL > domain > ip > app_name > root_domain)을 재사용하지만 그렇지 않습니다.
// URL 서비스(SMB, 비HTTP 포트 등)는 포트를 추가해야 합니다. 그렇지 않으면 해당 레이블이 호스트 IP/도메인 이름과 동일해집니다.
// 그들은 똑같습니다. 나무 위의 두 줄의 아버지와 아들은 똑같아 보입니다.
func (a *assetRow) label() string {
	if a.kind == "service" && a.url == "" {
		if host, port := a.hostPort(); host != "" && port > 0 {
			return host + ":" + strconv.Itoa(port)
		}
	}
	n := a.coverageNode()
	n.Key = assetKey(a.id)
	return coverageNodeLabel(&n)
}

// HostPort는 오버레이와 일치합니다. 먼저 도메인, 그 다음 URL의 호스트, 마지막으로 IP입니다.
func (a *assetRow) hostPort() (string, int) {
	n := a.coverageNode()
	return hostPortOf(&n)
}

const findingAssetSelectCols = `a.id, a.type, COALESCE(a.company_id,0),
       COALESCE(a.domain,''), COALESCE(a.root_domain,''), COALESCE(a.ip,''),
       COALESCE(a.url,''), COALESCE(a.port,0), COALESCE(a.service_type,''),
       COALESCE(a.app_name,'')`

func scanAssetRows(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}) ([]*assetRow, error) {
	defer rows.Close()
	var out []*assetRow
	for rows.Next() {
		a := &assetRow{}
		if err := rows.Scan(&a.id, &a.kind, &a.companyID, &a.domain, &a.rootDomain,
			&a.ip, &a.url, &a.port, &a.serviceType, &a.appName); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FindingAssetHit은 트리 구축 단계에서 검색에 필요한 최소한의 정보입니다.
type findingAssetHit struct {
	severity string
	ts       time.Time
	assetIDs []int64
}

// BuildFindingAssetTree 현재 필터를 기반으로 자산 트리를 구축합니다. AssetScope 자체는 참여하지 않습니다. 그렇지 않으면 트리가
// 선택한 노드가 체인으로 축소됩니다.
func (d *DB) BuildFindingAssetTree(f FindingFilter) (*FindingAssetTree, error) {
	return d.buildFindingAssetTree(f, findingAssetTreeMaxNodes)
}

// buildFindingAssetTree는 노드 캡을 사용한 내부 구현입니다. maxNodes<=0은 잘림이 없음을 의미합니다 - 분석
// AssetScope를 사용할 때 이 모드를 사용해야 합니다. 그렇지 않으면 폐기된 끝점으로 인해 하위 트리 ID 세트가 불완전해집니다.
func (d *DB) buildFindingAssetTree(f FindingFilter, maxNodes int) (*FindingAssetTree, error) {
	f.AssetScope = ""
	f.assetIDs, f.assetNone, f.assetMiss = nil, false, false
	where, args := f.where()

	rows, err := d.Query(`SELECT COALESCE(f.severity,''), f.created_at,
       COALESCE(f.asset_ids::text,'[]')
FROM findings f LEFT JOIN tasks t ON f.task_id = t.id`+where, args...)
	if err != nil {
		return nil, err
	}
	hits := []findingAssetHit{}
	assetIDs := map[int64]bool{}
	for rows.Next() {
		var h findingAssetHit
		var aidsJSON string
		if err := rows.Scan(&h.severity, &h.ts, &aidsJSON); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal([]byte(aidsJSON), &h.assetIDs)
		for _, id := range h.assetIDs {
			if id > 0 {
				assetIDs[id] = true
			}
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	tree := &FindingAssetTree{Nodes: []FindingAssetNode{}, FindingTotal: len(hits)}
	byID, err := d.loadFindingAssetRows(assetIDs)
	if err != nil {
		return nil, err
	}

	nodes, parentOf := d.assembleFindingAssetNodes(byID)
	if err := d.attachCompanyNodes(nodes, parentOf); err != nil {
		return nil, err
	}

	// 계산: 검색은 각 자산의 상위 체인으로 올라가서 중복 제거된 키 세트를 수집한 다음 하나씩 +1합니다.
	// 따라서 하나의 검색에 여러 하위 자산이 있으므로 상위 노드는 반복적으로 계산되지 않습니다.
	unassigned := &FindingAssetNode{Key: FindingUnassignedAsset, Kind: "none", Label: "연결되지 않은 자산"}
	touched := map[string]bool{}
	for _, h := range hits {
		clear(touched)
		var direct []*FindingAssetNode
		for _, id := range h.assetIDs {
			node := nodes[assetKey(id)]
			if node == nil {
				continue
			}
			direct = append(direct, node)
			for key := node.Key; key != ""; key = parentOf[key] {
				touched[key] = true
			}
		}
		if len(direct) == 0 {
			countFinding(unassigned, h)
			unassigned.Self++
			continue
		}
		for _, node := range direct {
			node.Self++
		}
		for key := range touched {
			countFinding(nodes[key], h)
		}
	}

	for _, node := range nodes {
		if node.Total > 0 {
			tree.Nodes = append(tree.Nodes, *node)
		}
	}
	if unassigned.Total > 0 {
		tree.Nodes = append(tree.Nodes, *unassigned)
	}
	sortFindingAssetNodes(tree.Nodes)
	truncateFindingAssetTree(tree, maxNodes)
	return tree, nil
}

// countFinding은 결과를 노드에 누적합니다(총 수/심각도 버킷/최근 검색 시간).
func countFinding(n *FindingAssetNode, h findingAssetHit) {
	if n == nil {
		return
	}
	n.Total++
	switch h.severity {
	case "critical":
		n.Critical++
	case "high":
		n.High++
	case "medium":
		n.Medium++
	case "low":
		n.Low++
	}
	if h.ts.After(n.LastFoundAt) {
		n.LastFoundAt = h.ts
	}
}

// loadFindingAssetRows는 적중 자산 행을 읽고 상위 항목(서비스의 호스트 도메인 이름/IP,
// 하위 도메인 이름의 루트 도메인 이름). 조상 자체는 아무것도 발견하지 못할 수도 있지만, 나무가 모양을 갖추려면 조상이 필요합니다.
func (d *DB) loadFindingAssetRows(ids map[int64]bool) (map[int64]*assetRow, error) {
	byID := map[int64]*assetRow{}
	if len(ids) == 0 {
		return byID, nil
	}
	idList := make([]int64, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	rows, err := d.Query(`SELECT `+findingAssetSelectCols+` FROM assets a WHERE a.id = ANY($1::bigint[])`, idList)
	if err != nil {
		return nil, err
	}
	found, err := scanAssetRows(rows)
	if err != nil {
		return nil, err
	}
	for _, a := range found {
		byID[a.id] = a
	}

	// 각 라운드에서 누락된 상위 노드의 호스트 ID가 발견되고 하나의 레이어가 일괄적으로 추가됩니다. 레이어 수는 고정되어 있습니다(엔드포인트→서비스→
	// subdomain/ip→root_domain), 4라운드이면 수렴에 충분합니다.
	for range 4 {
		want := missingParents(byID)
		if want.empty() {
			break
		}
		added, err := d.loadAssetsByHost(want, byID)
		if err != nil {
			return nil, err
		}
		if added == 0 {
			break
		}
	}
	return byID, nil
}

// missingHosts는 완료 라운드 동안 라이브러리에서 발견되는 호스트 ID이며 대상 자산 유형으로 구분됩니다.
type missingHosts struct {
	services []string // 엔드포인트의 호스트(서비스 라인 찾기)
	domains  []string // 서비스/엔드포인트의 호스트 도메인 이름(하위 도메인 줄 찾기)
	ips      []string // 서비스/엔드포인트의 호스트 IP(IP 라인 찾기)
	roots    []string // 하위 도메인의 루트 도메인 이름(root_domain 줄 찾기)
}

func (m missingHosts) empty() bool {
	return len(m.services) == 0 && len(m.domains) == 0 && len(m.ips) == 0 && len(m.roots) == 0
}

// missingParents는 로드되지 않은 호스트를 요약합니다: 서비스(엔드포인트 소속용), 하위 도메인 이름/IP(용
// 서비스 및 엔드포인트) 및 루트 도메인 이름(연결할 하위 도메인 이름용)입니다.
func missingParents(byID map[int64]*assetRow) missingHosts {
	haveService := map[string]bool{}
	haveDomain := map[string]bool{}
	haveIP := map[string]bool{}
	haveRoot := map[string]bool{}
	for _, a := range byID {
		switch a.kind {
		case "service":
			if host, _ := a.hostPort(); host != "" {
				haveService[host] = true
			}
		case "subdomain":
			haveDomain[a.domain] = true
		case "ip":
			haveIP[a.ip] = true
		case "root_domain":
			haveRoot[a.domain] = true
		}
	}
	wantService := map[string]bool{}
	wantDomain := map[string]bool{}
	wantIP := map[string]bool{}
	wantRoot := map[string]bool{}
	for _, a := range byID {
		switch a.kind {
		case "service", "endpoint":
			host, _ := a.hostPort()
			// 엔드포인트는 먼저 동일한 호스트의 서비스를 찾습니다. 포트가 일치하지 않는 서비스는 Total=0이 됩니다.
			// 결국 필터링되어 나무를 오염시키지 않습니다.
			if a.kind == "endpoint" && host != "" && !haveService[host] {
				wantService[host] = true
			}
			if host != "" && !haveDomain[host] && !haveIP[host] && !haveRoot[host] {
				if isIPLiteral(host) {
					wantIP[host] = true
				} else {
					wantDomain[host] = true
				}
			}
			if a.ip != "" && !haveIP[a.ip] {
				wantIP[a.ip] = true
			}
		case "subdomain":
			if a.rootDomain != "" && !haveRoot[a.rootDomain] {
				wantRoot[a.rootDomain] = true
			}
		}
	}
	return missingHosts{
		services: keysOf(wantService),
		domains:  keysOf(wantDomain),
		ips:      keysOf(wantIP),
		roots:    keysOf(wantRoot),
	}
}

func keysOf(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// isIPLiteral은 호스트가 IP 리터럴인지 여부를 대략적으로 결정합니다(ip 또는 하위 도메인 테이블에서 호스트를 찾을지 여부를 결정하는 데 사용됨).
func isIPLiteral(host string) bool {
	if strings.Contains(host, ":") {
		return true // IPv6
	}
	if host == "" {
		return false
	}
	for _, part := range strings.Split(host, ".") {
		if part == "" {
			return false
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return strings.Count(host, ".") == 3
}

// loadAssetsByHost는 호스트 ID에 따라 일괄적으로 자산 행을 완성하고 이번 라운드의 새 행 수를 반환합니다.
func (d *DB) loadAssetsByHost(want missingHosts, byID map[int64]*assetRow) (int, error) {
	added := 0
	load := func(q string, arg []string) error {
		if len(arg) == 0 {
			return nil
		}
		rows, err := d.Query(q, arg)
		if err != nil {
			return err
		}
		found, err := scanAssetRows(rows)
		if err != nil {
			return err
		}
		for _, a := range found {
			if _, ok := byID[a.id]; ok {
				continue
			}
			byID[a.id] = a
			added++
		}
		return nil
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='service' AND (a.domain = ANY($1::text[]) OR a.ip = ANY($1::text[]))`, want.services); err != nil {
		return 0, err
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='subdomain' AND a.domain = ANY($1::text[])`, want.domains); err != nil {
		return 0, err
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='ip' AND a.ip = ANY($1::text[])`, want.ips); err != nil {
		return 0, err
	}
	if err := load(`SELECT `+findingAssetSelectCols+` FROM assets a
WHERE a.type='root_domain' AND a.domain = ANY($1::text[])`, want.roots); err != nil {
		return 0, err
	}
	return added, nil
}

// assembleFindingAssetNodes는 자산 행을 노드로 변환하고 상위-하위 관계를 연결합니다. 부모 노드가 없는 경우(Kuri
// 루트 도메인 이름 자산이 전혀 없음) 및 오버레이 맵 처리와 일치하는 "r:<domain>" 자리 표시자 노드가 합성됩니다.
func (d *DB) assembleFindingAssetNodes(byID map[int64]*assetRow) (map[string]*FindingAssetNode, map[string]string) {
	nodes := map[string]*FindingAssetNode{}
	parentOf := map[string]string{}
	rootByDomain := map[string]string{}
	subByDomain := map[string]string{}
	ipByAddr := map[string]string{}
	svcByHost := map[string]string{}
	svcByHostPort := map[string]string{}

	for _, a := range byID {
		key := assetKey(a.id)
		nodes[key] = &FindingAssetNode{
			Key: key, Kind: a.kind, Label: a.label(),
			AssetID: a.id, CompanyID: a.companyID,
		}
		switch a.kind {
		case "root_domain":
			if a.domain != "" {
				rootByDomain[a.domain] = key
			}
		case "subdomain":
			if a.domain != "" {
				subByDomain[a.domain] = key
			}
		case "ip":
			if a.ip != "" {
				ipByAddr[a.ip] = key
			}
		case "service":
			if host, port := a.hostPort(); host != "" {
				svcByHost[host] = key
				svcByHostPort[host+"|"+strconv.Itoa(port)] = key
			}
		}
	}

	// 라이브러리에 자산 행이 없는 경우 하위 도메인 이름의 루트 도메인 이름이 자리 표시자 루트로 합성되어 하위 도메인 이름이 최상위 수준으로 흩어지는 것을 방지합니다.
	for _, a := range byID {
		if a.kind != "subdomain" || a.rootDomain == "" {
			continue
		}
		if _, ok := rootByDomain[a.rootDomain]; ok {
			continue
		}
		key := "r:" + a.rootDomain
		nodes[key] = &FindingAssetNode{Key: key, Kind: "root_domain", Label: a.rootDomain}
		rootByDomain[a.rootDomain] = key
	}

	firstOf := func(keys ...string) string {
		for _, k := range keys {
			if k != "" {
				if _, ok := nodes[k]; ok {
					return k
				}
			}
		}
		return ""
	}
	for _, a := range byID {
		key := assetKey(a.id)
		var parent string
		switch a.kind {
		case "subdomain":
			parent = firstOf(rootByDomain[a.rootDomain])
		case "service":
			host, _ := a.hostPort()
			parent = firstOf(subByDomain[a.domain], subByDomain[host],
				ipByAddr[a.ip], ipByAddr[host], rootByDomain[a.rootDomain], rootByDomain[host])
		case "endpoint":
			host, port := a.hostPort()
			parent = firstOf(svcByHostPort[host+"|"+strconv.Itoa(port)], svcByHost[host],
				subByDomain[host], subByDomain[a.domain], ipByAddr[host], ipByAddr[a.ip],
				rootByDomain[a.rootDomain], rootByDomain[host])
		}
		if parent != "" && parent != key {
			parentOf[key] = parent
			nodes[key].Parent = parent
		}
	}
	return nodes, parentOf
}

// attachmentCompanyNodes는 최상위 자산(루트 도메인 이름/IP/애플리케이션)에 엔터프라이즈 상위 노드를 추가합니다. 자산만
// 엔터프라이즈 레이어는 엔터프라이즈에 귀속된 경우에만 나타납니다. 귀속되지 않은 자산은 여전히 ​​최상위 계층입니다.
func (d *DB) attachCompanyNodes(nodes map[string]*FindingAssetNode, parentOf map[string]string) error {
	want := map[int64]bool{}
	for _, n := range nodes {
		if n.Parent != "" || n.CompanyID <= 0 {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			want[n.CompanyID] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(want))
	for id := range want {
		ids = append(ids, id)
	}
	rows, err := d.Query(`SELECT id, COALESCE(name,'') FROM companies WHERE id = ANY($1::bigint[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return err
		}
		names[id] = name
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for id, name := range names {
		key := companyKey(id)
		if _, ok := nodes[key]; ok {
			continue
		}
		if name == "" {
			name = "업체 번호" + strconv.FormatInt(id, 10)
		}
		nodes[key] = &FindingAssetNode{Key: key, Kind: "company", Label: name, CompanyID: id}
	}
	for _, n := range nodes {
		if n.Parent != "" || n.CompanyID <= 0 || n.Kind == "company" {
			continue
		}
		switch n.Kind {
		case "root_domain", "ip", "app":
			key := companyKey(n.CompanyID)
			if _, ok := nodes[key]; !ok {
				continue
			}
			n.Parent = key
			parentOf[n.Key] = key
		}
	}
	return nil
}

// sortFindingAssetNodes 정렬: 먼저 찾은 것, 레이블별로 동일한 번호; "연결되지 않은 자산"은 항상 끝에 있습니다.
// 프런트 엔드는 동일한 상위 노드 아래의 상대 순서가 올바른 한 배열 순서로 하위 노드를 정지합니다.
func sortFindingAssetNodes(nodes []FindingAssetNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if (a.Kind == "none") != (b.Kind == "none") {
			return b.Kind == "none"
		}
		if a.Total != b.Total {
			return a.Total > b.Total
		}
		return a.Label < b.Label
	})
}

// truncateFindingAssetTree는 노드(첫 번째 엔드포인트, 그 다음 서비스)가 너무 많으면 전체 레이어를 삭제합니다. 카운트는
// 상위 노드에 누적되면 확장 가능한 세부 정보 수준만 손실됩니다.
func truncateFindingAssetTree(tree *FindingAssetTree, maxNodes int) {
	if maxNodes <= 0 || len(tree.Nodes) <= maxNodes {
		return
	}
	for _, kind := range []string{"endpoint", "service"} {
		kept := tree.Nodes[:0]
		for _, n := range tree.Nodes {
			if n.Kind == kind {
				continue
			}
			kept = append(kept, n)
		}
		tree.Nodes = kept
		tree.Truncated = true
		tree.DroppedKinds = append(tree.DroppedKinds, kind)
		if len(tree.Nodes) <= maxNodes {
			return
		}
	}
}

// applyAssetScope는 AssetScope(노드 키)를 SQL에서 사용할 수 있는 자산 ID 컬렉션으로 구문 분석합니다. 선택된
// 노드는 전체 하위 트리를 선택하는 것과 동일하므로 먼저 트리를 구축한 다음 해당 하위 항목을 수집해야 합니다.
func (d *DB) applyAssetScope(f FindingFilter) (FindingFilter, error) {
	scope := strings.TrimSpace(f.AssetScope)
	f.assetIDs, f.assetNone, f.assetMiss = nil, false, false
	if scope == "" {
		return f, nil
	}
	if scope == FindingUnassignedAsset {
		f.assetNone = true
		return f, nil
	}
	// 잘림 없음: 삭제된 엔드포인트도 ID 수집에 참여해야 합니다. 그렇지 않으면 목록에 포함되는 데이터가 줄어듭니다.
	tree, err := d.buildFindingAssetTree(f, 0)
	if err != nil {
		return f, err
	}
	children := map[string][]FindingAssetNode{}
	byKey := map[string]FindingAssetNode{}
	for _, n := range tree.Nodes {
		byKey[n.Key] = n
		children[n.Parent] = append(children[n.Parent], n)
	}
	if _, ok := byKey[scope]; !ok {
		// 선택한 노드는 더 이상 현재 필터링에 존재하지 않으며 결과는 필터링 없음으로 변질되는 대신 비어 있어야 합니다.
		f.assetMiss = true
		return f, nil
	}
	seen := map[string]bool{scope: true}
	queue := []string{scope}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		if id := byKey[key].AssetID; id > 0 {
			f.assetIDs = append(f.assetIDs, id)
		}
		for _, child := range children[key] {
			if seen[child.Key] {
				continue
			}
			seen[child.Key] = true
			queue = append(queue, child.Key)
		}
	}
	if len(f.assetIDs) == 0 {
		f.assetMiss = true
	}
	return f, nil
}

// AssetIDContainments는 자산 ID를 판단을 포함하는 jsonb 오른쪽 피연산자 집합으로 변환하고 다음과 협력합니다.
// idx_findings_asset_ids(GIN jsonb_path_ops)가 사용됩니다.
func assetIDContainments(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, "["+strconv.FormatInt(id, 10)+"]")
	}
	return out
}

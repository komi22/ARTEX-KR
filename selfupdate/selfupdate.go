// 패키지 자체 업데이트는 한 번의 클릭으로 ARTEX 페이지가 업데이트되도록 구현합니다. GitHub 릴리스에서 새 버전을 가져옵니다.
// 다음 시작 시 바이너리, 검증, 임시 저장 및 원자 재로드.
//
// 전반적인 업무 분업(start.sh / start.bat 참조):
//
//	시작 스크립트 = 바보의 데몬 루프, "프로세스가 종료된 후 종료 코드를 눌러 다시 시작할지 여부를 결정"만 담당
//	이 패키지 = 오류가 발생하기 쉬운 모든 논리(다운로드/SHA256 확인/연기/교체/실패 롤백)
//
// 스크립트 대신 Go에 드레싱을 넣은 이유는 sha256 체크섬 스모크 테스트가 sh와 bat에 있기 때문입니다.
// 두 개의 세트(sha256sum / shasum / certutil)를 작성해야 하는데, 이 부분이 바로 잘못될 수 없는 부분입니다. 하나를 교체하세요.
// 바이너리를 실행할 수 없는 경우 데몬은 바이너리를 반복해서 충실하게 가져오며 사용자는 머신에 수동으로만 저장할 수 있습니다.
//
// 전체 업그레이드에는 세 가지 프로세스 시작이 필요합니다.
//
//	① 이전 버전의 서버는 /api/update/apply 수신 → 다운로드 및 확인 → artex.new 임시 저장 → 75번 종료
//	② 스크립트는 이전 버전을 다시 실행합니다 → 부트스트랩은 artex.new를 찾습니다 → 검증 + 연기 → 교체 → 75번 종료
//	③ 스크립트가 다시 실행되어 이제 새 버전이 됩니다. → Bootstrap은 시도를 기록합니다. → 성공적인 시작 후 표시를 지웁니다.
//
// 어느 단계든 실패하면 이전 버전으로 돌아가게 됩니다. ② 검증에 실패하면 임시 파일을 삭제하고 이전 버전을 계속 실행하세요. ③ 연속 3회 생존에 실패하는 경우
// 표시를 지우면(일어날 수 없으면 충돌이 발생함) artex.old가 자동으로 교체됩니다.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart는 "데몬을 다시 시작해주세요"의 종료 코드(EX_TEMPFAIL)입니다. 스크립트를 시작하여 확인하세요.
// 붕괴 후퇴를 계산하지 않고 즉시 다시 실행하십시오. 0은 사용자가 정상적으로 중지되었음을 의미하며(스크립트가 루프를 종료함) 나머지는 충돌로 간주됩니다.
const ExitRestart = 75

// maxAttempts는 다시 로드한 후 허용되는 부팅 시도 횟수입니다. 새 버전이 시작될 때마다 개수는 +1이 됩니다. 생존
// SetDelay는 표시를 지웁니다. maxAttempts 횟수가 충돌하면 새 버전을 전혀 시작할 수 없으며 자동으로 롤백됩니다.
const maxAttempts = 3

// 경로는 업그레이드와 관련된 모든 파일로, 실행 파일이 있는 디렉터리에 저장됩니다.
// 의도적으로 CWD를 사용하지 마십시오. 서비스 런타임의 작업 디렉터리는 / 또는 임의의 경로일 수 있습니다. CWD를 사용하면 임시 파일이 다음 위치에 놓이게 됩니다.
// 다른 곳에서는 드레싱 논리가 실패합니다.
type Paths struct {
	Dir     string // 실행파일이 위치한 디렉토리
	Current string // 현재 실행 중인 바이너리 artex/artex.exe
	New     string // 임시 새 버전 artex.new / artex.new.exe
	Sum     string // sha256(16진수) artex.new.sha256 / artex.new.exe.sha256의 새 버전
	Old     string // 교체 전에 이전 버전의 artex.old / artex.old.exe를 백업했습니다.
	Marker  string // 업그레이드 상태 태그 artex.upgrade.json
}

// ResolvePaths는 현재 실행 파일을 기반으로 모든 업그레이드 경로를 추론합니다.
//
// Windows에서는 .new/.old에도 .exe 접미사가 있어야 합니다. 그렇지 않으면 교체 후 스모크 테스트 및 실행이 실패합니다.
// 따라서 먼저 접미사를 제거한 다음 두 플랫폼의 이름이 대칭이 되도록 철자를 입력하세요.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("실행 파일 찾기: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // Windows에서는 ".exe", Unix에서는 일반적으로 비어 있음
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// 마커는 의상 변경 진행 상황을 기록하고 새 버전을 시작할 수 없을 때 자동 롤백을 실행하는 데 사용됩니다.
type marker struct {
	From     string `json:"from"`     //업그레이드 전 버전
	To       string `json:"to"`       // 대상 버전
	Attempts int    `json:"attempts"` // 교체 후 시동 시도 횟수
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged는 준비된 파일을 지웁니다. 설치가 성공했거나, 확인이 실패했거나, 잔여 문제를 피하기 위해 사용자가 취소한 경우에 사용하세요.
// artex.new는 다음 시작 시 다시 시도됩니다.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions 두 버전 번호를 비교하여 -1/0/1(a<b / a==b / a>b)을 반환합니다.
// ok=false는 적어도 한 쪽이 비교 가능한 버전 번호(예: 로컬 개발 빌드의 경우 "dev" 또는
// git 설명의 "0.3.7-2-gabc1234-dirty" 출력), 호출자는 이때 원클릭 업데이트를 비활성화해야 합니다.
// 그렇지 않으면 개발 중인 빌드가 공식 버전으로 "업그레이드"되어 커밋되지 않은 변경 사항을 덮어씁니다.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion은 "v0.3.7" / "0.3.7" 형식의 버전 번호를 [3]int로 구문 분석합니다.
//
// 순수한 세 부분으로 구성된 표현식만 허용됩니다. 태그 없이 빌드할 때 build.sh는 git 설명에 의해 출력됩니다.
// "0.3.7-2-gabc1234"와 같은 접미사가 붙은 버전은 다음과 같이 취급되지 않고 비교할 수 없는 것으로 판단되어야 합니다.
// 0.3.7 - 그렇지 않으면 개발 빌드가 "이미 최신"으로 잘못 판단되거나 공식 버전으로 덮어쓰게 됩니다.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker는 프로세스가 컨테이너에서 실행 중인지 여부를 보고합니다. 컨테이너 쓰기 가능 레이어는 Docker 아래에 작성됩니다.
// `docker compose up -d` 컨테이너를 다시 빌드하면 이미지와 함께 제공되는 버전으로 돌아갑니다. 이는 예상된 동작입니다.
// (그 당시 사용자들은 이미 새로운 이미지를 끌어오고 있었습니다.) 그러나 프런트엔드는 이를 바탕으로 명확하게 말할 수 있어야 합니다.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}

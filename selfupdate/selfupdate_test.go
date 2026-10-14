package selfupdate

import (
	"archive/zip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// testPaths는 격리된 업그레이드 디렉터리를 생성합니다. ResolvePaths()를 직접 사용할 수 없습니다. 이는 테스트를 가리킵니다.
// 바이너리 자체에서는 go test의 실행 파일이 실행되자마자 이름이 변경됩니다.
func testPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		Dir:     dir,
		Current: filepath.Join(dir, "artex"),
		New:     filepath.Join(dir, "artex.new"),
		Sum:     filepath.Join(dir, "artex.new.sha256"),
		Old:     filepath.Join(dir, "artex.old"),
		Marker:  filepath.Join(dir, "artex.upgrade.json"),
	}
}

// fakeBin은 artex를 가장하기 위해 실행 가능한 쉘 스크립트를 작성합니다. smokeTest를 -h로 당겨서 종료 코드를 확인하세요.
// 스크립트는 실제 바이너리를 컴파일하는 것보다 완벽하게 적절하고 훨씬 빠릅니다.
func fakeBin(t *testing.T, path, marker string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\necho " + marker + "\nexit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("가짜 바이너리 작성 %s: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// Stage는 저장소를 "교체를 위해 임시로 저장됨"으로 정렬합니다. artex.new 및 해당 체크섬을 작성합니다.
func stage(t *testing.T, p Paths, marker string, exitCode int) {
	t.Helper()
	fakeBin(t, p.New, marker, exitCode)
	sum, err := fileSHA256(p.New)
	if err != nil {
		t.Fatalf("체크섬 계산: %v", err)
	}
	if err := os.WriteFile(p.Sum, []byte(sum), 0o644); err != nil {
		t.Fatalf("체크섬 쓰기: %v", err)
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("읽다 %s: %v", path, err)
	}
	return string(b)
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("가짜 바이너리 사용 sh 스크립트,Windows 달려갈 수 없어")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b       string
		want       int
		comparable bool
	}{
		{"0.3.7", "0.3.8", -1, true},
		{"0.3.8", "0.3.7", 1, true},
		{"0.3.7", "0.3.7", 0, true},
		{"v0.3.7", "0.3.8", -1, true}, // build.sh에서 v를 제거하고 태그에 v를 추가하세요. 양쪽 모두를 인정해야 합니다.
		{"0.3.7", "v0.3.7", 0, true},
		{"0.9.0", "0.10.0", -1, true}, // 사전순이 아닌 숫자순으로 정렬
		{"1.0.0", "0.99.99", 1, true},
		// 개발 빌드는 비교할 수 없는 것으로 판단되어야 하며, 그렇지 않으면 커밋되지 않은 변경 사항이 공식 버전으로 덮어쓰여집니다.
		{"dev", "0.3.8", 0, false},
		{"0.3.7-2-gabc1234", "0.3.8", 0, false},
		{"0.3.7-dirty", "0.3.8", 0, false},
		{"0.3", "0.3.8", 0, false},
		{"", "0.3.8", 0, false},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.comparable {
			t.Errorf("CompareVersions(%q,%q) comparable=%v, 기대 %v", c.a, c.b, ok, c.comparable)
			continue
		}
		if ok && got != c.want {
			t.Errorf("CompareVersions(%q,%q)=%d, 기대 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolvePathsNaming(t *testing.T) {
	p, err := ResolvePaths()
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	//주요 불변성: 모든 업그레이드 파일은 실행 파일과 동일한 디렉터리에 있습니다. CWD로 전환하면 서비스 지향 운영이 가능해집니다.
	// (작업 디렉토리는 /일 수 있습니다.) 변경이 완전히 실패합니다.
	for name, path := range map[string]string{"New": p.New, "Sum": p.Sum, "Old": p.Old, "Marker": p.Marker} {
		if filepath.Dir(path) != p.Dir {
			t.Errorf("%s 실행 디렉터리에 없음: %s (기대 %s)", name, path, p.Dir)
		}
	}
	// .new/.old는 Windows에서 .exe로 유지되어야 합니다. 그렇지 않으면 스모크 테스트 및 교체 후 실행이 실패합니다.
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(p.New, ".exe") || !strings.HasSuffix(p.Old, ".exe") {
			t.Errorf("Windows 에 .new/.old 반드시 .exe 결말: new=%s old=%s", p.New, p.Old)
		}
	}
}

func TestVerifyStagedRejectsTamperedBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "new", 0)

	// 체크섬이 작성된 후 파일을 수정하여 다운로드 손상/삭제된 파일을 시뮬레이션합니다.
	fakeBin(t, p.New, "tampered", 0)
	if err := verifyStaged(p); err == nil {
		t.Fatal("기대 SHA256 일치하지 않아 거부되었으나 통과됨")
	}
}

func TestVerifyStagedRejectsUnrunnableBinary(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	stage(t, p, "broken", 1) // 실행할 수 있지만 종료 코드가 0이 아닙니다.

	if err := verifyStaged(p); err == nil {
		t.Fatal("스모크 테스트가 실패하고 거부될 것으로 예상되었으나 통과되었습니다.")
	}
}

func TestApplyStagedHappyPath(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8"}); err != nil {
		t.Fatalf("마크를 쓰다: %v", err)
	}

	action, st := applyStaged(p)
	if action != Restart {
		t.Fatalf("기대 Restart, 얻다 %v", action)
	}
	if !st.Pending {
		t.Error("교체 후 상태는 다음과 같아야합니다. Pending")
	}
	if !strings.Contains(readAll(t, p.Current), "new") {
		t.Error("artex 새 버전으로 교체했어야 했는데")
	}
	if !strings.Contains(readAll(t, p.Old), "old") {
		t.Error("이전 버전은 다음 위치에 백업해야 합니다. artex.old")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("옷을 갈아입은 후 artex.new 사라졌어야 했어")
	}
	if _, err := os.Stat(p.Sum); !os.IsNotExist(err) {
		t.Error("체크섬 파일은 교체 후 정리되어야 합니다.")
	}
	// 표시는 유지되어야 하며 다음 시작(새 버전 실행)은 이를 기준으로 계산되고 필요한 경우 롤백됩니다.
	if _, ok := readMarker(p.Marker); !ok {
		t.Error("업그레이드 마크는 장비 변경 후에도 유지되어야 합니다.")
	}
}

func TestApplyStagedKeepsCurrentWhenVerifyFails(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "old", 0)
	stage(t, p, "new", 0)
	fakeBin(t, p.New, "tampered", 0) // 손상된 체크섬

	action, st := applyStaged(p)
	if action != Continue {
		t.Fatalf("유효성 검사에 실패할 경우 예상되는 상황 Continue, 얻다 %v", action)
	}
	if !st.FailedStage {
		t.Error("상태는 다음과 같이 표시되어야 합니다. FailedStage")
	}
	if !strings.Contains(readAll(t, p.Current), "old") {
		t.Fatal("검증에 실패하면 현재 버전을 건드리면 안 됩니다.")
	}
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Error("확인에 실패한 임시 파일은 지워져야 합니다. 그렇지 않으면 다음 시작 시 다시 시도됩니다.")
	}
}

func TestSwapOverwritesPreviousBackup(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0) // 이전 업그레이드 라운드에서 남은 백업
	stage(t, p, "v3", 0)

	if err := swap(p); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if !strings.Contains(readAll(t, p.Current), "v3") {
		t.Error("다음과 같이 변경되어야 합니다. v3")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("백업은 방금 교체된 백업으로 업데이트되어야 합니다. v2")
	}
}

func TestConfirmCountsAttemptsThenRollsBack(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "broken-new", 0)
	fakeBin(t, p.Old, "good-old", 0)
	m := marker{From: "0.3.7", To: "0.3.8"}

	// 첫 번째 maxAttempts 실행은 누적 횟수만 계산되므로 새 버전이 스스로 확립될 수 있는 기회가 제공됩니다.
	for i := 1; i <= maxAttempts; i++ {
		action, st := confirmOrRollback(p, m)
		if action != Continue {
			t.Fatalf("아니요. %d 시도가 예상됨 Continue, 얻다 %v", i, action)
		}
		if !st.Pending {
			t.Errorf("아니요. %d 시도 상태는 다음과 같아야 합니다. Pending", i)
		}
		got, ok := readMarker(p.Marker)
		if !ok || got.Attempts != i {
			t.Fatalf("아니요. %d 시도 후 attempts=%d（ok=%v), 기대 %d", i, got.Attempts, ok, i)
		}
		m = got
	}

	// 다시 충돌이 발생하면 제한을 초과하고 이전 버전이 자동으로 교체됩니다.
	action, st := confirmOrRollback(p, m)
	if action != Restart {
		t.Fatalf("시도 제한 시간 예상을 초과했습니다. Restart, 얻다 %v", action)
	}
	if !st.RolledBack {
		t.Error("상태는 다음과 같이 표시되어야 합니다. RolledBack")
	}
	if !strings.Contains(readAll(t, p.Current), "good-old") {
		t.Fatal("이전 버전으로 롤백했어야 했는데")
	}
	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Error("롤백 후에 표시가 지워져야 합니다. 그렇지 않으면 무한히 롤백됩니다.")
	}
	// 로드할 수 없는 버전은 문제 해결을 위해 예약되어 있으며 직접 삭제되지 않습니다.
	if _, err := os.Stat(p.Current + ".failed"); err != nil {
		t.Error("실패한 버전은 다음과 같이 유지되어야 합니다. .failed 문제 해결을 위해")
	}
}

func TestManualRollbackIsReversible(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "v2", 0)
	fakeBin(t, p.Old, "v1", 0)

	// Rollback()은 기본 교환 의미 체계가 직접 측정되는 ResolvePaths()를 사용합니다.
	tmp := p.Current + ".swap"
	if err := os.Rename(p.Current, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readAll(t, p.Current), "v1") {
		t.Error("롤백 후 현재 버전은 다음과 같아야 합니다. v1")
	}
	if !strings.Contains(readAll(t, p.Old), "v2") {
		t.Error("롤백 후 백업은 다음과 같아야 합니다. v2, 다시 돌아갈 수 있도록")
	}
}

func TestParseSums(t *testing.T) {
	const (
		linuxSum = "1111111111111111111111111111111111111111111111111111111111111111"
		winSum   = "ABCDEF0000000000000000000000000000000000000000000000000000000000"
	)
	//sha256sum의 출력은 이중 공백으로 구분됩니다. shasum -a 256은 바이너리 모드에서 파일 이름에 *를 추가합니다.
	raw := linuxSum + "  artex-0.3.8-linux-amd64.zip\n" +
		winSum + " *artex-0.3.8-windows-amd64.zip\n" +
		"\n" +
		"garbage line\n" + // 정확히 두 개의 필드이지만 첫 번째 필드는 요약이 아닙니다.
		"deadbeef  artex-0.3.8-darwin-arm64.zip\n" // 요약 길이가 잘못되었습니다.

	out := parseSums(raw)
	if out["artex-0.3.8-linux-amd64.zip"] != linuxSum {
		t.Errorf("linux 항목 구문 분석 오류: %v", out)
	}
	// 초록은 비교 시 대소문자로 인한 불일치로 오인되지 않도록 소문자로 작성해야 합니다.
	if got := out["artex-0.3.8-windows-amd64.zip"]; got != strings.ToLower(winSum) {
		t.Errorf("windows 입력 오류(* 접두사는 제거되어야 하며 초록은 소문자로 변환되어야 합니다): %q", got)
	}
	if len(out) != 2 {
		t.Errorf("빈 줄, 요약이 아닌 줄, 잘못된 길이의 줄은 무시되어야 합니다. %v", out)
	}
}

func TestExtractBinaryFindsNestedEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("패키지의 기본 이름은 다음과 같습니다. Windows 예 artex.exe, 이 사용 사례는 프레스 Unix 명명된 구문")
	}
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")

	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	// 실제 릴리스 패키지의 구조: artex-<version>-<os>-<arch>/artex 및 일부 간섭 파일.
	for name, body := range map[string]string{
		"artex-0.3.8-linux-amd64/README.md":           "readme",
		"artex-0.3.8-linux-amd64/skills/a.md":         "skill",
		"artex-0.3.8-linux-amd64/artex":               "#!/bin/sh\nexit 0\n",
		"artex-0.3.8-linux-amd64/config.example.json": "{}",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "out")
	if err := extractBinary(zipPath, dst); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if got := readAll(t, dst); !strings.Contains(got, "exit 0") {
		t.Errorf("압축이 풀린 것은 아닙니다. artex 실행 파일: %q", got)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("압축이 풀린 바이너리에는 실행 비트가 있어야 합니다.")
	}
}

func TestExtractBinaryMissingEntry(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "release.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("artex-0.3.8-linux-amd64/README.md")
	_, _ = w.Write([]byte("readme"))
	_ = zw.Close()
	f.Close()

	if err := extractBinary(zipPath, filepath.Join(dir, "out")); err == nil {
		t.Fatal("패키지에 실행 파일이 없으면 오류가 보고되어야 합니다.")
	}
}

func TestCheckURLRejectsNonGitHub(t *testing.T) {
	bad := []string{
		"http://github.com/x",           // HTTPS가 아님
		"https://evil.com/artex.zip",    // 허용 목록에 없는 도메인
		"https://github.com.evil.com/x", // 도메인 접미사 위장
		"https://raw.githubusercontent.com.evil.com/x",
	}
	for _, raw := range bad {
		u := mustParse(t, raw)
		if err := checkURL(u); err == nil {
			t.Errorf("checkURL(%q) 거절해야한다", raw)
		}
	}
	good := []string{
		"https://api.github.com/repos/x/releases/latest",
		"https://objects.githubusercontent.com/blah",
		"https://GitHub.com/x", // 도메인 대소문자 구분 없음
	}
	for _, raw := range good {
		u := mustParse(t, raw)
		if err := checkURL(u); err != nil {
			t.Errorf("checkURL(%q) 출시되어야 하는데 오류가 보고됩니다.: %v", raw, err)
		}
	}
}

func TestAssetNameMatchesBuildScript(t *testing.T) {
	// build.sh의 package_binary는 artex-<version>-<os>-<arch>.zip을 사용하며 버전 번호는 다음과 같습니다.
	// v 접두사를 제거했습니다. 여기서는 하나의 캐릭터가 옳고 그름을 결정하며, 모든 플랫폼에서 원클릭 업데이트로는 해당 자산을 찾을 수 없습니다.
	if got := AssetName("v0.3.8", "linux", "amd64"); got != "artex-0.3.8-linux-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
	if got := AssetName("0.3.8", "windows", "amd64"); got != "artex-0.3.8-windows-amd64.zip" {
		t.Errorf("AssetName = %q", got)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("분석하다 %q: %v", raw, err)
	}
	return u
}

func TestSettleClearsMarkerAndStopsRollback(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "new", 0)
	fakeBin(t, p.Old, "old", 0)
	if err := writeMarker(p.Marker, marker{From: "0.3.7", To: "0.3.8", Attempts: 2}); err != nil {
		t.Fatal(err)
	}

	settle(p)

	if _, err := os.Stat(p.Marker); !os.IsNotExist(err) {
		t.Fatal("안정성을 확인한 후 업그레이드 표시를 지워야 합니다.")
	}
	// 표시가 사라지면 후속 일반 재시작에 더 이상 시간이 누적되지 않으며 롤백이 실수로 실행되지 않습니다.
	if _, ok := readMarker(p.Marker); ok {
		t.Error("태그 읽기가 실패해야 합니다.")
	}
	// 백업은 유지되어야 하며 사용자는 수동으로 롤백할 수 있습니다.
	if _, err := os.Stat(p.Old); err != nil {
		t.Error("안정적인지 확인한 후에도 이전 버전의 백업을 유지해야 합니다.")
	}
}

func TestSettleIsNoopWithoutMarker(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "cur", 0)
	settle(p) // 일반 시작 경로, 당황하거나 파일을 이동해서는 안 됩니다.
	if _, err := os.Stat(p.Current); err != nil {
		t.Error("표시가 없을 때 settle 어떤 파일에도 영향을 주어서는 안 됩니다.")
	}
}

package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

// smokeEnv를 사용하면 연기 테스트로 가져온 하위 프로세스가 부트스트랩을 직접 건너뛸 수 있습니다.
//
// 엄밀히 말하면 추가하지 않으면 아무 일도 일어나지 않습니다. 하위 프로세스의 os.Executable()은 artex.new에서 파생됩니다.
// 모든 경로에는 .new 접두사가 붙으며 실제 업그레이드 파일은 건드릴 수 없습니다. 하지만 그런 우연에 의존하는 것은 너무 취약해서,
// 명시적인 단락 회로는 한 눈에 명확하며 하위 프로세스에서 불필요한 디스크 프로브를 저장합니다.
const smokeEnv = "ARTEX_SELFUPDATE_SMOKE"

// Action은 Bootstrap이 main에 제공하는 명령입니다.
type Action int

const (
	// 계속: 평소대로 서버를 시작합니다.
	Continue Action = iota
	// 다시 시작: ExitRestart를 사용하여 즉시 종료하고 데몬 스크립트가 다시 시작되도록 합니다.
	Restart
)

// 상태는 /api/update/check에 대한 이 시작 시의 업그레이드 상태를 설명하여 프런트 엔드에 진실하게 알려줍니다.
// "마지막 업그레이드가 성공했습니까, 아니면 롤백되었습니까?"
type State struct {
	Pending     bool   // 교체 후에도 안정적인지는 확인되지 않았습니다.
	RolledBack  bool   // 이 시작은 방금 자동 롤백을 수행했습니다.
	FailedStage bool   // 임시 파일 확인/스모크가 실패하여 삭제되었습니다.
	Detail      string // 사용자를 위한 한 문장 설명
}

// 부트스트랩은 기본 시작 부분에서 실행되며 수신 대기 포트 이전과 데이터베이스를 열기 전에 호출되어야 합니다.
//
// 세 가지 상황:
//
// ① 임시파일 artex.new → 검증+연기가 있습니다. 통과하면 교체되고 다시 시작하라는 메시지가 표시됩니다. 실패하면 폐기되고 이전 버전을 계속 실행합니다.
//
//	② 표시된 파일만 남습니다 → 방금 설치가 완료되었으며 시도 횟수가 1회 누적되었음을 의미합니다. 연속해서 여러 번 실패하면 롤백됩니다.
//	③ 없음 → 정상 기동
func Bootstrap() (Action, State) {
	if os.Getenv(smokeEnv) != "" {
		return Continue, State{}
	}
	p, err := ResolvePaths()
	if err != nil {
		log.Printf("[업데이트] 부트스트래핑 건너뛰기: %v", err)
		return Continue, State{}
	}

	if _, err := os.Stat(p.New); err == nil {
		return applyStaged(p)
	}

	m, ok := readMarker(p.Marker)
	if !ok {
		return Continue, State{}
	}
	return confirmOrRollback(p, m)
}

// applyStaged는 "스테이징된 파일이 존재함" 상황을 처리합니다. 확인에 성공하면 대체되고, 실패하면 삭제됩니다.
//
// 이것은 전체 업그레이드 링크에서 실행 파일이 다루어지는 유일한 장소이자 마지막 관문이기도 합니다. - 스모크 테스트가 이를 차단합니다.
// 다운로드 손상, 잘못된 아키텍처 선택, 동적 링크 누락 등의 문제. 실행할 수 없는 바이너리가 출시되면
// 데몬 스크립트는 지칠 줄 모르고 반복적으로 이를 가져오며 Go 코드는 실행할 기회가 없으며 자동 롤백도 불가능합니다.
func applyStaged(p Paths) (Action, State) {
	m, _ := readMarker(p.Marker)

	if err := verifyStaged(p); err != nil {
		log.Printf("[업데이트] 새 임시 버전이 확인에 실패하여 삭제되었습니다. 현재 버전을 계속 실행하세요: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "새 버전 확인이 실패하여 삭제되었습니다:" + err.Error()}
	}

	if err := swap(p); err != nil {
		log.Printf("[업데이트] 교체에 실패했습니다. 현재 버전을 계속 실행하세요: %v", err)
		cleanStaged(p)
		_ = os.Remove(p.Marker)
		return Continue, State{FailedStage: true, Detail: "옷을 갈아입지 못했습니다:" + err.Error()}
	}

	// 전환이 성공했습니다. 표시를 유지하고 다음 시작(새 버전 실행)에 맡겨서 안정적인지 확인하십시오.
	m.Attempts = 0
	if m.StagedAt == 0 {
		m.StagedAt = time.Now().Unix()
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[업데이트] 업그레이드 표시 쓰기 실패(자동 롤백 기능 손실): %v", err)
	}
	log.Printf("[update] %s로 변경되었습니다. 다시 시작하려면 종료하세요(%d 종료)", orUnknown(m.To), ExitRestart)
	return Restart, State{Pending: true}
}

// verifyOrRollback은 "교체 후 시작"을 처리합니다. 누적 시도 횟수입니다. 제한이 초과되면 이전 버전이 교체됩니다.
//
// 개수는 Go 코드가 실행된 후에만 증가하므로 "실행할 수 있지만 초기화 중에 충돌이 발생함"을 다룹니다.
// (구성 비호환, 포트 점유, DB 마이그레이션 충돌) 이러한 유형의 실패; 교체 전 문제로 인해 "전혀 실행할 수 없습니다"
// 스모크 테스트가 이를 차단하고, 둘이 함께 완성됩니다.
func confirmOrRollback(p Paths, m marker) (Action, State) {
	m.Attempts++
	if m.Attempts > maxAttempts {
		if err := rollback(p); err != nil {
			//롤백이 실패하면 다시 시작하지 마세요. 그렇지 않으면 무한 재시작 상태에 빠지게 됩니다. 마크를 지우고,
			// 현재 상태에서 프로세스를 시작하십시오. 시작할 수 없는 경우 사용자는 최소한 로그에서 이유를 볼 수 있습니다.
			log.Printf("[업데이트] 새 버전이 연속으로 %d회 시작하지 못했고 롤백도 실패했습니다: %v", maxAttempts, err)
			_ = os.Remove(p.Marker)
			return Continue, State{Detail: "새 버전을 시작하지 못했으며 롤백에 실패했습니다:" + err.Error()}
		}
		log.Printf("[업데이트] 새 버전이 %d번 연속으로 시작되지 못했습니다. %s로 롤백되었으며 다시 시작하기 위해 종료되었습니다(%d 종료)",
			maxAttempts, orUnknown(m.From), ExitRestart)
		_ = os.Remove(p.Marker)
		return Restart, State{RolledBack: true, Detail: fmt.Sprintf("새 버전을 시작하지 못하여 %s(으)로 롤백되었습니다", orUnknown(m.From))}
	}
	if err := writeMarker(p.Marker, m); err != nil {
		log.Printf("[업데이트] 업그레이드 태그 업데이트 실패: %v", err)
	}
	log.Printf("[업데이트] 새 버전이 시작됩니다(%d/%d 시도). 안정적인 작동 후 업그레이드가 확정됩니다",
		m.Attempts, maxAttempts)
	return Continue, State{Pending: true}
}

// Settle은 새 버전이 안정적으로 실행되고 있는지 확인하고 업그레이드 표시를 지웁니다.
//
// HTTP 리스너 이후 기본 호출 지연: 이 시간 동안 살아남는 것이 중요합니다. 그렇지 않으면 표시가 그대로 유지됩니다.
// 다음 시작에서는 롤백이 트리거될 때까지 시도 횟수가 계속 누적됩니다.
func Settle() {
	p, err := ResolvePaths()
	if err != nil {
		return
	}
	settle(p)
}

func settle(p Paths) {
	if _, ok := readMarker(p.Marker); !ok {
		return // 업그레이드 후 부팅이 되지 않아 할 일이 없습니다.
	}
	if err := os.Remove(p.Marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("[업데이트] 업그레이드 표시를 지우지 못했습니다: %v", err)
		return
	}
	log.Printf("[update] 새 버전이 안정적으로 실행되고 있으며 업그레이드가 완료되었습니다. (이전 버전은 %s로 유지됩니다.)", p.Old)
}

// SettleDelay는 "새 버전이 살아 남았는지"를 확인하는 데 필요한 실행 시간입니다.
const SettleDelay = 30 * time.Second

// verifyStaged 준비된 파일을 확인합니다. 먼저 SHA256을 비교한 다음 실제로 가져와서 실행합니다.
func verifyStaged(p Paths) error {
	want, err := os.ReadFile(p.Sum)
	if err != nil {
		return fmt.Errorf("체크섬 읽기: %w", err)
	}
	got, err := fileSHA256(p.New)
	if err != nil {
		return fmt.Errorf("체크섬 계산: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(string(want)), got) {
		return errors.New("SHA256 불일치(손상되거나 변조된 다운로드)")
	}
	return smokeTest(p.New)
}

// smokeTest는 -h를 사용하여 새 바이너리를 가져와 현재 시스템에서 실제로 실행되는지 확인합니다.
// 이를 통해 다운로드 잘림, 잘못된 아키텍처 선택(실행 형식 오류), 종속성 누락 및 기타 주요 유형의 문제를 방지할 수 있습니다.
func smokeTest(bin string) error {
	if err := os.Chmod(bin, 0o755); err != nil {
		return fmt.Errorf("실행 권한 부여: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "-h")
	cmd.Env = append(os.Environ(), smokeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("스모크 테스트 시간 초과(새 바이너리가 응답하지 않음)")
	}
	if err != nil {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		return fmt.Errorf("스모크 테스트 실패: %v: %s", err, snippet)
	}
	return nil
}

// swap은 현재 바이너리를 새로운 임시 버전으로 바꿉니다.
//
// Unix와 Windows 모두 실행 중인 실행 파일의 이름을 바꿀 수 있습니다(Windows에서는 삭제 및
// 적용 범위, 이름 변경은 포함되지 않음)이므로 여기에서 플랫폼을 나눌 필요가 없으며 먼저 스스로 멈출 필요가 없습니다.
func swap(p Paths) error {
	// Windows 이름 변경은 기존 대상을 덮어쓰지 않으며 이전 업그레이드에서 남은 .old를 먼저 지워야 합니다.
	if err := os.Remove(p.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("이전 백업 %s 정리: %w", p.Old, err)
	}
	if err := os.Rename(p.Current, p.Old); err != nil {
		return fmt.Errorf("현재 버전 백업: %w", err)
	}
	if err := os.Rename(p.New, p.Current); err != nil {
		// 교체에 실패했지만 현재 버전이 이동되었으므로 있는 그대로 다시 가져와야 합니다. 그렇지 않으면 다음 시작 시 실행 파일이 없습니다.
		if rerr := os.Rename(p.Old, p.Current); rerr != nil {
			return fmt.Errorf("새 버전(%v)을 로드하지 못했고 현재 버전을 복원하지 못했습니다: %w", err, rerr)
		}
		return fmt.Errorf("새 버전 로드: %w", err)
	}
	_ = os.Remove(p.Sum)
	return nil
}

// 롤백은 이전 버전의 스왑 백업을 대체합니다.
func rollback(p Paths) error {
	if _, err := os.Stat(p.Old); err != nil {
		return fmt.Errorf("롤백 백업 없음 %s: %w", p.Old, err)
	}
	// 문제 해결을 위해 로드할 수 없는 새 버전을 직접 삭제하는 대신 .failed로 이동하세요.
	failed := p.Current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(p.Current, failed); err != nil {
		return fmt.Errorf("실패한 버전 제거: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		return fmt.Errorf("이전 버전 복원: %w", err)
	}
	return nil
}

// 롤백은 /api/update/rollback 구현으로, 이전 버전으로 적극적으로 돌아갑니다.
// 의상 변경만 완료되고 다시 시작도 데몬 스크립트에 맡겨집니다(호출자는 ExitRestart를 사용하여 종료됩니다).
func Rollback() error {
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p.Old); err != nil {
		return errors.New("롤백할 이전 버전이 없습니다(" + p.Old + " 존재하지 않음)")
	}
	cleanStaged(p)
	if err := smokeTest(p.Old); err != nil {
		return fmt.Errorf("이전 버전을 실행할 수 없으며 롤백이 거부됩니다: %w", err)
	}
	// 현재 및 백업 교체: 롤백한 후 다시 롤백할 수 있습니다.
	tmp := p.Current + ".swap"
	_ = os.Remove(tmp)
	if err := os.Rename(p.Current, tmp); err != nil {
		return fmt.Errorf("현재 버전 제거: %w", err)
	}
	if err := os.Rename(p.Old, p.Current); err != nil {
		_ = os.Rename(tmp, p.Current)
		return fmt.Errorf("이전 버전 로드: %w", err)
	}
	if err := os.Rename(tmp, p.Old); err != nil {
		log.Printf("[업데이트] 롤백 후 백업을 구성하지 못했습니다(작업에 영향을 주지 않음): %v", err)
	}
	_ = os.Remove(p.Marker)
	return nil
}

// HasBackup은 롤백할 수 있는 이전 버전이 있는지 보고하여 프런트 엔드에서 롤백 버튼 표시 여부를 결정할 수 있도록 합니다.
func HasBackup() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, err = os.Stat(p.Old)
	return err == nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "알 수 없는 버전"
	}
	return s
}

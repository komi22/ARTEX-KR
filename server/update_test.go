package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Autumn-27/artex/selfupdate"
)

// releaseCache는 GitHub 할당량을 보호하는 계층입니다. 인증되지 않은 API의 경우 IP당 60회만,
// 전체 페이지가 로드될 때마다 상단 표시줄의 "새 버전 사용 가능" 프롬프트가 확인됩니다. 캐시가 만료되면 사용자는 더 많은 캐시를 열 수 있습니다.
// 탭 페이지는 할당량을 모두 사용하게 되며 업데이트하고 싶은데 찾을 수 없습니다.

func newTestCache(fetch func(context.Context, *http.Client) (*selfupdate.Release, error)) *releaseCache {
	return &releaseCache{fetch: fetch}
}

func TestReleaseCacheServesFromCache(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	for range 5 {
		rel, err := c.get(t.Context(), nil, false)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if rel.TagName != "v0.3.8" {
			t.Fatalf("TagName = %q", rel.TagName)
		}
	}
	if calls != 1 {
		t.Errorf("5 쿼리는 원본으로만 반환되어야 합니다. 1 몇번, 실제 %d 배", calls)
	}
}

func TestReleaseCacheForceBypasses(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// "업데이트 확인"을 클릭하는 사용자는 실시간 결과를 받아야 합니다. 그렇지 않으면 캐시가 만료될 때까지 새로 출시된 버전이 표시되지 않습니다.
	if _, err := c.get(t.Context(), nil, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("force 캐싱을 우회하고 원본으로 돌아가야 합니다. 2 몇번, 실제 %d 배", calls)
	}
}

func TestReleaseCacheExpiresAfterTTL(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return &selfupdate.Release{TagName: "v0.3.8"}, nil
	})

	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	// TTL 도착 지점을 시뮬레이션하려면 드롭인 시간을 만료 직후로 이동하세요.
	c.at = time.Now().Add(-releaseTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("TTL 만료 후에는 원본으로 반환되어야 합니다. 예상되는 것은 2 몇번, 실제 %d 배", calls)
	}
}

func TestReleaseCacheUsesShorterTTLForErrors(t *testing.T) {
	calls := 0
	c := newTestCache(func(context.Context, *http.Client) (*selfupdate.Release, error) {
		calls++
		return nil, errors.New("github 연결할 수 없음")
	})

	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("오류가 반환될 것으로 예상")
	}
	// 실패 결과도 잠시 동안 캐시해야 합니다. 그렇지 않으면 GitHub에 연결할 수 없으면 페이지가 로드될 때마다 시간 초과를 기다리게 됩니다.
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("오류가 반환될 것으로 예상")
	}
	if calls != 1 {
		t.Errorf("오류는 짧은 기간 동안 캐시되어야 하며 소스로 반환될 것으로 예상됩니다. 1 몇번, 실제 %d 배", calls)
	}

	// 그러나 오류 TTL은 성공한 TTL보다 훨씬 짧아야 하며 네트워크는 복구 후 빠르게 자체적으로 복구될 수 있어야 합니다.
	if releaseErrTTL >= releaseTTL {
		t.Fatalf("오류 TTL(%v) 성공보다 짧아야 합니다 TTL(%v)", releaseErrTTL, releaseTTL)
	}
	c.at = time.Now().Add(-releaseErrTTL - time.Second)
	if _, err := c.get(t.Context(), nil, false); err == nil {
		t.Fatal("오류가 반환될 것으로 예상")
	}
	if calls != 2 {
		t.Errorf("오류 TTL 만료 후 다시 시도해야 합니다. 2 몇번, 실제 %d 배", calls)
	}
}

func TestReleaseCacheDoesNotPoisonOnCallerCancel(t *testing.T) {
	good := &selfupdate.Release{TagName: "v0.3.8"}
	c := newTestCache(func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return good, nil
	})
	if _, err := c.get(t.Context(), nil, false); err != nil {
		t.Fatal(err)
	}

	//방문자가 탭을 닫으면 요청이 취소됩니다. 그렇다고 GitHub에 문제가 있다는 의미는 아니며 "취소됨"을 입력하면 안 됩니다.
	// 캐시에 쓰기 - 그렇지 않으면 모든 방문자는 다음 30분 동안 설명할 수 없는 오류를 받게 됩니다.
	c.fetch = func(ctx context.Context, _ *http.Client) (*selfupdate.Release, error) {
		return nil, ctx.Err()
	}
	c.at = time.Now().Add(-releaseTTL - time.Second) // 캐시가 만료되도록 하고 강제로 소스로 돌아가도록 합니다.

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.get(ctx, nil, false); err == nil {
		t.Fatal("취소된 오류는 호출자에게 투명하게 전달되어야 합니다.")
	}

	// 주요 불변성: 취소된 시간은 흔적을 남기지 않습니다. 캐시에 "취소된" 오류가 없습니다.
	// 지난번의 좋은 성적이 아직도 유지되고 있습니다.
	if c.err != nil {
		t.Fatalf("취소 오류는 캐시에 기록되어서는 안 됩니다. %v", c.err)
	}
	if c.rel == nil || c.rel.TagName != "v0.3.8" {
		t.Fatalf("캐시는 마지막으로 좋은 결과를 유지해야 합니다. %+v", c.rel)
	}

	// 해당 취소로 인해 새로운 데이터가 생성되지 않았으므로 다음 방문자는 소스로 돌아가서 정상적으로 결과를 받아야 합니다.
	// 마지막 취소로 인해 영향을 받지 않습니다.
	c.fetch = func(context.Context, *http.Client) (*selfupdate.Release, error) {
		return good, nil
	}
	rel, err := c.get(t.Context(), nil, false)
	if err != nil {
		t.Fatalf("취소 후 일반 요청은 오류를 보고하지 않아야 합니다.: %v", err)
	}
	if rel == nil || rel.TagName != "v0.3.8" {
		t.Fatalf("정상적인 결과를 얻어야 합니다. %+v", rel)
	}
}

import { fileURLToPath } from "node:url";

//정적 내보내기: `NEXT_EXPORT=1 next build`는 웹/아웃에 대한 순수 정적 디렉터리를 생성하며, 이는 직접적으로 던져질 수 있습니다.
// nginx 웹 루트 디렉터리를 실행합니다. 개발(다음 개발)에서는 이 변수를 설정하지 않으며 /api 역생성 및 핫 업데이트를 유지합니다.
const isExport = process.env.NEXT_EXPORT === "1";
// Vercel 데모: 전체 사이트가 조롱되고 백엔드가 없으며 /api 역생성이 필요하지 않습니다.
const isMock = process.env.NEXT_PUBLIC_MOCK === "1";

/** @type {import('next').NextConfig} */
const nextConfig = {
  // 상위 디렉터리의 잠금 파일이 루트 디렉터리 추론 및 리소스 경로 생성에 영향을 미치지 않도록 하세요.
  turbopack: { root: fileURLToPath(new URL(".", import.meta.url)) },
  reactCompiler: true,
  // LAN IP에서 개발 리소스(HMR)에 대한 액세스를 허용하고 필요에 따라 추가 및 삭제합니다.
  // 개발 단계에서는 모든 IPv4 소스가 /_next/* 및 HMR에 액세스할 수 있습니다(LAN IP 변경은 영향을 받지 않음).
  // 참고: Next는 보안상의 이유로 기본 "*"를 금지하고 분할된 와일드카드를 요구합니다. "*.*.*.*"는 모든 IPv4와 일치합니다.
  allowedDevOrigins: ["*.*.*.*"],
  compiler: {
    removeConsole: process.env.NODE_ENV === "production",
  },
  ...(isExport
    ? {
        // 순수 정적 내보내기: 노드 런타임 없음; 이미지가 최적화되지 않았습니다. 각 경로는 <route>/index.html을 출력합니다.
        output: "export",
        images: { unoptimized: true },
        trailingSlash: true,
      }
    : isMock
      ? {
          // Vercel 모의 데모: 백엔드가 없고 /api 백엔드가 필요하지 않습니다.
          images: { unoptimized: true },
        }
      : {
          // 개발: /api/*를 Go 백엔드로 역방향(기본값: 8787, AUTOPENTEST_API로 재정의할 수 있음)
          async rewrites() {
            const backend = process.env.AUTOPENTEST_API ?? "http://localhost:8787";
            return [{ source: "/api/:path*", destination: `${backend}/api/:path*` }];
          },
        }),
};

export default nextConfig;

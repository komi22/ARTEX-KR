// 모의 스위치. 빌드 시간 동안 삽입된 공용 변수(NEXT_PUBLIC_ 접두사는 브라우저에서만 읽을 수 있습니다).
// Vercel에서 NEXT_PUBLIC_MOCK=1을 설정하면 전체 사이트가 백엔드 없이 조롱됩니다.
export const MOCK = process.env.NEXT_PUBLIC_MOCK === "1";

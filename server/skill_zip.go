package server

import (
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Go의 archive/zip에는 Store(0) 및 Deflate(8)라는 두 개의 내장 압축 해제기만 있으며, 다른 방법을 만나면 반환됩니다.
// "zip: 지원되지 않는 압축 알고리즘". 압축 소프트웨어는 종종 기본이 아닌 설정으로 다른 방법을 작성합니다.
// (7-Zip의 bzip2, WinZip의 zstd) 순수 Go가 해결할 수 있는 두 가지는 다음과 같습니다. 정말 해결이 안되는 것들은
// (Deflate64 / LZMA / XZ / PPMd / 암호화된 패키지) 압축 해제 전 하단 레이어 대신 중국어 프롬프트가 보고됩니다.
// 오류는 변경되지 않고 사용자에게 덤프됩니다.
const (
	zipMethodStore     = 0
	zipMethodDeflate   = 8
	zipMethodDeflate64 = 9
	zipMethodBzip2     = 12
	zipMethodLZMA      = 14
	zipMethodZstdPKW   = 20 // PKWARE 초기에 zstd에 할당된 번호
	zipMethodZstd      = 93
	zipMethodXZ        = 95
	zipMethodJPEG      = 96
	zipMethodWavPack   = 97
	zipMethodPPMd      = 98
	zipMethodAES       = 99
)

var zipMethodNames = map[uint16]string{
	zipMethodStore:     "Store",
	zipMethodDeflate:   "Deflate",
	zipMethodDeflate64: "Deflate64",
	zipMethodBzip2:     "bzip2",
	zipMethodLZMA:      "LZMA",
	zipMethodZstdPKW:   "Zstandard",
	zipMethodZstd:      "Zstandard",
	zipMethodXZ:        "XZ",
	zipMethodJPEG:      "JPEG",
	zipMethodWavPack:   "WavPack",
	zipMethodPPMd:      "PPMd",
	zipMethodAES:       "AES 암호화",
}

func zipMethodName(m uint16) string {
	if n, ok := zipMethodNames[m]; ok {
		return n
	}
	return "알 수 없음"
}

// newSkillZipReader parses an uploaded archive and registers the extra decompressors
// we can support beyond the stdlib's Store/Deflate.
func newSkillZipReader(buf []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("압축된 패키지를 구문 분석할 수 없습니다(zip 형식이어야 함): %w", err)
	}
	zr.RegisterDecompressor(zipMethodBzip2, func(r io.Reader) io.ReadCloser {
		return io.NopCloser(bzip2.NewReader(r))
	})
	zdec := zstd.ZipDecompressor(zstd.WithDecoderConcurrency(1))
	zr.RegisterDecompressor(zipMethodZstd, zdec)
	zr.RegisterDecompressor(zipMethodZstdPKW, zdec)
	return zr, nil
}

// skillZipEntry pairs a zip entry with its decoded (UTF-8) name — f.Name may hold
// raw GBK bytes, see zipEntryName.
type skillZipEntry struct {
	f    *zip.File
	name string
}

// skillZipEntries lists the archive's real files (no directory entries, no archiver
// junk) with their names decoded to UTF-8.
func skillZipEntries(zr *zip.Reader) []skillZipEntry {
	out := make([]skillZipEntry, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := zipEntryName(f)
		if strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/__MACOSX/") ||
			path.Base(name) == ".DS_Store" {
			continue // macOS 포장 잔여물
		}
		out = append(out, skillZipEntry{f: f, name: name})
	}
	return out
}

// zipEntryName은 항목 경로를 UTF-8로 반환합니다. Windows의 7-Zip/WinRAR/탐색기
// UTF-8 플래그가 설정되지 않으면 중국어 파일 이름이 GBK의 zip에 기록되고 Go는 이 바이트를 그대로 유지하므로 이름은
// 이는 합법적인 UTF-8도 아니고 경로 확인을 통과할 수도 없습니다. 여기서는 GBK에 따라 디코딩됩니다.
func zipEntryName(f *zip.File) string {
	if utf8.ValidString(f.Name) {
		return f.Name
	}
	if dec, err := simplifiedchinese.GBK.NewDecoder().String(f.Name); err == nil && utf8.ValidString(dec) {
		return dec
	}
	return f.Name
}

// checkSkillZipMethods rejects archives we cannot extract, naming the offending
// entry and method instead of letting f.Open() fail with an opaque English error.
func checkSkillZipMethods(entries []skillZipEntry) error {
	for _, e := range entries {
		if e.f.Flags&0x1 != 0 || e.f.Method == zipMethodAES {
			return fmt.Errorf("압축된 패키지가 암호화되었습니다(%s). 암호화되지 않은 zip을 업로드하세요", e.name)
		}
		switch e.f.Method {
		case zipMethodStore, zipMethodDeflate, zipMethodBzip2, zipMethodZstd, zipMethodZstdPKW:
		default:
			return fmt.Errorf("압축된 패키지가 지원되지 않는 압축 방법 %s(방법 %d): %s을(를) 사용합니다."+
				"\"저장소\" 또는 \"Deflate\"를 사용하여 재패키지하세요(7-Zip/WinRAR의 경우 압축 방법으로 Deflate를 선택하세요."+
				"또는 시스템 자체의 \"압축 폴더로 압축/보내기\" 또는 명령줄 zip -r)을 직접 사용하세요",
				zipMethodName(e.f.Method), e.f.Method, e.name)
		}
	}
	return nil
}

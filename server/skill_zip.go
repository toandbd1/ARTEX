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

// archive/zip của Go chỉ tích hợp sẵn hai bộ giải nén Store(0) và Deflate(8), gặp phương thức khác sẽ trả về
// "zip: unsupported compression algorithm". Phần mềm nén ở chế độ không mặc định thường ghi ra phương thức khác
// (bzip2 của 7-Zip, zstd của WinZip), nên ở đây bổ sung hai loại mà Go thuần có thể giải; những loại thực sự
// không giải được (Deflate64 / LZMA / XZ / PPMd / gói mã hóa) sẽ báo thông điệp tiếng Việt trước khi giải nén,
// thay vì quăng nguyên lỗi tầng dưới cho người dùng.
const (
	zipMethodStore     = 0
	zipMethodDeflate   = 8
	zipMethodDeflate64 = 9
	zipMethodBzip2     = 12
	zipMethodLZMA      = 14
	zipMethodZstdPKW   = 20 // số hiệu PKWARE cấp cho zstd ở thời kỳ đầu
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
	zipMethodAES:       "AES mã hóa",
}

func zipMethodName(m uint16) string {
	if n, ok := zipMethodNames[m]; ok {
		return n
	}
	return "không rõ"
}

// newSkillZipReader parses an uploaded archive and registers the extra decompressors
// we can support beyond the stdlib's Store/Deflate.
func newSkillZipReader(buf []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("không thể phân tích gói nén (phải là định dạng zip): %w", err)
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
			continue // tàn dư đóng gói của macOS
		}
		out = append(out, skillZipEntry{f: f, name: name})
	}
	return out
}

// zipEntryName returns the entry path as UTF-8. 7-Zip / WinRAR / Windows Explorer
// khi không bật cờ UTF-8 sẽ ghi tên file tiếng Trung vào zip theo GBK, Go giữ nguyên các byte đó, nên tên
// vừa không phải UTF-8 hợp lệ vừa không qua được kiểm tra đường dẫn — ở đây giải mã dự phòng theo GBK.
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
			return fmt.Errorf("gói nén đã bị mã hóa (%s), vui lòng tải lên zip không mã hóa", e.name)
		}
		switch e.f.Method {
		case zipMethodStore, zipMethodDeflate, zipMethodBzip2, zipMethodZstd, zipMethodZstdPKW:
		default:
			return fmt.Errorf("gói nén dùng phương thức nén không được hỗ trợ %s(method %d): %s."+
				"Hãy đóng gói lại bằng chế độ \"Store\" hoặc \"Deflate\" (chọn Deflate trong phương thức nén của 7-Zip/WinRAR, "+
				"hoặc dùng trực tiếp tính năng nén có sẵn của hệ thống / lệnh zip -r)",
				zipMethodName(e.f.Method), e.f.Method, e.name)
		}
	}
	return nil
}

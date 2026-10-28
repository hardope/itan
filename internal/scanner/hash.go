package scanner

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/zeebo/xxh3"
)

const chunk = 1 << 20 // 1 MiB

// ContentHash computes the portable "ph1" identity of a file:
//
//	xxh3_64( le_uint64(size) || first 1 MiB || last 1 MiB )
//
// Files of 2 MiB or less are hashed whole. See docs/export-format.md.
func ContentHash(path string) (hash string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size = st.Size()

	h := xxh3.New()
	var sz [8]byte
	binary.LittleEndian.PutUint64(sz[:], uint64(size))
	h.Write(sz[:])

	if size <= 2*chunk {
		if _, err := io.Copy(h, f); err != nil {
			return "", 0, err
		}
	} else {
		buf := make([]byte, chunk)
		if _, err := io.ReadFull(f, buf); err != nil {
			return "", 0, err
		}
		h.Write(buf)
		if _, err := f.ReadAt(buf, size-chunk); err != nil {
			return "", 0, err
		}
		h.Write(buf)
	}
	return fmt.Sprintf("ph1:%016x", h.Sum64()), size, nil
}

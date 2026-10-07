//go:build tray && windows

package native

// Reading the brand mark out of the embedded .ico.
//
// Explorer gets its tile from the icon resource stamped into the executable, and the tray
// used to draw something else again, which is how "the menu bar icon does not match Finder"
// happens on Windows. Both now come from one generated file: scripts/trayicon writes
// deploy/windows/TunnelMeshTray.ico, the same bytes are copied beside this package and
// embedded, and the shell turns the entry it needs into an HICON at run time. The
// generator, not a human with a screenshot, owns the artwork.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/png"
	"sync"
	"unsafe"

	_ "embed"

	"golang.org/x/sys/windows"
)

//go:embed icons/TunnelMeshTray.ico
var trayIconBytes []byte

var (
	iconCacheMu sync.Mutex
	iconCache   = map[int]windows.Handle{}
)

// trayIcon returns an HICON for the notification area or a window at the requested edge
// length in physical pixels.
//
// Handles are cached because a taskbar or tray that repaints asks for the same icon
// hundreds of times, and because destroying one that Windows still holds draws a blank
// tile. The process owns them for its whole lifetime instead.
func trayIcon(size int) windows.Handle {
	if size <= 0 {
		size = 16
	}
	iconCacheMu.Lock()
	defer iconCacheMu.Unlock()
	if cached, ok := iconCache[size]; ok {
		return cached
	}
	pixels, err := decodeICO(trayIconBytes, size)
	if err != nil {
		return 0
	}
	handle, err := createIcon(pixels)
	if err != nil {
		return 0
	}
	iconCache[size] = handle
	return handle
}

// icoEntry is one image inside an .ico container.
type icoEntry struct {
	size int
	data []byte
}

// decodeICO returns the entry closest to size, as un-premultiplied pixels.
//
// A miss is not fatal: the caller keeps a Windows-drawn placeholder. Both encodings the
// format allows are handled because the committed file today is all DIB entries while a
// regenerated one could carry PNG for the large sizes, and failing on that difference
// would mean the taskbar icon disappears after a brand change.
func decodeICO(container []byte, size int) (*image.NRGBA, error) {
	entries, err := parseICO(container)
	if err != nil {
		return nil, err
	}
	best := entries[0]
	for _, entry := range entries[1:] {
		if abs(entry.size-size) < abs(best.size-size) {
			best = entry
		}
	}
	return decodeICOEntry(best.data, best.size)
}

func parseICO(container []byte) ([]icoEntry, error) {
	if len(container) < 6 {
		return nil, errors.New("native: icon file is too short to be an .ico")
	}
	if binary.LittleEndian.Uint16(container[2:]) != 1 {
		return nil, errors.New("native: icon file is not an .ico (type must be 1)")
	}
	count := int(binary.LittleEndian.Uint16(container[4:]))
	if count == 0 || len(container) < 6+16*count {
		return nil, errors.New("native: icon file has no usable entries")
	}
	entries := make([]icoEntry, 0, count)
	for index := 0; index < count; index++ {
		record := container[6+16*index : 6+16*(index+1)]
		// A zero byte means 256; the field is one byte wide.
		edge := int(record[0])
		if edge == 0 {
			edge = 256
		}
		length := int(binary.LittleEndian.Uint32(record[8:]))
		offset := int(binary.LittleEndian.Uint32(record[12:]))
		if length <= 0 || offset+length > len(container) || offset < 6+16*count {
			return nil, errors.New("native: icon entry points outside the file")
		}
		entries = append(entries, icoEntry{size: edge, data: container[offset : offset+length]})
	}
	return entries, nil
}

// decodeICOEntry reads a PNG or a bottom-up 32-bit DIB into the same un-premultiplied
// model, so the two encodings cannot render differently.
func decodeICOEntry(data []byte, size int) (*image.NRGBA, error) {
	if isPNG(data) {
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		plain := image.NewNRGBA(decoded.Bounds())
		draw.Draw(plain, plain.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
		return plain, nil
	}
	if len(data) < 40 {
		return nil, errors.New("native: icon entry has no header")
	}
	width := int(int32(binary.LittleEndian.Uint32(data[4:])))
	// The header height counts the colour plane and the mask plane.
	height := int(int32(binary.LittleEndian.Uint32(data[8:])))
	if width <= 0 || height <= 0 || height/2 != width {
		return nil, errors.New("native: icon entry is not a square 32-bit DIB")
	}
	if binary.LittleEndian.Uint32(data[16:]) != 0 {
		return nil, errors.New("native: icon entry uses an unsupported compression")
	}
	rowBytes := ((width + 31) / 32) * 4
	pixels := data[40:]
	out := image.NewNRGBA(image.Rect(0, 0, width, height/2))
	if len(pixels) < out.Bounds().Dy()*rowBytes {
		return nil, errors.New("native: icon entry is truncated")
	}
	for y := 0; y < out.Bounds().Dy(); y++ {
		// Rows are stored bottom-up, and the mask plane that follows is left unread: the
		// alpha channel is what the shell blends with. The copy swaps BGRA into the order
		// Go's un-premultiplied model uses.
		source := (out.Bounds().Dy() - 1 - y) * rowBytes
		target := y * out.Stride
		for x := 0; x < width; x++ {
			from, to := source+x*4, target+x*4
			out.Pix[to], out.Pix[to+1], out.Pix[to+2], out.Pix[to+3] = pixels[from+2], pixels[from+1], pixels[from], pixels[from+3]
		}
	}
	return out, nil
}

// createIcon turns pixels into an HICON through CreateIconFromResourceEx, which takes the
// same in-memory DIB layout an .ico entry carries. No temporary file, no icon handle
// leaking through a GDI bitmap.
func createIcon(pixels *image.NRGBA) (windows.Handle, error) {
	bounds := pixels.Bounds()
	size := bounds.Dx()
	if size <= 0 || size != bounds.Dy() {
		return 0, errors.New("native: icon pixels are not square")
	}
	stream := make([]byte, 0, 40+size*size*4+((size+31)/32)*4*size)

	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[0:], 40)
	binary.LittleEndian.PutUint32(header[4:], uint32(size))
	binary.LittleEndian.PutUint32(header[8:], uint32(size*2))
	binary.LittleEndian.PutUint16(header[12:], 1)
	binary.LittleEndian.PutUint16(header[14:], 32)
	binary.LittleEndian.PutUint32(header[20:], uint32(size*size*4))
	stream = append(stream, header...)

	body := make([]byte, size*size*4)
	for y := 0; y < size; y++ {
		target := (size - 1 - y) * size * 4
		for x := 0; x < size; x++ {
			from, to := y*pixels.Stride+x*4, target+x*4
			body[to], body[to+1], body[to+2], body[to+3] = pixels.Pix[from+2], pixels.Pix[from+1], pixels.Pix[from], pixels.Pix[from+3]
		}
	}
	stream = append(stream, body...)
	// The AND mask stays zero: a 32-bit icon is alpha blended, and a set mask bit forces a
	// pixel transparent whatever its alpha says, which would punch holes in the anti-aliased
	// corners of the tile.
	stream = append(stream, make([]byte, ((size+31)/32)*4*size)...)

	handle, _, err := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&stream[0])), uintptr(len(stream)), iconVersion3, 1,
		uintptr(size), uintptr(size), 0)
	if handle == 0 {
		return 0, err
	}
	return windows.Handle(handle), nil
}

func isPNG(data []byte) bool {
	return len(data) > 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G'
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

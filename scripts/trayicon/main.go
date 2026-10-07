// Command trayicon draws the application and notification-area icons for the
// TunnelMesh client tray: the macOS .icns and menu-bar glyph, and the Windows .ico pair.
//
// The repository has no design toolchain and no editable binary source for artwork, so the
// icon is generated from geometry instead: the shape is reviewable text, the output is
// deterministic, and a brand change becomes one command (scripts/generate-tray-icon.sh)
// rather than a hunt for whoever owned the original file. Nothing at runtime imports this
// package; it is a build-time tool that happens to be written in the project's own language.
//
// The mark is the topology the product actually runs: one hub with three peers linked to
// it, on the blue tile the GitHub social card already uses. A closed triangle was the
// obvious drawing and was rejected on purpose, because it lands on top of another vendor's
// logo; an open hub-and-spoke graph is what a client, an agent and the server really are.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// masterSize is the canvas everything is drawn on. The icon set is produced by box-filtering
// this image down to each required size, which keeps one anti-aliasing decision in one place
// and makes the 16x16 tile a true average of the same artwork rather than a redraw.
const masterSize = 1024

// samplesPerAxis is the supersampling grid per output pixel. Four gives crisp edges on the
// rounded tile and on the link ends without turning the render into a wait.
const samplesPerAxis = 4

// The tile: an inset rounded square, filled with a vertical gradient.
//
// The inset matters. An icon that bleeds to the edge of its canvas looks oversized next to
// the system's own tiles in a Finder grid, and macOS applies no mask of its own.
var (
	tileLeft   = 0.06
	tileRight  = 0.94
	tileTop    = 0.06
	tileBottom = 0.94
	tileRadius = 0.198 // 22.5% of the tile side, close to the system's own squircle

	colorTop    = "#5AA9FF"
	colorBottom = "#1A5FC4"

	// The mark: three nodes and the links between them. The link half-width is generous
	// relative to a 1024 canvas on purpose, because at 16x16 a hairline link disappears and
	// the icon stops reading as a network at all.
	hubRadius  = 0.112
	peerRadius = 0.082
	linkHalf   = 0.050
	peerSpread = 0.240
)

// Menu-bar variant: the same mark, one colour, no tile.
//
// NSStatusItem draws a *template* image, so only the alpha channel survives and the system
// tints it for the current menu-bar appearance. The glyph reuses insideMark with the tile's
// own radii and link width rather than redrawing a lighter copy, because the point of the
// exercise is that the menu bar and Finder show one shape: a second geometry is a second
// thing to keep in sync, and it is exactly the drift that made the two disagree before.
//
// Only the framing differs. glyphInset is the share of the canvas left clear around the
// mark, and 0.20 puts the clover at 80% of the 18pt box, which is how much of the box a
// system menu-bar symbol fills.
const glyphInset = 0.20

// menuBarSizes are the edge lengths written for the status item, at 1x, 2x and 3x of the
// 18pt bar. Naming them ...@2x and ...@3x is what lets NSImage pick a representation per
// display instead of scaling one bitmap.
var menuBarSizes = []struct {
	suffix string
	size   int
}{
	{"", 18},
	{"@2x", 36},
	{"@3x", 54},
}

// hubCenter is the hub of the mark. It sits below the canvas centre because one peer points
// straight up: the bounding box of the whole mark, not its centre point, is what has to look
// centred inside the tile.
var hubCenter = point{0.5, 0.525}

// peerCenters returns the three peer node positions, derived rather than listed so the
// spacing stays exact if the spread or the centre ever changes.
func peerCenters() [3]point {
	peers := [3]point{}
	for i := range peers {
		angle := (-90 + float64(i)*120) * math.Pi / 180
		peers[i] = point{
			hubCenter.x + peerSpread*math.Cos(angle),
			hubCenter.y + peerSpread*math.Sin(angle),
		}
	}
	return peers
}

type point struct{ x, y float64 }
type rgba struct{ r, g, b, a float64 }

// parseHex reads a #rrggbb literal. It panics on a typo because the values are constants in
// this file, not operator input, and a silently wrong colour is harder to spot than a crash.
func parseHex(value string) rgba {
	trimmed := strings.TrimPrefix(value, "#")
	if len(trimmed) != 6 {
		panic("trayicon: not a #rrggbb colour: " + value)
	}
	part := func(offset int) float64 {
		parsed, err := strconv.ParseInt(trimmed[offset:offset+2], 16, 32)
		if err != nil {
			panic("trayicon: unreadable colour " + value + ": " + err.Error())
		}
		return float64(parsed) / 255
	}
	return rgba{part(0), part(2), part(4), 1}
}

// lerp mixes two colours by t in 0..1.
func lerp(from, to rgba, t float64) rgba {
	return rgba{
		from.r + (to.r-from.r)*t,
		from.g + (to.g-from.g)*t,
		from.b + (to.b-from.b)*t,
		1,
	}
}

// tile is the rounded-square background geometry. appTile is what Finder and Explorer show:
// an inset tile, because a tile that bleeds to the edge of its canvas looks oversized in a
// grid and macOS applies no mask of its own. trayTile is the notification-area variant: full
// bleed, so at 16 pixels the block is the icon rather than a mark floating inside padding
// that stops reading as a shape on a taskbar.
type tile struct {
	left, right, top, bottom, radius float64
}

var (
	appTile  = tile{tileLeft, tileRight, tileTop, tileBottom, tileRadius}
	trayTile = tile{0, 1, 0, 1, tileRadius / (tileRight - tileLeft)}
)

// insideRoundedRect reports whether a unit-space point falls inside the tile.
func insideRoundedRect(p point, t tile) bool {
	if p.x < t.left || p.x > t.right || p.y < t.top || p.y > t.bottom {
		return false
	}
	// Corner regions are decided by the circle that rounds them; anywhere else the point is
	// inside the rectangle by construction.
	nearestX := math.Max(t.left+t.radius, math.Min(p.x, t.right-t.radius))
	nearestY := math.Max(t.top+t.radius, math.Min(p.y, t.bottom-t.radius))
	dx, dy := p.x-nearestX, p.y-nearestY
	return dx*dx+dy*dy <= t.radius*t.radius
}

// distanceToSegment is the planar distance from p to the segment a..b, used to give the
// links their rounded caps for free.
func distanceToSegment(p, a, b point) float64 {
	vx, vy := b.x-a.x, b.y-a.y
	wx, wy := p.x-a.x, p.y-a.y
	length := vx*vx + vy*vy
	if length == 0 {
		return math.Hypot(wx, wy)
	}
	t := (wx*vx + wy*vy) / length
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p.x-(a.x+t*vx), p.y-(a.y+t*vy))
}

// insideMark reports whether a point belongs to the mesh: a spoke, the hub, or a peer.
func insideMark(p point) bool {
	for _, peer := range peerCenters() {
		if distanceToSegment(p, hubCenter, peer) <= linkHalf {
			return true
		}
	}
	for _, center := range peerCenters() {
		if insideCircle(p, center, peerRadius) {
			return true
		}
	}
	if insideCircle(p, hubCenter, hubRadius) {
		return true
	}
	return false
}

// markBounds returns the axis-aligned bounding box of the mesh mark in unit space. The node
// radii exceed the link half-width in both variants, so the circles decide the extent.
func markBounds() (point, point) {
	min := point{math.MaxFloat64, math.MaxFloat64}
	max := point{-math.MaxFloat64, -math.MaxFloat64}
	center := hubCenter
	radius := hubRadius
	min = point{math.Min(min.x, center.x-radius), math.Min(min.y, center.y-radius)}
	max = point{math.Max(max.x, center.x+radius), math.Max(max.y, center.y+radius)}
	for _, peer := range peerCenters() {
		min = point{math.Min(min.x, peer.x-peerRadius), math.Min(min.y, peer.y-peerRadius)}
		max = point{math.Max(max.x, peer.x+peerRadius), math.Max(max.y, peer.y+peerRadius)}
	}
	return min, max
}

// glyphPoint maps a canvas sample to mark space so the mark fills the canvas up to
// glyphInset, and stays centred. It is a pure scale and translate: no rotation, no aspect
// change, so the glyph is the same drawing rather than a reinterpretation of it.
func glyphPoint(c point) point { return scalePoint(c, 1-glyphInset) }

// trayPoint frames the mark inside the full-bleed block. It is larger than in the app tile
// and smaller than in the menu-bar glyph: the peer nodes are two pixels across at 16, which
// is where the mark stops reading as a network.
func trayPoint(c point) point { return scalePoint(c, 0.72) }

// scalePoint maps canvas space to mark space so the mark spans fill of the canvas and stays
// centred. Like glyphPoint it is a pure scale and translate, so every variant is one drawing
// rather than a reinterpretation of it.
func scalePoint(c point, fill float64) point {
	min, max := markBounds()
	center := point{(min.x + max.x) / 2, (min.y + max.y) / 2}
	span := math.Max(max.x-min.x, max.y-min.y)
	factor := span / fill
	return point{center.x + (c.x-0.5)*factor, center.y + (c.y-0.5)*factor}
}

// renderGlyph draws the monochrome menu-bar mark at size pixels: black where the mesh is,
// transparent outside it, with the same supersampling the tile uses so the two stay in step.
func renderGlyph(size int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	step := 1 / float64(samplesPerAxis)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var coverage float64
			for sy := 0; sy < samplesPerAxis; sy++ {
				for sx := 0; sx < samplesPerAxis; sx++ {
					px := (float64(x) + (float64(sx)+0.5)*step) / float64(size)
					py := (float64(y) + (float64(sy)+0.5)*step) / float64(size)
					if insideMark(glyphPoint(point{px, py})) {
						coverage++
					}
				}
			}
			samples := float64(samplesPerAxis * samplesPerAxis)
			alpha := clamp255(coverage / samples * 255)
			// Premultiplied black: colour times coverage, which for black is zero, so the
			// stored colour stays 0 and only alpha carries the shape.
			out.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: uint8(alpha)})
		}
	}
	return out
}

// identityPoint leaves the mark in the position the app tile has always used: it is drawn
// from the unit-space geometry directly rather than scaled to fill the canvas.
func identityPoint(c point) point { return c }

// insideCircle is the unit-space circle test the nodes are drawn with.
func insideCircle(p, center point, radius float64) bool {
	dx, dy := p.x-center.x, p.y-center.y
	return dx*dx+dy*dy <= radius*radius
}

// colourAt resolves one unit-space sample to a premultiplied colour.
func colourAt(p point, t tile, mark func(point) point) rgba {
	top, bottom := parseHex(colorTop), parseHex(colorBottom)
	switch {
	case insideMark(mark(p)):
		return rgba{1, 1, 1, 1}
	case insideRoundedRect(p, t):
		depth := (p.y - t.top) / (t.bottom - t.top)
		return lerp(top, bottom, depth)
	default:
		return rgba{}
	}
}

// render draws the mark on one tile at size pixels.
func render(size int, t tile, mark func(point) point) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	step := 1 / float64(samplesPerAxis)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var accR, accG, accB, accA float64
			for sy := 0; sy < samplesPerAxis; sy++ {
				for sx := 0; sx < samplesPerAxis; sx++ {
					px := (float64(x) + (float64(sx)+0.5)*step) / float64(size)
					py := (float64(y) + (float64(sy)+0.5)*step) / float64(size)
					c := colourAt(point{px, py}, t, mark)
					// Premultiplied accumulation: a transparent sample contributes nothing,
					// which is what keeps the tile edge from gaining a dark halo.
					accR += c.r * c.a
					accG += c.g * c.a
					accB += c.b * c.a
					accA += c.a
				}
			}
			// image.RGBA is a *premultiplied* model and the PNG encoder un-premultiplies it
			// on the way out, so the samples are stored as they were accumulated. Dividing
			// the colour by the alpha here would darken every anti-aliased edge instead.
			samples := float64(samplesPerAxis * samplesPerAxis)
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(clamp255(accR/samples*255 + 0.5)),
				G: uint8(clamp255(accG/samples*255 + 0.5)),
				B: uint8(clamp255(accB/samples*255 + 0.5)),
				A: uint8(clamp255(accA/samples*255 + 0.5)),
			})
		}
	}
	return out
}

func clamp255(value float64) float64 {
	return math.Max(0, math.Min(255, value))
}

// downsample box-filters a master image to one edge length. Every source pixel contributes
// equally, which is the honest way to shrink an icon that has to survive being drawn at 16
// pixels in a Finder list.
func downsample(src *image.RGBA, size int) *image.RGBA {
	bounds := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	scale := float64(bounds.Dx()) / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			x0 := int(math.Floor(float64(x) * scale))
			x1 := int(math.Ceil(float64(x+1) * scale))
			y0 := int(math.Floor(float64(y) * scale))
			y1 := int(math.Ceil(float64(y+1) * scale))
			if x1 > bounds.Dx() {
				x1 = bounds.Dx()
			}
			if y1 > bounds.Dy() {
				y1 = bounds.Dy()
			}
			// Both sums are premultiplied: r holds colour times coverage, a holds coverage.
			// Averaging them by the same count keeps the filter a true box, so a half-covered
			// edge pixel stays half transparent rather than going black.
			var r, g, b, a, n float64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					pixel := src.RGBAAt(sx, sy)
					weight := float64(pixel.A) / 255
					r += float64(pixel.R) * weight
					g += float64(pixel.G) * weight
					b += float64(pixel.B) * weight
					a += float64(pixel.A)
					n++
				}
			}
			if n == 0 {
				out.SetRGBA(x, y, color.RGBA{})
				continue
			}
			out.SetRGBA(x, y, color.RGBA{
				R: uint8(clamp255(r/n + 0.5)),
				G: uint8(clamp255(g/n + 0.5)),
				B: uint8(clamp255(b/n + 0.5)),
				A: uint8(clamp255(a/n + 0.5)),
			})
		}
	}
	return out
}

// encode writes one PNG.
func encode(path string, img *image.RGBA) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		return err
	}
	return file.Close()
}

// iconsetNames maps an .iconset file name to its edge length. iconutil rejects a directory
// that does not use exactly these names, and a missing size is the difference between an icon
// that looks right in a Dock tile and one that looks blurred in a Finder column.
var iconsetNames = []struct {
	file string
	size int
}{
	{"icon_16x16.png", 16},
	{"icon_16x16@2x.png", 32},
	{"icon_32x32.png", 32},
	{"icon_32x32@2x.png", 64},
	{"icon_128x128.png", 128},
	{"icon_128x128@2x.png", 256},
	{"icon_256x256.png", 256},
	{"icon_256x256@2x.png", 512},
	{"icon_512x512.png", 512},
	{"icon_512x512@2x.png", 1024},
}

// Icon sizes for the Windows .ico pair. The app icon carries the sizes Explorer asks for
// between a 16-pixel list row and a 256-pixel preview; the notification-area icon carries
// the three the taskbar and tray actually request at common scales, so the block a user
// sees is a true render at that size rather than a downscale of a downscale.
var (
	appIconSizes  = []int{16, 24, 32, 48, 64, 128, 256}
	trayIconSizes = []int{16, 24, 32}
)

// encodeICO writes one .ico file from rendered sizes.
//
// Every entry is an uncompressed 32-bit DIB rather than the PNG a Windows SDK tool would
// emit for the 256-pixel size. That costs a few hundred kilobytes and buys two things the
// tray needs: the file is readable by the shell at run time without a PNG-in-ICO special
// case, and the bytes stay deterministic, which is what lets a test assert that the
// committed icon still matches the geometry.
func encodeICO(path string, master *image.RGBA, sizes []int) error {
	images := make([]*image.RGBA, 0, len(sizes))
	for _, size := range sizes {
		if size == master.Bounds().Dx() {
			images = append(images, master)
			continue
		}
		images = append(images, downsample(master, size))
	}
	entries := make([][]byte, len(images))
	for i, img := range images {
		entries[i] = iconDIB(img)
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	header := make([]byte, 6)
	binary.LittleEndian.PutUint16(header[2:], 1) // type 1 is an icon, not a cursor
	binary.LittleEndian.PutUint16(header[4:], uint16(len(images)))
	directory := make([]byte, 16*len(images))
	// Image data starts after the header and the directory, and each entry points at where
	// its own bytes land in that order.
	offset := 6 + 16*len(images)
	for i, size := range sizes {
		entry := directory[i*16 : (i+1)*16]
		// A size of 256 is encoded as zero, which is how the format says "not a byte".
		edge := size
		if edge == 256 {
			edge = 0
		}
		entry[0] = byte(edge)
		entry[1] = byte(edge)
		binary.LittleEndian.PutUint16(entry[4:], 1)  // planes
		binary.LittleEndian.PutUint16(entry[6:], 32) // bits per pixel
		binary.LittleEndian.PutUint32(entry[8:], uint32(len(entries[i])))
		binary.LittleEndian.PutUint32(entry[12:], uint32(offset))
		offset += len(entries[i])
	}
	if _, err := file.Write(header); err != nil {
		return err
	}
	if _, err := file.Write(directory); err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := file.Write(entry); err != nil {
			return err
		}
	}
	return file.Close()
}

// iconDIB builds one entry: a BITMAPINFOHEADER whose height counts both the colour and the
// mask planes, bottom-up un-premultiplied BGRA pixels, and an all-zero AND mask.
//
// The mask has to stay zero: a 32-bit icon is alpha-blended, and a non-zero mask bit forces
// the pixel transparent no matter what its alpha says, which would punch holes in every
// anti-aliased edge the renderer worked to keep smooth.
func iconDIB(img *image.RGBA) []byte {
	size := img.Bounds().Dx()
	rowBytes := ((size + 31) / 32) * 4
	pixelBytes := size * size * 4

	header := make([]byte, 40)
	binary.LittleEndian.PutUint32(header[0:], 40)
	binary.LittleEndian.PutUint32(header[4:], uint32(size))
	// The doubled height is the format's way of describing "colour plane plus mask plane".
	binary.LittleEndian.PutUint32(header[8:], uint32(size*2))
	binary.LittleEndian.PutUint16(header[12:], 1)
	binary.LittleEndian.PutUint16(header[14:], 32)
	binary.LittleEndian.PutUint32(header[20:], uint32(pixelBytes))

	pixels := make([]byte, pixelBytes)
	// image.RGBA is premultiplied and an icon's alpha channel is not, so the conversion has
	// to un-premultiply; drawing through an NRGBA image is how the standard library does it.
	plain := image.NewNRGBA(img.Bounds())
	draw.Draw(plain, img.Bounds(), img, image.Point{}, draw.Src)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			source := plain.NRGBAAt(x, y)
			// Rows are stored bottom-up.
			target := ((size-1-y)*size + x) * 4
			pixels[target] = source.B
			pixels[target+1] = source.G
			pixels[target+2] = source.R
			pixels[target+3] = source.A
		}
	}
	return append(append(header, pixels...), make([]byte, rowBytes*size)...)
}

func main() {
	out := flag.String("out", "", "directory to write the .iconset into (required)")
	preview := flag.String("preview", "", "also write a full-size PNG here, for review")
	menubar := flag.String("menubar", "", "also write the monochrome menu-bar glyph here, at 1x/2x/3x")
	ico := flag.String("ico", "", "also write the Windows TunnelMeshClient.ico and TunnelMeshTray.ico here")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "trayicon: -out is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
		os.Exit(1)
	}

	master := render(masterSize, appTile, identityPoint)
	for _, entry := range iconsetNames {
		img := master
		if entry.size != masterSize {
			img = downsample(master, entry.size)
		}
		if err := encode(filepath.Join(*out, entry.file), img); err != nil {
			fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
			os.Exit(1)
		}
	}
	if *preview != "" {
		if err := encode(*preview, master); err != nil {
			fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
			os.Exit(1)
		}
	}
	written := len(iconsetNames)
	if dir := *ico; dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
			os.Exit(1)
		}
		// Both files come out of the same geometry as the .icns, so the taskbar block and
		// the Explorer tile cannot disagree the way two hand-placed PNGs did.
		if err := encodeICO(filepath.Join(dir, "TunnelMeshClient.ico"), master, appIconSizes); err != nil {
			fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
			os.Exit(1)
		}
		block := render(masterSize, trayTile, trayPoint)
		if err := encodeICO(filepath.Join(dir, "TunnelMeshTray.ico"), block, trayIconSizes); err != nil {
			fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
			os.Exit(1)
		}
		written += 2
	}
	if dir := *menubar; dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
			os.Exit(1)
		}
		// Rendered from the same geometry at each size rather than downsampled from the
		// 1024 master: a 1.4px stroke survives supersampling at 18px and does not survive a
		// 57x box filter, which would leave the small sizes visibly lighter than the large.
		for _, entry := range menuBarSizes {
			file := filepath.Join(dir, "TunnelMeshMenuBar"+entry.suffix+".png")
			if err := encode(file, renderGlyph(entry.size)); err != nil {
				fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
				os.Exit(1)
			}
			written++
		}
	}
	fmt.Printf("wrote %d icon entries to %s\n", written, *out)
}

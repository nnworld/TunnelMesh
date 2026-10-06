// Command trayicon draws the macOS application icon for the TunnelMesh client tray.
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
	"flag"
	"fmt"
	"image"
	"image/color"
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

// insideRoundedRect reports whether a unit-space point falls inside the tile.
func insideRoundedRect(p point) bool {
	if p.x < tileLeft || p.x > tileRight || p.y < tileTop || p.y > tileBottom {
		return false
	}
	// Corner regions are decided by the circle that rounds them; anywhere else the point is
	// inside the rectangle by construction.
	nearestX := math.Max(tileLeft+tileRadius, math.Min(p.x, tileRight-tileRadius))
	nearestY := math.Max(tileTop+tileRadius, math.Min(p.y, tileBottom-tileRadius))
	dx, dy := p.x-nearestX, p.y-nearestY
	return dx*dx+dy*dy <= tileRadius*tileRadius
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

// insideCircle is the unit-space circle test the nodes are drawn with.
func insideCircle(p, center point, radius float64) bool {
	dx, dy := p.x-center.x, p.y-center.y
	return dx*dx+dy*dy <= radius*radius
}

// colourAt resolves one unit-space sample to a premultiplied colour.
func colourAt(p point) rgba {
	top, bottom := parseHex(colorTop), parseHex(colorBottom)
	switch {
	case insideMark(p):
		return rgba{1, 1, 1, 1}
	case insideRoundedRect(p):
		t := (p.y - tileTop) / (tileBottom - tileTop)
		return lerp(top, bottom, t)
	default:
		return rgba{}
	}
}

// render draws the master image at size pixels.
func render(size int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	step := 1 / float64(samplesPerAxis)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var accR, accG, accB, accA float64
			for sy := 0; sy < samplesPerAxis; sy++ {
				for sx := 0; sx < samplesPerAxis; sx++ {
					px := (float64(x) + (float64(sx)+0.5)*step) / float64(size)
					py := (float64(y) + (float64(sy)+0.5)*step) / float64(size)
					c := colourAt(point{px, py})
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

func main() {
	out := flag.String("out", "", "directory to write the .iconset into (required)")
	preview := flag.String("preview", "", "also write a full-size PNG here, for review")
	flag.Parse()

	if *out == "" {
		fmt.Fprintln(os.Stderr, "trayicon: -out is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "trayicon: %v\n", err)
		os.Exit(1)
	}

	master := render(masterSize)
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
	fmt.Printf("wrote %d iconset entries to %s\n", len(iconsetNames), *out)
}

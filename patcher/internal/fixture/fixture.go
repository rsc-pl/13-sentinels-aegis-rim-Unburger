// Package fixture builds small synthetic inputs (a game-style archive plus a
// patch-data bundle) so the whole pipeline can be exercised without any
// copyrighted game data. It is used by the tests and by the offline demo
// generator.
package fixture

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"13s-patcher/internal/hashutil"
	"13s-patcher/internal/patchdata"
	"13s-patcher/internal/synth"
)

// Fixture describes a generated test fixture.
type Fixture struct {
	Dir           string
	SourceArchive string
	BundleDir     string
	OutputDir     string
	SourceSHA256  string
	SourceSize    int64
	Operations    int
}

// SourceMembers returns the synthetic archive members.
func SourceMembers() []synth.Member {
	return []synth.Member{
		{Dir: "Fms", Name: "Greeting.fms", ID: 0, Logical: synth.Bank([]string{
			"Hello, pilot.", "@#(1)Begin@/b", "Goodbye.",
		})},
		{Dir: "Fms", Name: "Menu.fms", ID: 1, Logical: synth.Bank([]string{
			"Start", "Continue", "Quit",
		})},
		{Dir: "data", Name: "blob.bin", ID: 2, Logical: synth.Repetitive(3000, 1), Compress: true},
		{Dir: "data", Name: "small.bin", ID: 3, Logical: synth.Bytes(300, 9)},
	}
}

// SourceArchive builds the synthetic source archive bytes.
func SourceArchive() ([]byte, error) { return synth.Archive(SourceMembers()) }

// Ops returns the synthetic patch operations. The set deliberately includes a
// token-preserving edit so control-token validation is exercised.
func Ops() []patchdata.ExportOperation {
	return []patchdata.ExportOperation{
		{ID: "op-greeting-0", Member: "Fms/Greeting.fms", RecordIndex: 0, Original: "Hello, pilot.", Replacement: "Greetings, pilot.", PreserveTokens: true},
		{ID: "op-greeting-1", Member: "Fms/Greeting.fms", RecordIndex: 1, Original: "@#(1)Begin@/b", Replacement: "@#(1)Start@/b", PreserveTokens: true},
		{ID: "op-menu-2", Member: "Fms/Menu.fms", RecordIndex: 2, Original: "Quit", Replacement: "Exit Game", PreserveTokens: true},
	}
}

// Write creates a fixture under dir with src/, patchdata/ and out/ subdirectories.
func Write(dir string) (*Fixture, error) {
	f := &Fixture{
		Dir:           dir,
		SourceArchive: filepath.Join(dir, "src", "GAME_PATCH.CPK"),
		BundleDir:     filepath.Join(dir, "patchdata"),
		OutputDir:     filepath.Join(dir, "out"),
	}
	for _, d := range []string{filepath.Dir(f.SourceArchive), f.BundleDir, f.OutputDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	raw, err := SourceArchive()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.SourceArchive, raw, 0o644); err != nil {
		return nil, err
	}
	// The synthetic fixture has no game-derived plate scripts or title atlas.
	// Allow those additions to be absent explicitly; the production default
	// stays fail-closed and is covered by its own tests.
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte("{\n  \"allow_missing_resources\": true\n}\n"), 0o644); err != nil {
		return nil, err
	}
	f.SourceSHA256 = hashutil.Bytes(raw)
	f.SourceSize = int64(len(raw))

	in := patchdata.ExportInput{
		BundleID:       "example-1",
		BundleVersion:  "1.0.0",
		ReleaseVersion: "2026.1",
		Game:           patchdata.GameInfo{Title: "Example Game", Region: "US", BaseVersion: "1.10"},
		FormatVersion:  patchdata.SupportedFormatVersion,
		Sources: []patchdata.SourceSpec{
			{Role: "patch_archive", Name: "Game patch archive", PathHint: "GAME_PATCH.CPK", Size: f.SourceSize, SHA256: f.SourceSHA256},
		},
		Operations: Ops(),
		Output:     patchdata.OutputSpec{PreserveUnmodified: true, MemberCountDelta: 0},
	}
	if _, err := patchdata.ExportBundle(f.BundleDir, in); err != nil {
		return nil, fmt.Errorf("export fixture bundle: %w", err)
	}
	f.Operations = len(in.Operations)
	return f, nil
}

// WriteDemo writes a human-friendly demo tree: source/, patchdata/ and out/.
func WriteDemo(dir string) (*Fixture, error) {
	return Write(dir)
}

// PlateBase writes a synthetic background image into dir/resources. It is the
// optional-override fixture: normal builds use the shipped public presentation
// plate, so tests and demos only call this to exercise a deliberate user
// replacement. Synthetic pixels only: no game data is involved.
func PlateBase(dir string) error {
	d := filepath.Join(dir, "resources")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	img := image.NewNRGBA(image.Rect(0, 0, 640, 360))
	for y := 0; y < 360; y++ {
		for x := 0; x < 640; x++ {
			o := img.PixOffset(x, y)
			img.Pix[o] = uint8(x % 256)
			img.Pix[o+1] = uint8(y % 256)
			img.Pix[o+2] = uint8((x * y) % 256)
			img.Pix[o+3] = 255
		}
	}
	f, err := os.Create(filepath.Join(d, "plate_base.png"))
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// TitleBase writes a synthetic, structurally-real GUI/title.ftx atlas into
// dir/resources so the title-footer branding can be composed. The payload is
// synthetic: no game data is involved.
func TitleBase(dir string) error {
	d := filepath.Join(dir, "resources")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(d, "GUI__title.ftx"), SynthTitleFTX(), 0o644)
}

// SynthTitleFTX builds a valid FTX container with the same sub-texture geometry
// as the shipped title atlas (three 2048x2048 and three 1024x1024 sub-textures)
// and deterministic synthetic payloads.
func SynthTitleFTX() []byte {
	sizes := [][2]int{{2048, 2048}, {1024, 1024}, {2048, 2048}, {1024, 1024}, {1024, 1024}, {2048, 2048}}
	const (
		hdrSize = 320
		phdr    = 64
		dstart  = 256
	)
	var body []byte
	for i, s := range sizes {
		w, h := s[0], s[1]
		dlen := w * h
		psize := dlen + dstart
		blk := make([]byte, phdr+psize)
		copy(blk[0:4], "FTX0")
		put32(blk, 4, uint32(psize))
		put32(blk, 8, uint32(phdr))
		tex := phdr
		put32(blk, tex+0x00, 0x7865742E) // ".tex"
		put32(blk, tex+0x04, 77)
		put32(blk, tex+0x08, 1)
		put32(blk, tex+0x0C, uint32(w))
		put32(blk, tex+0x10, uint32(h))
		put32(blk, tex+0x14, 1)
		put32(blk, tex+0x18, 1)
		put32(blk, tex+0x1C, dstart)
		put32(blk, tex+0x20, uint32(dlen))
		for j := tex + dstart; j < len(blk); j++ {
			blk[j] = byte(j*31 + i*7)
		}
		body = append(body, blk...)
	}
	total := hdrSize + len(body) + 16
	out := make([]byte, total)
	copy(out[0:4], "FTEX")
	put32(out, 4, uint32(total-hdrSize-16))
	put32(out, 8, hdrSize)
	put32(out, 0x0C, uint32(len(sizes)))
	copy(out[hdrSize:], body)
	copy(out[hdrSize+len(body):], "FEOC")
	put32(out, hdrSize+len(body)+4, 0)
	put32(out, hdrSize+len(body)+8, 0x10)
	return out
}

func put32(b []byte, o int, v uint32) {
	b[o] = byte(v)
	b[o+1] = byte(v >> 8)
	b[o+2] = byte(v >> 16)
	b[o+3] = byte(v >> 24)
}

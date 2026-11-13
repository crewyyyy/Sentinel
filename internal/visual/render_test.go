package visual

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func samplePNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 16, 16))
	im.Set(1, 1, color.White)
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestFFmpegFormats(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "source.png")
	if err := os.WriteFile(input, samplePNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"png", nil},
		{"jpg", []string{"-frames:v", "1"}},
		{"webp", []string{"-frames:v", "1", "-c:v", "libwebp"}},
		{"mp4", []string{"-t", "3", "-c:v", "libx264", "-pix_fmt", "yuv420p"}},
		{"webm", []string{"-t", "3", "-c:v", "libvpx-vp9"}},
		{"short.webm", []string{"-t", "0.08", "-c:v", "libvpx-vp9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := input
			if tc.name != "png" {
				path = filepath.Join(dir, "test."+tc.name)
				args := []string{"-nostdin", "-v", "error", "-threads", "1", "-loop", "1", "-i", input}
				args = append(args, tc.args...)
				args = append(args, "-threads", "1", path)
				if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
					t.Fatalf("fixture conversion: %v %s", err, out)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			frames, err := (FFmpeg{}).Frames(context.Background(), data, 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(frames) < 1 || len(frames) > 3 {
				t.Fatal("invalid frame count", len(frames))
			}
			for _, frame := range frames {
				c, err := png.DecodeConfig(bytes.NewReader(frame))
				if err != nil || c.Width > 768 || c.Height > 768 {
					t.Fatal(c, err)
				}
			}
		})
	}
	// A real animated GIF fixture without depending on a public download.
	im := image.NewPaletted(image.Rect(0, 0, 16, 16), color.Palette{color.Black, color.White})
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{im, im, im}, Delay: []int{100, 100, 100}, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	frames, err := (FFmpeg{}).Frames(context.Background(), b.Bytes(), 3)
	if err != nil || len(frames) != 3 {
		t.Fatal("GIF sampling", len(frames), err)
	}
}
func TestRendererRejectsUntrustedInputs(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("https://private.example/image.png"), bytes.Repeat([]byte("x"), MaxInputBytes+1), []byte("\x89PNG\r\n\x1a\ncorrupt")} {
		if _, err := (FFmpeg{}).Frames(context.Background(), b, 1); err == nil {
			t.Fatal("accepted corrupt media")
		}
	}
	data := samplePNG(t)
	frames, err := splitPNG(append(append([]byte{}, data...), data...))
	if err != nil || len(frames) != 2 {
		t.Fatal(err)
	}
	if _, err := splitPNG(append(data, 'x')); err == nil {
		t.Fatal("trailing bytes accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (FFmpeg{}).Frames(ctx, data, 1); err == nil {
		t.Fatal("canceled renderer continued")
	}
}

package hw

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"slices"
	"strings"
	"testing"
)

func TestCaptureArgs(t *testing.T) {
	got := strings.Join(captureArgs(CaptureOptions{Index: 1, Width: 640, Height: 480, Quality: 85, Rotation: 180, HFlip: true}), " ")
	want := "--camera 1 -n -t 1 --width 640 --height 480 -q 85 --rotation 180 --hflip -e jpg -o -"
	if got != want {
		t.Errorf("args\n got %s\nwant %s", got, want)
	}
	plain := captureArgs(CaptureOptions{Width: 64, Height: 48, Quality: 85})
	if slices.Contains(plain, "--rotation") || slices.Contains(plain, "--vflip") {
		t.Errorf("unexpected mount flags: %v", plain)
	}
}

func TestParseCameraList(t *testing.T) {
	out := `Available cameras
-----------------
0 : imx708 [4608x2592 10-bit RGGB] (/base/soc/i2c0mux/i2c@1/imx708@1a)
    Modes: 'SRGGB10_CSI2P' : 1536x864 [120.13 fps - (768, 432)/3072x1728 crop]
                             2304x1296 [56.03 fps - (0, 0)/4608x2592 crop]

1 : imx477 [4056x3040 12-bit RGGB] (/base/soc/i2c0mux/i2c@0/imx477@1a)
`
	got := parseCameraList(out)
	if len(got) != 2 || got[0] != (CameraInfo{0, "imx708"}) || got[1] != (CameraInfo{1, "imx477"}) {
		t.Errorf("cameras: %+v", got)
	}
	if got := parseCameraList("No cameras available!\n"); len(got) != 0 {
		t.Errorf("expected none, got %+v", got)
	}
}

// noisy makes a photo that compresses badly, like a real busy scene.
func noisy(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(1))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95})
	return buf.Bytes()
}

func TestFitJPEG(t *testing.T) {
	src := noisy(800, 600)
	same, w, h, err := FitJPEG(src, len(src))
	if err != nil || !bytes.Equal(same, src) || w != 800 || h != 600 {
		t.Fatalf("a photo that fits must pass through unchanged (w=%d h=%d err=%v)", w, h, err)
	}
	limit := len(src) / 6
	out, w, h, err := FitJPEG(src, limit)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > limit {
		t.Errorf("%d bytes, limit %d", len(out), limit)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(out))
	if err != nil || cfg.Width != w || cfg.Height != h || w >= 800 {
		t.Errorf("reported %d×%d, decoded %+v (%v); expected a smaller photo", w, h, cfg, err)
	}
	if _, _, _, err := FitJPEG(src, 100); err != ErrPhotoTooLarge {
		t.Errorf("impossible limit: got %v", err)
	}
}

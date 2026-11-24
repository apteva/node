package hw

import (
	"bufio"
	"bytes"
	"errors"
	"image"
	"image/draw"
	"image/jpeg"
	"regexp"
	"strconv"
	"strings"
)

// CaptureOptions describes one still photo. Rotation and the flips correct
// for how the camera is mounted; libcamera can only rotate by 0 or 180.
type CaptureOptions struct {
	Index         int // camera number as rpicam-hello --list-cameras prints it
	Width, Height int
	Quality       int // JPEG quality, 1–100
	Rotation      int // 0 or 180
	HFlip, VFlip  bool
}

// CameraInfo is one camera the system can see.
type CameraInfo struct {
	Index int    `json:"index"`
	Model string `json:"model"`
}

// ErrNoCameraSoftware means rpicam-apps (or the older libcamera-apps) is not
// installed, so neither detection nor capture can work.
var ErrNoCameraSoftware = errors.New("camera software not installed (apt install rpicam-apps)")

// captureArgs builds the rpicam-still command line: no preview, shortest
// warm-up, JPEG to stdout.
func captureArgs(o CaptureOptions) []string {
	args := []string{
		"--camera", strconv.Itoa(o.Index),
		"-n", "-t", "1",
		"--width", strconv.Itoa(o.Width), "--height", strconv.Itoa(o.Height),
		"-q", strconv.Itoa(o.Quality),
	}
	if o.Rotation == 180 {
		args = append(args, "--rotation", "180")
	}
	if o.HFlip {
		args = append(args, "--hflip")
	}
	if o.VFlip {
		args = append(args, "--vflip")
	}
	return append(args, "-e", "jpg", "-o", "-")
}

// "0 : imx708 [4608x2592 10-bit RGGB] (/base/soc/i2c0mux/i2c@1/imx708@1a)"
var cameraLine = regexp.MustCompile(`^\s*(\d+)\s*:\s*(\S+)`)

// parseCameraList reads `rpicam-hello --list-cameras` output. "No cameras
// available!" (or anything without camera lines) is an empty list.
func parseCameraList(out string) []CameraInfo {
	var cams []CameraInfo
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		m := cameraLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		i, _ := strconv.Atoi(m[1])
		cams = append(cams, CameraInfo{Index: i, Model: m[2]})
	}
	return cams
}

// ErrPhotoTooLarge means the photo could not be brought under the size
// limit even at reduced quality and resolution.
var ErrPhotoTooLarge = errors.New("photo too large to send, even at reduced quality and size")

// FitJPEG returns a JPEG of at most limit bytes: the photo itself if it
// already fits, otherwise re-encoded at lower quality, then at half the
// resolution, down to minFitWidth pixels wide. It also returns the final
// dimensions. Re-encoding the captured frame avoids a second exposure,
// which would be slow and could show a different scene.
func FitJPEG(src []byte, limit int) (out []byte, width, height int, err error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, 0, 0, err
	}
	if len(src) <= limit {
		return src, cfg.Width, cfg.Height, nil
	}
	decoded, err := jpeg.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, 0, 0, err
	}
	img := image.NewRGBA(decoded.Bounds())
	draw.Draw(img, img.Bounds(), decoded, decoded.Bounds().Min, draw.Src)
	var buf bytes.Buffer
	for {
		for _, q := range []int{80, 65, 50} {
			buf.Reset()
			if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
				return nil, 0, 0, err
			}
			if buf.Len() <= limit {
				b := img.Bounds()
				return bytes.Clone(buf.Bytes()), b.Dx(), b.Dy(), nil
			}
		}
		if img.Bounds().Dx()/2 < minFitWidth {
			return nil, 0, 0, ErrPhotoTooLarge
		}
		img = halve(img)
	}
}

const minFitWidth = 160

// halve averages each 2×2 block.
func halve(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx()/2, b.Dy()/2
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		r0 := src.Pix[(2*y)*src.Stride:]
		r1 := src.Pix[(2*y+1)*src.Stride:]
		d := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			i := 8 * x
			for c := 0; c < 4; c++ {
				sum := int(r0[i+c]) + int(r0[i+4+c]) + int(r1[i+c]) + int(r1[i+4+c])
				d[4*x+c] = uint8((sum + 2) / 4)
			}
		}
	}
	return dst
}

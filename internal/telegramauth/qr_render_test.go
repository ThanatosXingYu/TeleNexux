package telegramauth

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"
)

func TestRenderQRPNGUsesTheFullCanvas(t *testing.T) {
	data, err := renderQRPNG("tg://login?token=test")
	if err != nil {
		t.Fatalf("render QR PNG: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode QR PNG: %v", err)
	}

	bounds := decoded.Bounds()
	minimumBlackX := bounds.Max.X
	maximumBlackX := bounds.Min.X
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			gray := color.GrayModel.Convert(decoded.At(x, y)).(color.Gray)
			if gray.Y < 128 {
				minimumBlackX = min(minimumBlackX, x)
				maximumBlackX = max(maximumBlackX, x)
			}
		}
	}
	width := bounds.Dx()
	if minimumBlackX > width/5 || maximumBlackX < width*4/5 {
		t.Fatalf("QR code does not fill canvas: width=%d black range=%d..%d",
			width, minimumBlackX, maximumBlackX)
	}
}

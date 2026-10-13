package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"strings"
	"testing"
)

// makePNG 造一张 width x height 的实心 PNG
func makePNG(t *testing.T, width, height int) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := range width {
		for y := range height {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x80, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码 PNG 失败：%v", err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, payload ImagePayload) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		t.Fatalf("解 base64 失败：%v", err)
	}
	return raw
}

// 达标的小图原样过，不重新编码 —— 一编一解平白掉画质，GIF 还会丢掉动画
func TestImageFromBytesPassesThroughSmallImage(t *testing.T) {
	raw := makePNG(t, 40, 30)

	payload, err := ImageFromBytes(raw)
	if err != nil {
		t.Fatalf("认不出来：%v", err)
	}
	if payload.Mime != "image/png" || payload.Width != 40 || payload.Height != 30 {
		t.Errorf("形状不对：%+v", payload)
	}
	if payload.Resized {
		t.Error("没超限不该重编码")
	}
	if !bytes.Equal(decode(t, payload), raw) {
		t.Error("没超限的图该原样出去")
	}
}

// 超过尺寸上限的图缩到上限里，原图尺寸留着 —— 说明文案要靠它算坐标换算
func TestImageFromBytesResizesOversizedImage(t *testing.T) {
	raw := makePNG(t, maxImageWidth+200, 100)

	payload, err := ImageFromBytes(raw)
	if err != nil {
		t.Fatalf("处理失败：%v", err)
	}
	if !payload.Resized {
		t.Fatal("超过尺寸上限的图该被缩过")
	}
	if payload.Width != maxImageWidth || payload.Height >= 100 {
		t.Errorf("该等比缩到宽 %d，实际 %dx%d", maxImageWidth, payload.Width, payload.Height)
	}
	if payload.OriginalWidth != maxImageWidth+200 || payload.OriginalHeight != 100 {
		t.Errorf("原图尺寸该留着，实际 %dx%d", payload.OriginalWidth, payload.OriginalHeight)
	}
	if encodedLen(len(decode(t, payload))) >= maxImageBase64 {
		t.Error("缩完还在体积上限之上")
	}
}

// 认得出来的格式都得能解 —— 嗅探和解码器两边的名单不能对不上
func TestImageFromBytesAcceptsSniffedFormats(t *testing.T) {
	gifBytes := func() []byte {
		palette := color.Palette{color.Black, color.White}
		img := image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
		var buf bytes.Buffer
		if err := gif.Encode(&buf, img, nil); err != nil {
			t.Fatalf("编码 GIF 失败：%v", err)
		}
		return buf.Bytes()
	}()

	payload, err := ImageFromBytes(gifBytes)
	if err != nil {
		t.Fatalf("GIF 该认得：%v", err)
	}
	if payload.Mime != "image/gif" {
		t.Errorf("GIF 的 mime 不对：%s", payload.Mime)
	}
	if !bytes.Equal(decode(t, payload), gifBytes) {
		t.Error("小 GIF 该原样出去，动画才不会丢")
	}
}

func TestImageFromBytesRejectsUnknownFormat(t *testing.T) {
	if _, err := ImageFromBytes([]byte("这不是图片")); err == nil {
		t.Fatal("认不出的东西该报错")
	} else if !strings.Contains(err.Error(), "认不出") {
		t.Errorf("错误文案该说清是认不出格式，实际 %q", err)
	}
}

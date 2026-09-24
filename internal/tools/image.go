package tools

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"slices"

	"golang.org/x/image/draw"

	"github.com/reuben/go-harness/internal/types"

	// 只借它们的解码器，靠 init 注册进 image.Decode
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

// 图片这条路：嗅探出格式 -> 解码 -> 超过上限就缩放重编码 -> 塞进 ImageBlock。
// 尺寸和体积的上限照抄 pi（packages/coding-agent/src/utils/image-resize-core.ts）
const (
	maxImageWidth  = 2000
	maxImageHeight = 2000
	// 上限卡的是 base64 编码之后的字节数，不是原始字节数。pi 取 4.5MB 是为了压在
	// Anthropic 5MB 的 inline 上限下面，DeepSeek 没给公开数值，先照用
	maxImageBase64 = 4.5 * 1024 * 1024
	jpegQuality    = 80
)

var (
	pngMagic  = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	jpegMagic = []byte{0xff, 0xd8, 0xff}
)

// imageMime 从文件头认格式，认不出给空串。
// 认出来的格式都是 image.Decode 解得了的，不会出现「认了却解不开」
func imageMime(head []byte) string {
	switch {
	case bytes.HasPrefix(head, pngMagic):
		return "image/png"
	// 0xffd8fff7 是 JPEG-LS，标准库的 image/jpeg 解不了
	case bytes.HasPrefix(head, jpegMagic) && !bytes.HasPrefix(head, []byte{0xff, 0xd8, 0xff, 0xf7}):
		return "image/jpeg"
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return "image/gif"
	case len(head) >= 12 && bytes.HasPrefix(head, []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return "image/webp"
	case isBmp(head):
		return "image/bmp"
	}
	return ""
}

// isBmp 除了 "BM" 还要把头部里的几个数字对一遍。别的格式魔数都够长够怪，文本文件
// 撞不上；只有 "BM" 是两个字面量字母，以「BMW」开头的文本会被它骗过去
func isBmp(head []byte) bool {
	if len(head) < 26 || !bytes.HasPrefix(head, []byte("BM")) {
		return false
	}

	declaredSize := binary.LittleEndian.Uint32(head[2:])
	pixelOffset := binary.LittleEndian.Uint32(head[10:])
	dibSize := binary.LittleEndian.Uint32(head[14:])
	if declaredSize != 0 && declaredSize < 26 {
		return false
	}
	if pixelOffset < 14+dibSize {
		return false
	}
	if declaredSize != 0 && pixelOffset >= declaredSize {
		return false
	}

	var planes, bits uint16
	switch {
	case dibSize == 12:
		planes, bits = binary.LittleEndian.Uint16(head[22:]), binary.LittleEndian.Uint16(head[24:])
	case dibSize >= 40 && dibSize <= 124:
		if len(head) < 30 {
			return false
		}
		planes, bits = binary.LittleEndian.Uint16(head[26:]), binary.LittleEndian.Uint16(head[28:])
	default:
		return false
	}

	return planes == 1 && slices.Contains([]int{1, 4, 8, 16, 24, 32}, int(bits))
}

type imagePayload struct {
	Data   string // base64
	Mime   string
	Width  int
	Height int

	// Resized 为 true 时上面两个是缩放后的尺寸，原图尺寸在下面
	Resized        bool
	OriginalWidth  int
	OriginalHeight int
}

// processImage 把图片字节变成能直接放进 ImageBlock 的东西。
// 尺寸和体积都在上限内就原样用它（不重新编码，GIF 还能保住动画），超了就缩放重编码
func processImage(raw []byte, mime string) (imagePayload, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return imagePayload{}, fmt.Errorf("按 %s 解不开：%w", mime, err)
	}

	width, height := src.Bounds().Dx(), src.Bounds().Dy()
	if width <= maxImageWidth && height <= maxImageHeight && encodedLen(len(raw)) < maxImageBase64 {
		return imagePayload{
			Data: base64.StdEncoding.EncodeToString(raw), Mime: mime,
			Width: width, Height: height,
			OriginalWidth: width, OriginalHeight: height,
		}, nil
	}

	// 先缩到尺寸上限，还塞不进体积上限就按 0.75 继续缩，缩到 1x1 还不行才算没救
	width, height = fitWithin(width, height)
	for {
		data, outMime, ok, err := shrinkTo(src, width, height)
		if err != nil {
			return imagePayload{}, err
		}
		if ok {
			return imagePayload{
				Data: data, Mime: outMime, Width: width, Height: height,
				Resized:       true,
				OriginalWidth: src.Bounds().Dx(), OriginalHeight: src.Bounds().Dy(),
			}, nil
		}
		if width == 1 && height == 1 {
			return imagePayload{}, errors.New("缩到 1x1 还是超过体积上限")
		}
		width, height = max(1, width*3/4), max(1, height*3/4)
	}
}

// fitWithin 等比缩进尺寸上限里，只缩不放
func fitWithin(width, height int) (int, int) {
	if width > maxImageWidth {
		height = max(1, height*maxImageWidth/width)
		width = maxImageWidth
	}
	if height > maxImageHeight {
		width = max(1, width*maxImageHeight/height)
		height = maxImageHeight
	}
	return width, height
}

// shrinkTo 缩到指定尺寸，挑一种塞得进体积上限的编码：PNG 优先（无损、带透明），
// 塞不下再按 JPEG 逐级降质。都塞不下返回 ok=false，让调用方接着缩尺寸
func shrinkTo(src image.Image, width, height int) (data, mime string, ok bool, err error) {
	scaled := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, src.Bounds(), draw.Src, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, scaled); err != nil {
		return "", "", false, fmt.Errorf("编码 PNG 失败：%w", err)
	}
	if encodedLen(buf.Len()) < maxImageBase64 {
		return base64.StdEncoding.EncodeToString(buf.Bytes()), "image/png", true, nil
	}

	for _, quality := range []int{jpegQuality, 70, 55, 40} {
		buf.Reset()
		if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: quality}); err != nil {
			return "", "", false, fmt.Errorf("编码 JPEG 失败：%w", err)
		}
		if encodedLen(buf.Len()) < maxImageBase64 {
			return base64.StdEncoding.EncodeToString(buf.Bytes()), "image/jpeg", true, nil
		}
	}

	return "", "", false, nil
}

// encodedLen 是 n 个字节 base64 之后的长度，不用真去编码一遍
func encodedLen(n int) int {
	return base64.StdEncoding.EncodedLen(n)
}

// formatSize 是说明文案里给人看的体积
func formatSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d 字节", n)
	}
	return fmt.Sprintf("%d KB", n/1024)
}

// readImage 返回 [文本说明, 图片块]。图片不走行数和字节预算，那是给文本准备的，
// 图片的体积由 processImage 自己管
func readImage(r io.Reader, path, mime string) (types.ToolResult, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return types.ToolResult{}, fmt.Errorf("读 %s 失败：%w", path, err)
	}

	payload, err := processImage(raw, mime)
	if err != nil {
		return textResult(fmt.Sprintf("[%s 是 %s，但读不出图：%v]", path, mime, err)), nil
	}

	note := fmt.Sprintf("[%s 是图片：%s，%dx%d，%s]",
		path, payload.Mime, payload.Width, payload.Height, formatSize(len(raw)))
	if payload.Resized {
		note = fmt.Sprintf("[%s 是图片：%s，原图 %dx%d %s，超过上限，已缩到 %dx%d 并重编码；坐标乘 %.2f 对回原图]",
			path, payload.Mime, payload.OriginalWidth, payload.OriginalHeight, formatSize(len(raw)),
			payload.Width, payload.Height,
			float64(payload.OriginalWidth)/float64(payload.Width))
	}

	return types.ToolResult{ContentBlock: []types.ContentBlock{
		types.NewTextBlock(note),
		{Kind: types.ImageBlock, ImageMime: payload.Mime, ImageData: payload.Data},
	}}, nil
}

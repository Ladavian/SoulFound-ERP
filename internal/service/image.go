package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // 支持 webp 解码

	"icewine-erp/internal/model"
)

// 产品图片的处理参数。
const (
	MaxImageBytes   = 12 << 20 // 上传上限 12MB（手机原图通常 2-5MB）
	MaxImageSide    = 1280     // 长边缩到这个尺寸，收银台上够看清酒标
	ImageJPEGQual   = 82
	productImageDir = "products"
)

// UploadDir 图片存放目录。
func (s *Service) UploadDir() string {
	return filepath.Join(s.Cfg.DataDir, "uploads")
}

// SaveProductImage 保存产品图片。
//
// 手机原图动辄 3-5MB，直接存会在市集现场拖慢加载，
// 因此统一等比缩到长边 1280 再存；带透明的 PNG 保留 PNG 格式。
// 返回可直接放进 <img src> 的路径。
func (s *Service) SaveProductImage(ctx context.Context, productID int64, filename string, data []byte, user *model.User) (string, error) {
	if productID <= 0 {
		return "", UserErrf("产品不存在")
	}
	if len(data) == 0 {
		return "", UserErrf("没有收到图片内容")
	}
	if len(data) > MaxImageBytes {
		return "", UserErrf("图片太大了（上限 %d MB），请先压缩再上传", MaxImageBytes>>20)
	}

	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return "", err
	}
	if product == nil {
		return "", UserErrf("产品不存在")
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", UserErrf("这个文件不是有效的图片（支持 JPG / PNG / GIF / WebP）")
	}

	scaled := scaleDown(img, MaxImageSide)
	out, ext, err := encodeImage(scaled, format)
	if err != nil {
		return "", err
	}

	name := fmt.Sprintf("%d-%d%s", productID, time.Now().Unix(), ext)
	dir := filepath.Join(s.UploadDir(), productImageDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建图片目录失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), out, 0o644); err != nil {
		return "", fmt.Errorf("写入图片失败: %w", err)
	}

	publicPath := "/uploads/" + productImageDir + "/" + name
	previous := product.ImageURL
	product.ImageURL = publicPath
	if err := s.Store.UpdateProduct(ctx, product); err != nil {
		return "", err
	}
	s.removeUploaded(previous)
	return publicPath, nil
}

// ClearProductImage 清空产品图片。
func (s *Service) ClearProductImage(ctx context.Context, productID int64, user *model.User) error {
	product, err := s.Store.ProductByID(ctx, productID)
	if err != nil {
		return err
	}
	if product == nil {
		return UserErrf("产品不存在")
	}
	previous := product.ImageURL
	product.ImageURL = ""
	if err := s.Store.UpdateProduct(ctx, product); err != nil {
		return err
	}
	s.removeUploaded(previous)
	return nil
}

// removeUploaded 尽力删除被替换掉的旧图片，只允许删上传目录内的文件。
func (s *Service) removeUploaded(publicPath string) {
	if !strings.HasPrefix(publicPath, "/uploads/") {
		return // 外部 URL 不动
	}
	rel := strings.TrimPrefix(publicPath, "/uploads/")
	if strings.Contains(rel, "..") {
		return
	}
	full := filepath.Join(s.UploadDir(), filepath.FromSlash(rel))
	if strings.HasPrefix(full, filepath.Clean(s.UploadDir())) {
		_ = os.Remove(full)
	}
}

// scaleDown 等比缩小到长边不超过 max。
func scaleDown(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	ratio := float64(max) / float64(w)
	if h > w {
		ratio = float64(max) / float64(h)
	}
	nw, nh := int(float64(w)*ratio), int(float64(h)*ratio)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

// encodeImage 按原格式编码；不认识的原格式统一存 JPEG。
func encodeImage(img image.Image, format string) ([]byte, string, error) {
	var buf bytes.Buffer
	switch strings.ToLower(format) {
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", fmt.Errorf("编码 PNG 失败: %w", err)
		}
		return buf.Bytes(), ".png", nil
	default:
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: ImageJPEGQual}); err != nil {
			return nil, "", fmt.Errorf("编码 JPEG 失败: %w", err)
		}
		return buf.Bytes(), ".jpg", nil
	}
}

// ImageSizeText 把字节数转成便于阅读的文本。
func ImageSizeText(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

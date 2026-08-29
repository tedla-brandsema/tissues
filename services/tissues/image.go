package tissues

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
)

const (
	maxImageDimension = 12000
	maxImagePixels    = 12_000_000
	maxStoredEdge     = 1200
)

type processedImage struct {
	Name        string
	ContentType string
	Width       int
	Height      int
	Data        []byte
}

type imageProcessor func(string, []byte) (processedImage, error)

type imageDecoder struct {
	decodeConfig func(io.Reader) (image.Config, string, error)
	decode       func(io.Reader) (image.Image, string, error)
}

func canonicalAssetName(raw string) (string, string, error) {
	if raw == "" || raw != filepath.Base(raw) || strings.ContainsAny(raw, `/\`) || strings.Contains(raw, "..") {
		return "", "", fmt.Errorf("%w: invalid asset filename", ErrInvalid)
	}
	if first := raw[0]; !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return "", "", fmt.Errorf("%w: invalid asset filename", ErrInvalid)
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-') {
			return "", "", fmt.Errorf("%w: invalid asset filename", ErrInvalid)
		}
	}
	ext := strings.ToLower(filepath.Ext(raw))
	stem := strings.TrimSuffix(raw, filepath.Ext(raw))
	if stem == "" {
		return "", "", fmt.Errorf("%w: invalid asset filename", ErrInvalid)
	}
	format := ""
	switch ext {
	case ".jpg", ".jpeg":
		format, ext = "jpeg", ".jpg"
	case ".png":
		format = "png"
	default:
		return "", "", fmt.Errorf("%w: asset filename must end in .jpg, .jpeg, or .png", ErrInvalid)
	}
	name := strings.ToLower(stem) + ext
	if len(name) > 100 {
		return "", "", fmt.Errorf("%w: asset filename exceeds 100 bytes", ErrInvalid)
	}
	return name, format, nil
}

func processImage(filename string, data []byte) (processedImage, error) {
	return processImageWithDecoder(filename, data, imageDecoder{decodeConfig: image.DecodeConfig, decode: image.Decode})
}

func processImageWithDecoder(filename string, data []byte, decoder imageDecoder) (processedImage, error) {
	name, expectedFormat, err := canonicalAssetName(filename)
	if err != nil {
		return processedImage{}, err
	}
	config, format, err := decoder.decodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return processedImage{}, fmt.Errorf("%w: upload is not a valid JPEG or PNG", ErrInvalid)
	}
	if format != expectedFormat {
		return processedImage{}, fmt.Errorf("%w: filename extension does not match image format", ErrInvalid)
	}
	if err := validateImageDimensions(config.Width, config.Height); err != nil {
		return processedImage{}, err
	}
	decoded, decodedFormat, err := decoder.decode(bytes.NewReader(data))
	if err != nil || decodedFormat != format {
		return processedImage{}, fmt.Errorf("%w: malformed image data", ErrInvalid)
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if err := validateImageDimensions(width, height); err != nil {
		return processedImage{}, err
	}
	if width != config.Width || height != config.Height {
		return processedImage{}, fmt.Errorf("%w: decoded image dimensions do not match header", ErrInvalid)
	}
	current := decoded
	if max(width, height) > maxStoredEdge {
		width, height = fitDimensions(width, height, maxStoredEdge)
		current = resizeImage(current, width, height)
	}
	contentType := "image/png"
	var encoded []byte
	if format == "jpeg" {
		contentType = "image/jpeg"
		current, width, height, encoded, err = encodeJPEGWithinLimit(current, width, height)
	} else {
		current, width, height, encoded, err = encodePNGWithinLimit(current, width, height)
	}
	_ = current
	if err != nil {
		return processedImage{}, err
	}
	return processedImage{Name: name, ContentType: contentType, Width: width, Height: height, Data: encoded}, nil
}

func validateImageDimensions(width, height int) error {
	if width < 1 || height < 1 || width > maxImageDimension || height > maxImageDimension || int64(width)*int64(height) > maxImagePixels {
		return fmt.Errorf("%w: image dimensions exceed the decoded-image limit", ErrInvalid)
	}
	return nil
}

func fitDimensions(width, height, longest int) (int, int) {
	if width >= height {
		return longest, max(1, int((int64(height)*int64(longest))/int64(width)))
	}
	return max(1, int((int64(width)*int64(longest))/int64(height))), longest
}

func shrinkDimensions(width, height int) (int, int) {
	return max(1, width*85/100), max(1, height*85/100)
}

func resizeImage(source image.Image, width, height int) image.Image {
	destination := image.NewNRGBA(image.Rect(0, 0, width, height))
	xdraw.CatmullRom.Scale(destination, destination.Bounds(), source, source.Bounds(), draw.Src, nil)
	return destination
}

func encodeJPEGWithinLimit(current image.Image, width, height int) (image.Image, int, int, []byte, error) {
	qualities := [...]int{85, 80, 75, 70, 65}
	for {
		for _, quality := range qualities {
			var output bytes.Buffer
			if err := jpeg.Encode(&output, current, &jpeg.Options{Quality: quality}); err != nil {
				return nil, 0, 0, nil, fmt.Errorf("%w: encode JPEG: %v", ErrInternal, err)
			}
			if output.Len() <= MaxStoredBytes {
				return current, width, height, output.Bytes(), nil
			}
		}
		nextWidth, nextHeight := shrinkDimensions(width, height)
		if nextWidth == width && nextHeight == height {
			return nil, 0, 0, nil, fmt.Errorf("%w: cannot reduce JPEG below stored size limit", ErrInternal)
		}
		current, width, height = resizeImage(current, nextWidth, nextHeight), nextWidth, nextHeight
	}
}

func encodePNGWithinLimit(current image.Image, width, height int) (image.Image, int, int, []byte, error) {
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	for {
		var output bytes.Buffer
		if err := encoder.Encode(&output, current); err != nil {
			return nil, 0, 0, nil, fmt.Errorf("%w: encode PNG: %v", ErrInternal, err)
		}
		if output.Len() <= MaxStoredBytes {
			return current, width, height, output.Bytes(), nil
		}
		nextWidth, nextHeight := shrinkDimensions(width, height)
		if nextWidth == width && nextHeight == height {
			return nil, 0, 0, nil, fmt.Errorf("%w: cannot reduce PNG below stored size limit", ErrInternal)
		}
		current, width, height = resizeImage(current, nextWidth, nextHeight), nextWidth, nextHeight
	}
}

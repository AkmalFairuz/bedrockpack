package pack

import (
	"bytes"
	"image/png"
)

func compressPng(data []byte) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	var buf bytes.Buffer
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

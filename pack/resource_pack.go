package pack

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type ResourcePack struct {
	uuid      string
	files     map[string][]byte
	encrypted bool
}

func LoadResourcePack(path string) (*ResourcePack, error) {
	packBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return LoadResourcePackFromBytes(packBytes)
}

func LoadResourcePackFromBytes(packBytes []byte) (*ResourcePack, error) {
	rp := &ResourcePack{}

	if err := rp.load(packBytes); err != nil {
		return nil, err
	}

	return rp, nil
}

func (r *ResourcePack) load(packBytes []byte) error {
	reader, err := zip.NewReader(bytes.NewReader(packBytes), int64(len(packBytes)))
	if err != nil {
		return err
	}

	manifestFound := false
	basePath := ""

	r.files = map[string][]byte{}
	for _, fileInfo := range reader.File {
		file, err := fileInfo.Open()
		if err != nil {
			return err
		}
		content, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		if filepath.Base(fileInfo.Name) == "manifest.json" {
			var manifest map[string]any
			if err := json.Unmarshal(content, &manifest); err != nil {
				return err
			}
			if _, ok := manifest["header"]; !ok {
				return errors.New("manifest.json header not found")
			}
			if _, ok := manifest["header"].(map[string]any); !ok {
				return errors.New("manifest.json header is not a map[string]any")
			}
			if _, ok := manifest["header"].(map[string]any)["uuid"]; !ok {
				return errors.New("manifest.json header uuid not found")
			}
			packUuid, ok := manifest["header"].(map[string]any)["uuid"].(string)
			if !ok {
				return errors.New("manifest.json header uuid is not a string")
			}
			r.uuid = packUuid
			manifestFound = true
			basePath = filepath.Dir(fileInfo.Name)
		}

		if fileInfo.Name == "" {
			continue
		}
		r.files[fileInfo.Name] = content
	}

	if !manifestFound {
		return errors.New("manifest.json not found")
	}

	if basePath != "." {
		if !strings.HasSuffix(basePath, "/") {
			basePath += "/"
		}
		for fileName, fileBytes := range r.files {
			if strings.HasPrefix(fileName, basePath) {
				newFileName := strings.TrimPrefix(fileName, basePath)
				if newFileName == "" {
					continue
				}
				r.files[newFileName] = fileBytes
				delete(r.files, fileName)
			}
		}
	}

	if _, ok := r.files["contents.json"]; ok {
		r.encrypted = true
	}

	return nil
}

type contentJson struct {
	Content []contentJsonEntry `json:"content"`
}

type contentJsonEntry struct {
	Path string `json:"path"`
	Key  string `json:"key"`
}

func (r *ResourcePack) UUID() string {
	return r.uuid
}

func (r *ResourcePack) loadFile(fileName string) ([]byte, error) {
	fileBytes, ok := r.files[fileName]
	if !ok {
		return nil, fmt.Errorf("file %s not found", fileName)
	}
	return fileBytes, nil
}

func (r *ResourcePack) Decrypt(key []byte) error {
	if !r.encrypted {
		return nil
	}

	contentsBytes, err := r.loadFile("contents.json")
	if err != nil {
		return err
	}

	if len(contentsBytes) < 256 {
		return errors.New("contents.json bytes is less than 256 bytes")
	}

	contentRaw := contentsBytes[256:]
	decryptedContents, err := decryptCfb(contentRaw, key)
	if err != nil {
		return err
	}

	var contents contentJson
	if err := json.Unmarshal(decryptedContents, &contents); err != nil {
		return err
	}

	for _, content := range contents.Content {
		if content.Key == "" {
			continue
		}
		fileBytes, ok := r.files[content.Path]
		if !ok {
			continue
		}

		decryptedFileBytes, err := decryptCfb(fileBytes, []byte(content.Key))
		if err != nil {
			return fmt.Errorf("failed to decrypt %s file with key %s: %w", content.Path, content.Key, err)
		}

		r.files[content.Path] = decryptedFileBytes
	}

	delete(r.files, "contents.json")
	r.encrypted = false
	return nil
}

func (r *ResourcePack) CompressPNGFiles() error {
	if r.encrypted {
		return errors.New("pack is encrypted")
	}

	for fileName, fileBytes := range r.files {
		if !strings.HasSuffix(fileName, ".png") {
			continue
		}
		compressedBytes, err := compressPng(fileBytes)
		if err != nil {
			return err
		}
		if len(compressedBytes) < len(fileBytes) {
			r.files[fileName] = compressedBytes
			continue
		}
	}
	return nil
}

func (r *ResourcePack) MinifyJSONFiles() error {
	if r.encrypted {
		return errors.New("pack is encrypted")
	}

	re1 := regexp.MustCompile(`(?im)^\s+\/\/.*$`)
	re2 := regexp.MustCompile(`(?im)\/\/[^"\[\]]+$`)
	for fileName, fileBytes := range r.files {
		if !strings.HasSuffix(fileName, ".json") {
			continue
		}
		fileBytes = re1.ReplaceAll(fileBytes, []byte(""))
		fileBytes = re2.ReplaceAll(fileBytes, []byte(""))
		var data any
		if err := json.Unmarshal(fileBytes, &data); err != nil {
			return err
		}

		minifiedJSON, err := json.Marshal(data)
		if err != nil {
			return err
		}

		r.files[fileName] = minifiedJSON
	}

	return nil
}

func (r *ResourcePack) Encrypt(key []byte) error {
	if r.encrypted {
		return errors.New("unable to encrypt pack that already encrypted before")
	}

	contents := make([]contentJsonEntry, 0)

	for fileName, decryptedFileBytes := range r.files {
		if fileName == "manifest.json" || fileName == "pack_icon.png" {
			contents = append(contents, contentJsonEntry{
				Path: fileName,
			})
			continue
		}

		fileKey := GenerateKey()
		encryptedFileBytes, err := encryptCfb(decryptedFileBytes, fileKey)
		if err != nil {
			return err
		}
		r.files[fileName] = encryptedFileBytes

		contents = append(contents, contentJsonEntry{
			Path: fileName,
			Key:  string(fileKey),
		})
	}

	contentBytes, err := json.Marshal(contentJson{Content: contents})
	if err != nil {
		return err
	}

	contentBytes2 := bytes.NewBuffer(nil)
	contentBytes2.Write(make([]byte, 4))          // version
	contentBytes2.WriteString("\xfc\xb9\xcf\x9b") // type
	contentBytes2.Write(make([]byte, 8))          // padding
	contentBytes2.WriteString("\x24")             // separator
	contentBytes2.WriteString(r.uuid)             // uuid
	contentBytes2.Write(make([]byte, 256-contentBytes2.Len()))

	encryptedContentBytes, err := encryptCfb(contentBytes, key)
	if err != nil {
		return err
	}
	contentBytes2.Write(encryptedContentBytes)

	r.files["contents.json"] = contentBytes2.Bytes()
	return nil
}

func (r *ResourcePack) Save(path string) error {
	zipFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0777)
	if err != nil {
		return err
	}
	defer zipFile.Close()
	arc := zip.NewWriter(zipFile)

	for fileName, fileBytes := range r.files {
		w, err := arc.Create(fileName)
		if err != nil {
			return err
		}
		if _, err := w.Write(fileBytes); err != nil {
			return err
		}
	}

	return arc.Close()
}

func (r *ResourcePack) RegenerateUUID() error {
	manifestBytes, err := r.loadFile("manifest.json")
	if err != nil {
		return err
	}

	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return err
	}

	newPackUuid := uuid.New().String()

	if _, ok := manifest["header"]; !ok {
		return errors.New("manifest.json header not found")
	}

	if _, ok := manifest["header"].(map[string]any); !ok {
		return errors.New("manifest.json header is not a map[string]any")
	}

	manifest["header"].(map[string]any)["uuid"] = newPackUuid

	modules, ok := manifest["modules"]
	if ok {
		modules2 := modules.([]any)
		for _, module := range modules2 {
			if _, ok := module.(map[string]any); !ok {
				return errors.New("manifest.json module is not a map[string]any")
			}
			module.(map[string]any)["uuid"] = uuid.New().String()
		}
	}

	manifestBytes2, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	r.uuid = newPackUuid
	r.files["manifest.json"] = manifestBytes2
	return nil
}

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
		switch fileInfo.Name {
		case "contents.json":
			r.encrypted = true
		case "manifest.json":
			var manifest map[string]any
			if err := json.Unmarshal(content, &manifest); err != nil {
				return err
			}
			r.uuid = manifest["header"].(map[string]any)["uuid"].(string)
		}

		r.files[fileInfo.Name] = content
	}

	if _, ok := r.files["manifest.json"]; !ok {
		return errors.New("manifest.json not found")
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

func (r *ResourcePack) Decrypt(key []byte) error {
	if !r.encrypted {
		return nil
	}

	contentRaw := r.files["contents.json"][256:]
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
	var manifest map[string]any
	if err := json.Unmarshal(r.files["manifest.json"], &manifest); err != nil {
		return err
	}

	newPackUuid := uuid.New().String()
	manifest["header"].(map[string]any)["uuid"] = newPackUuid

	modules, ok := manifest["modules"]
	if ok {
		modules2 := modules.([]any)
		for _, module := range modules2 {
			module.(map[string]any)["uuid"] = uuid.New().String()
		}
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	r.uuid = newPackUuid
	r.files["manifest.json"] = manifestBytes
	return nil
}

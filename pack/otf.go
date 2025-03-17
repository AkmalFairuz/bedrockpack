package pack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/resource"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type OTF struct {
	log      *slog.Logger
	listener *minecraft.Listener
	orgName  string
	repoName string
	branch   string
	// pat is personal access token
	pat               string
	currentPackCommit string
	currentPackKey    string
	currentPack       *resource.Pack
}

const (
	otfUserAgent = "BedrockPack-OTF-Agent"
)

type OTFConfig struct {
	OrgName  string
	RepoName string
	Branch   string
	PAT      string
}

func (conf OTFConfig) New(log *slog.Logger) *OTF {
	return &OTF{
		log:      log.With("pack_repo", conf.OrgName+"/"+conf.RepoName+":"+conf.Branch),
		orgName:  conf.OrgName,
		repoName: conf.RepoName,
		branch:   conf.Branch,
		pat:      conf.PAT,
	}
}

// Start ...
func (o *OTF) Start() error {
	// try first tick
	if err := o.tick(); err != nil {
		return err
	}

	// then start the ticker
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ticker.C:
				if err := o.tick(); err != nil {
					o.log.Error("failed to tick", "error", err)
				}
			}
		}
	}()
	return nil
}

// tick ...
func (o *OTF) tick() error {
	commitHash, err := o.lastCommit(o.branch)
	if err != nil {
		return fmt.Errorf("failed to get last commit: %w", err)
	}

	if o.currentPackCommit == commitHash {
		return nil
	}

	if o.currentPackCommit != "" {
		o.log.Info("detected pack update, updating pack", "new_commit_hash", commitHash)
	}

	o.log.Info("downloading pack")
	packBytes, err := o.downloadRepoZip(commitHash)
	if err != nil {
		return fmt.Errorf("failed to download pack: %w", err)
	}

	o.log.Info("loading pack")
	pack, err := LoadResourcePackFromBytes(packBytes)
	if err != nil {
		return fmt.Errorf("failed to load pack: %w", err)
	}

	pack.DeleteFile("README.md")
	pack.DeleteFilesByPrefix(".git") // .github, .gitignore, etc.

	o.log.Info("minifying json files")
	if err := pack.MinifyJSONFiles(); err != nil {
		return fmt.Errorf("failed to minify JSON files: %w", err)
	}

	o.log.Info("compressing png files")
	if err := pack.CompressPNGFiles(); err != nil {
		return fmt.Errorf("failed to compress PNG files: %w", err)
	}

	packHash := pack.ComputeHash()
	packKey := GenerateKeyFromSeed(packHash)

	o.log.Info("generating uuid")
	if err := pack.RegenerateUUID(packHash); err != nil {
		return fmt.Errorf("failed to regenerate pack UUID: %w", err)
	}

	o.log.Info("encrypting pack", "pack_key", string(packKey))
	if err := pack.Encrypt(packKey); err != nil {
		return fmt.Errorf("failed to encrypt pack: %w", err)
	}

	o.log.Info("saving pack")
	compiledPackBytes, err := pack.SaveToBytes()
	if err != nil {
		return fmt.Errorf("failed to save pack: %w", err)
	}

	packBytes = nil // free memory

	o.log.Info("compiling pack")
	compiledPack, err := resource.Read(bytes.NewBuffer(compiledPackBytes))
	if err != nil {
		return fmt.Errorf("failed to read pack: %w", err)
	}

	compiledPackBytes = nil // free memory

	o.log.Info("pack updated", "pack_uuid", compiledPack.UUID().String())
	var prevPackUUID string
	if o.currentPack != nil {
		prevPackUUID = o.currentPack.UUID().String()
	}
	o.currentPackKey = string(packKey)
	o.currentPackCommit = commitHash
	o.currentPack = compiledPack

	if o.listener != nil {
		if prevPackUUID != "" {
			o.listener.RemoveResourcePack(prevPackUUID)
		}
		o.addPackToListener()
	}

	return nil
}

// SetListener ...
func (o *OTF) SetListener(listener *minecraft.Listener) {
	o.listener = listener
	o.addPackToListener()
}

// addPackToListener adds the pack to the listener.
func (o *OTF) addPackToListener() {
	if o.listener == nil || o.currentPack == nil {
		return
	}
	o.listener.AddResourcePack(o.currentPack.WithContentKey(o.currentPackKey))
}

// Listener ...
func (o *OTF) Listener() *minecraft.Listener {
	return o.listener
}

// initializeHeaders sets the necessary headers for the HTTP request.
func (o *OTF) initializeHeaders(req *http.Request) {
	if o.pat != "" {
		req.Header.Set("Authorization", "Bearer "+o.pat)
	}
	req.Header.Set("User-Agent", otfUserAgent)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// lastCommit fetches the latest commit hash from the given branch.
func (o *OTF) lastCommit(branch string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?sha=%s&per_page=1", o.orgName, o.repoName, branch)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	o.initializeHeaders(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned status: %d", resp.StatusCode)
	}

	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return "", err
	}

	if len(commits) == 0 {
		return "", fmt.Errorf("no commits found for branch: %s", branch)
	}

	return commits[0].SHA, nil
}

// downloadRepoZip downloads the entire repository as a .zip for a specific commit or branch.
func (o *OTF) downloadRepoZip(ref string) ([]byte, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/zipball/%s", o.orgName, o.repoName, ref)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	o.initializeHeaders(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download repository: %s (status: %d)", url, resp.StatusCode)
	}

	// Read the response body into a byte slice
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return data, nil
}

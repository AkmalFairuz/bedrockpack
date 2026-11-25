package stealer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/akmalfairuz/bedrockpack/pack"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/auth"
	"github.com/sandertv/gophertunnel/minecraft/resource"
	"golang.org/x/oauth2"
)

func DownloadAndDecrypt(address, network string) error {
	conn, normalizedAddress, err := connect(address, network)
	if err != nil {
		return err
	}
	defer conn.Close()

	fmt.Println("Getting resource pack information...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := conn.DoSpawnContext(ctx); err != nil {
		return fmt.Errorf("join server: %w", err)
	}

	_, err = downloadAndSavePacks(conn, normalizedAddress)
	return err
}

func connect(address, network string) (*minecraft.Conn, string, error) {
	network = strings.ToLower(strings.TrimSpace(network))
	if network == "" {
		network = "raknet"
	}
	if network != "raknet" && network != "nethernet" {
		return nil, "", fmt.Errorf("unsupported transport %q (use raknet or nethernet)", network)
	}
	normalizedAddress, err := normalizeServerAddress(address, network)
	if err != nil {
		return nil, "", err
	}
	src, err := getTokenSource()
	if err != nil {
		return nil, "", err
	}

	fmt.Printf("Connecting to %s over %s...\n", normalizedAddress, network)
	conn, err := (minecraft.Dialer{TokenSource: src}).DialTimeout(network, normalizedAddress, 5*time.Minute)
	if err != nil {
		return nil, "", fmt.Errorf("connect to %s: %w", normalizedAddress, err)
	}
	return conn, normalizedAddress, nil
}

func normalizeServerAddress(address, network string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", fmt.Errorf("server address is empty")
	}
	if network == "raknet" {
		if _, _, err := net.SplitHostPort(address); err == nil {
			return address, nil
		}
		if net.ParseIP(address) != nil || !strings.Contains(address, ":") {
			return net.JoinHostPort(address, "19132"), nil
		}
		return "", fmt.Errorf("invalid RakNet address %q; use host:port", address)
	}

	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "", fmt.Errorf("invalid NetherNet address %q; use https://host:port", address)
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "19132")
	}
	return u.String(), nil
}

func getTokenSource() (oauth2.TokenSource, error) {
	const cachePath = ".token_cache"
	if tokenBytes, err := os.ReadFile(cachePath); err == nil {
		_ = os.Chmod(cachePath, 0600)
		var token oauth2.Token
		if json.Unmarshal(tokenBytes, &token) == nil && token.RefreshToken != "" {
			fmt.Println("Using cached Xbox authentication")
			return auth.RefreshTokenSource(&token), nil
		}
	}

	token, err := auth.RequestLiveToken()
	if err != nil {
		return nil, fmt.Errorf("Xbox authentication: %w", err)
	}
	tokenBytes, err := json.Marshal(token)
	if err != nil {
		return nil, fmt.Errorf("encode authentication cache: %w", err)
	}
	if err := os.WriteFile(cachePath, tokenBytes, 0600); err != nil {
		return nil, fmt.Errorf("save authentication cache: %w", err)
	}
	return auth.RefreshTokenSource(token), nil
}

func downloadAndSavePacks(conn *minecraft.Conn, serverAddress string) (string, error) {
	packs := conn.ResourcePacks()
	if len(packs) == 0 {
		fmt.Println("Server did not provide any resource packs.")
		return "", nil
	}

	serverName := safeName(serverLabel(serverAddress))
	baseDir := filepath.Join("resource_packs", serverName)
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	runDir, err := os.MkdirTemp(baseDir, "session-")
	if err != nil {
		return "", fmt.Errorf("create output session: %w", err)
	}

	for i, rp := range packs {
		fmt.Printf("Downloading resource pack %s...\n", rp.Name())
		packBytes, err := downloadPack(rp)
		if err != nil {
			return runDir, fmt.Errorf("download resource pack %q: %w", rp.Name(), err)
		}
		pac, err := pack.LoadResourcePackFromBytes(packBytes)
		if err != nil {
			return runDir, fmt.Errorf("load resource pack %q: %w", rp.Name(), err)
		}
		if err := pac.Decrypt([]byte(rp.ContentKey())); err != nil {
			return runDir, fmt.Errorf("decrypt resource pack %q: %w", rp.Name(), err)
		}

		name := fmt.Sprintf("%02d_%s", i+1, safeName(rp.Name()))
		zipPath := filepath.Join(runDir, name+".zip")
		if err := pac.Save(zipPath); err != nil {
			return runDir, fmt.Errorf("save resource pack ZIP %q: %w", rp.Name(), err)
		}
		if rp.ContentKey() != "" {
			keyPath := filepath.Join(runDir, name+".key.txt")
			if err := os.WriteFile(keyPath, []byte(rp.ContentKey()), 0600); err != nil {
				return runDir, fmt.Errorf("save resource pack key %q: %w", rp.Name(), err)
			}
		}
		if err := pac.SaveDirectory(filepath.Join(runDir, name)); err != nil {
			return runDir, fmt.Errorf("extract resource pack %q: %w", rp.Name(), err)
		}
		fmt.Printf("Saved decrypted ZIP and files: %s\n", filepath.Join(runDir, name))
	}
	return runDir, nil
}

func serverLabel(address string) string {
	if u, err := url.Parse(address); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('_')
		default:
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "._")
	if name == "" {
		return "resource-pack"
	}
	return name
}

func downloadPack(rp *resource.Pack) ([]byte, error) {
	if rp.DownloadURL() != "" {
		req, err := http.NewRequest(http.MethodGet, rp.DownloadURL(), nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 3 * time.Minute}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("resource pack download returned HTTP %s", resp.Status)
		}
		return io.ReadAll(resp.Body)
	}

	buf := make([]byte, rp.Len())
	for off := 0; off < len(buf); {
		n, err := rp.ReadAt(buf[off:], int64(off))
		off += n
		if err != nil && !(err == io.EOF && off == len(buf)) {
			return nil, err
		}
		if n == 0 && off < len(buf) {
			return nil, io.ErrUnexpectedEOF
		}
	}
	return buf, nil
}

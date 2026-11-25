package stealer

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/resource"
)

const localProxyAddress = "0.0.0.0:19141"
const localNetherNetAddress = "0.0.0.0:26060"

type proxySession struct {
	serverAddress string
	world         *levelDBWorld
	packs         []*resource.Pack
	client        *minecraft.Conn
	upstream      *minecraft.Conn

	mu         sync.RWMutex
	capturing  bool
	chunkCount atomic.Uint64
}

func RunProxy(address, network string) error {
	upstream, normalizedAddress, err := connect(address, network)
	if err != nil {
		return err
	}
	defer upstream.Close()

	fmt.Println("Joining upstream server...")
	spawnCtx, cancelSpawn := context.WithTimeout(upstream.Context(), 10*time.Minute)
	err = upstream.DoSpawnContext(spawnCtx)
	cancelSpawn()
	if err != nil {
		return fmt.Errorf("join upstream server: %w", err)
	}

	packs := upstream.ResourcePacks()
	listenerGroup := new(minecraft.ListenerGroup)
	listenConfig := minecraft.ListenConfig{
		AuthenticationDisabled: true,
		MaximumPlayers:         1,
		ListenerGroup:          listenerGroup,
		ResourcePacks:          packs,
	}
	raknetListener, err := listenConfig.Listen("raknet", localProxyAddress)
	if err != nil {
		return fmt.Errorf("open local proxy on %s: %w", localProxyAddress, err)
	}
	defer raknetListener.Close()
	netherNetListener, err := listenConfig.Listen("nethernet", localNetherNetAddress)
	if err != nil {
		return fmt.Errorf("open NetherNet listener on %s: %w", localNetherNetAddress, err)
	}
	defer netherNetListener.Close()

	world, err := openLevelDBWorld(normalizedAddress, upstream.GameData(), upstream)
	if err != nil {
		return err
	}
	defer func() {
		if err := world.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Close LevelDB world: %v\n", err)
		}
	}()
	session := &proxySession{
		serverAddress: normalizedAddress,
		world:         world,
		packs:         packs,
		upstream:      upstream,
	}

	ctx, stop := signal.NotifyContext(upstream.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = raknetListener.Close()
		_ = netherNetListener.Close()
		_ = upstream.Close()
	}()

	fmt.Printf("RakNet listener: UDP %s -> %s\n", raknetListener.Addr(), normalizedAddress)
	fmt.Printf("NetherNet listener: HTTP/TCP %s -> %s\n", netherNetListener.Addr(), normalizedAddress)
	fmt.Println("Connect with RakNet on UDP 19141 or NetherNet on TCP 26060.")
	fmt.Println("Commands: .rplist, .chunk, .chunkdownload, .chunk off, .help")
	fmt.Println("Waiting for one Bedrock client...")
	type acceptResult struct {
		conn      net.Conn
		transport string
		err       error
	}
	accepts := make(chan acceptResult, 2)
	go func() {
		conn, err := raknetListener.Accept()
		accepts <- acceptResult{conn: conn, transport: "RakNet", err: err}
	}()
	go func() {
		conn, err := netherNetListener.Accept()
		accepts <- acceptResult{conn: conn, transport: "NetherNet", err: err}
	}()
	var accepted net.Conn
	for closed := 0; accepted == nil && closed < 2; {
		select {
		case <-ctx.Done():
			return nil
		case result := <-accepts:
			if result.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				fmt.Fprintf(os.Stderr, "%s accept stopped: %v\n", result.transport, result.err)
				closed++
				continue
			}
			accepted = result.conn
			fmt.Printf("Incoming client transport: %s\n", result.transport)
		}
	}
	if accepted == nil {
		return fmt.Errorf("both proxy listeners stopped before a client connected")
	}
	client, ok := accepted.(*minecraft.Conn)
	if !ok {
		_ = accepted.Close()
		return fmt.Errorf("local listener returned unexpected connection %T", accepted)
	}
	session.client = client
	defer client.Close()

	startCtx, cancelStart := context.WithTimeout(client.Context(), 10*time.Minute)
	err = client.StartGameContext(startCtx, upstream.GameData())
	cancelStart()
	if err != nil {
		return fmt.Errorf("start local client: %w", err)
	}

	fmt.Printf("Local client connected as %s\n", client.IdentityData().DisplayName)
	go session.readConsoleCommands(ctx)
	results := make(chan error, 2)
	go func() { results <- session.relay(ctx, client, upstream, true) }()
	go func() { results <- session.relay(ctx, upstream, client, false) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-results:
		stop()
		_ = client.Close()
		_ = upstream.Close()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return fmt.Errorf("proxy connection ended: %w", err)
		}
		return nil
	}
}

func (s *proxySession) relay(ctx context.Context, src, dst *minecraft.Conn, fromClient bool) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		pk, err := src.ReadPacket()
		if err != nil {
			return err
		}
		if fromClient {
			if command, ok := localCommand(pk); ok {
				s.handleCommand(command)
				if err := s.writeLocalMessage(s.commandReply(command)); err != nil {
					return err
				}
				continue
			}
		} else {
			s.observeServerPacket(pk)
		}
		if err := dst.WritePacket(pk); err != nil {
			return err
		}
	}
}

func localCommand(pk packet.Packet) (string, bool) {
	var command string
	switch pk := pk.(type) {
	case *packet.Text:
		if pk.TextType == packet.TextTypeChat {
			command = pk.Message
		}
	case *packet.CommandRequest:
		command = pk.CommandLine
	}
	command = strings.TrimSpace(command)
	return command, strings.HasPrefix(command, ".")
}

func (s *proxySession) readConsoleCommands(ctx context.Context) {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ".") {
			fmt.Println("Local commands must start with a dot; this input was not sent to chat.")
			continue
		}
		s.handleCommand(line)
	}
}

func (s *proxySession) handleCommand(command string) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "."))
	switch name {
	case "help":
		fmt.Println(".rplist - list upstream resource packs and content keys")
		fmt.Println(".chunk - toggle chunk capture")
		fmt.Println(".chunk on|off - enable or disable chunk capture")
		fmt.Println(".chunkdownload - start chunk capture")
	case "rplist":
		if len(s.packs) == 0 {
			fmt.Println("Upstream server did not provide resource packs.")
			return
		}
		fmt.Printf("Resource packs from %s:\n", s.serverAddress)
		for i, rp := range s.packs {
			key := rp.ContentKey()
			if key == "" {
				key = "(no key)"
			}
			fmt.Printf("%d. %s v%s UUID=%s key=%s\n", i+1, rp.Name(), rp.Version(), rp.UUID(), key)
		}
	case "chunk", "chunkdownload", "chunkdowloand":
		mode := ""
		if len(fields) > 1 {
			mode = strings.ToLower(fields[1])
		}
		s.mu.Lock()
		switch mode {
		case "on", "start", "download":
			s.capturing = true
		case "off", "stop":
			s.capturing = false
		default:
			if name == "chunk" {
				s.capturing = !s.capturing
			} else {
				s.capturing = true
			}
		}
		capturing := s.capturing
		s.mu.Unlock()
		if !capturing {
			s.world.ClearPending()
		}
		if capturing {
			fmt.Printf("World capture enabled: %s (LevelDB, %d chunk writes)\n", s.world.dir, s.chunkCount.Load())
		} else {
			fmt.Printf("World capture disabled (%d chunk writes)\n", s.chunkCount.Load())
		}
	default:
		fmt.Printf("Unknown local command %q; it was not sent to chat. Use .help.\n", fields[0])
	}
}

func (s *proxySession) commandReply(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "."))
	switch name {
	case "rplist":
		if len(s.packs) == 0 {
			return "No resource packs from upstream."
		}
		lines := []string{"§bUpstream resource packs:"}
		for i, rp := range s.packs {
			key := rp.ContentKey()
			if key == "" {
				key = "(no key)"
			}
			lines = append(lines, fmt.Sprintf("§f%d. %s v%s key=%s", i+1, rp.Name(), rp.Version(), key))
		}
		return strings.Join(lines, "\n")
	case "chunk", "chunkdownload", "chunkdowloand":
		s.mu.RLock()
		capturing := s.capturing
		s.mu.RUnlock()
		if capturing {
			return fmt.Sprintf("§aLevelDB world capture is on: %s", s.world.dir)
		}
		return "§eChunk capture is off. Use .chunkdownload to start."
	case "help":
		return "§bLocal commands: .rplist, .chunk, .chunkdownload, .chunk off, .help"
	default:
		return "§cUnknown local command; it was not sent to chat. Use .help."
	}
}

func (s *proxySession) writeLocalMessage(message string) error {
	if message == "" || s.client == nil {
		return nil
	}
	return s.client.WritePacket(&packet.Text{TextType: packet.TextTypeSystem, Message: message})
}

func (s *proxySession) observeServerPacket(pk packet.Packet) {
	switch pk := pk.(type) {
	case *packet.Text:
		logChat(pk)
	default:
		if s.isCapturing() {
			saved, err := s.world.HandlePacket(pk)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Save world to LevelDB: %v\n", err)
			} else if saved > 0 {
				s.chunkCount.Add(uint64(saved))
			}
		}
	}
}

func (s *proxySession) isCapturing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.capturing
}

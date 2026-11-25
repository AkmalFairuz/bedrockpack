package stealer

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	"github.com/sandertv/gophertunnel/minecraft/protocol/packet"
	"github.com/sandertv/gophertunnel/minecraft/text"
)

type playerState struct {
	name string
	pos  mgl32.Vec3
	yaw  float32
}

type botState struct {
	sync.RWMutex
	selfID   uint64
	position mgl32.Vec3
	yaw      float32
	pitch    float32
	tick     uint64
	players  map[uint64]playerState
	target   string
}

func RunBot(address, network, target string) error {
	conn, normalizedAddress, err := connect(address, network)
	if err != nil {
		return err
	}
	defer conn.Close()

	fmt.Println("Joining world...")
	spawnCtx, cancelSpawn := context.WithTimeout(conn.Context(), 10*time.Minute)
	defer cancelSpawn()
	if err := conn.DoSpawnContext(spawnCtx); err != nil {
		return fmt.Errorf("join server: %w", err)
	}
	outputDir, err := downloadAndSavePacks(conn, normalizedAddress)
	if err != nil {
		return err
	}
	if outputDir != "" {
		fmt.Printf("Resource packs saved in %s\n", outputDir)
	}

	identity := conn.IdentityData()
	fmt.Printf("Bot connected as %s\n", text.ANSI("§b"+identity.DisplayName+"§r"))
	if target == "" {
		fmt.Println("Chat logging is active. Press Ctrl+C to disconnect.")
	} else {
		fmt.Printf("Following %s only while visible in the bot's view. Press Ctrl+C to disconnect.\n", target)
	}

	state := &botState{
		selfID:   conn.GameData().EntityRuntimeID,
		position: conn.GameData().PlayerPosition,
		yaw:      conn.GameData().Yaw,
		pitch:    conn.GameData().Pitch,
		players:  make(map[uint64]playerState),
		target:   strings.TrimSpace(target),
	}
	ctx, stop := signal.NotifyContext(conn.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if state.target != "" {
		go followLoop(ctx.Done(), conn, state)
	}
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		pk, err := conn.ReadPacket()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read server packet: %w", err)
		}
		switch pk := pk.(type) {
		case *packet.Text:
			logChat(pk)
		case *packet.AddPlayer:
			state.Lock()
			state.players[pk.EntityRuntimeID] = playerState{name: pk.Username, pos: pk.Position, yaw: pk.Yaw}
			state.Unlock()
		case *packet.MovePlayer:
			state.Lock()
			if pk.EntityRuntimeID == state.selfID {
				state.position = pk.Position
				state.yaw = pk.Yaw
				state.pitch = pk.Pitch
			} else if player, ok := state.players[pk.EntityRuntimeID]; ok {
				player.pos = pk.Position
				player.yaw = pk.Yaw
				state.players[pk.EntityRuntimeID] = player
			}
			state.Unlock()
		case *packet.MoveActorAbsolute:
			state.Lock()
			if player, ok := state.players[pk.EntityRuntimeID]; ok {
				player.pos = pk.Position
				player.yaw = pk.Rotation[1]
				state.players[pk.EntityRuntimeID] = player
			}
			state.Unlock()
		case *packet.RemoveActor:
			state.Lock()
			delete(state.players, uint64(pk.EntityUniqueID))
			state.Unlock()
		case *packet.CorrectPlayerMovePrediction:
			if pk.PredictionType == packet.PredictionTypePlayer {
				state.Lock()
				state.position = pk.Position
				state.yaw = pk.Rotation[1]
				state.pitch = pk.Rotation[0]
				state.Unlock()
			}
		}
	}
}

func logChat(pk *packet.Text) {
	message := pk.Message
	if len(pk.Parameters) > 0 {
		message += " " + strings.Join(pk.Parameters, " ")
	}
	message = text.ANSI(message) + "\x1b[0m"
	switch pk.TextType {
	case packet.TextTypeChat, packet.TextTypeWhisper, packet.TextTypeAnnouncement,
		packet.TextTypeObjectWhisper, packet.TextTypeObjectAnnouncement:
		if pk.SourceName != "" {
			fmt.Printf("%s%s%s > %s\n", text.ANSI("§8[§b"), text.ANSI(pk.SourceName), text.ANSI("§8]§r"), message)
		} else {
			fmt.Println(message)
		}
	default:
		fmt.Println(message)
	}
}

func followLoop(ctxDone <-chan struct{}, conn *minecraft.Conn, state *botState) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctxDone:
			return
		case <-ticker.C:
			if err := followTick(conn, state); err != nil {
				fmt.Fprintf(os.Stderr, "Follow movement stopped: %v\n", err)
				return
			}
		}
	}
}

func followTick(conn *minecraft.Conn, state *botState) error {
	state.Lock()
	defer state.Unlock()

	var target *playerState
	for _, player := range state.players {
		if strings.EqualFold(player.name, state.target) {
			playerCopy := player
			target = &playerCopy
			break
		}
	}
	if target == nil || !inView(state.position, state.yaw, state.pitch, target.pos) {
		return nil
	}

	dx := target.pos[0] - state.position[0]
	dz := target.pos[2] - state.position[2]
	distance := float32(math.Hypot(float64(dx), float64(dz)))
	if distance < 2.5 {
		return nil
	}

	state.yaw = float32(math.Atan2(float64(-dx), float64(dz)) * 180 / math.Pi)
	step := float32(0.20)
	if distance < step {
		step = distance
	}
	delta := mgl32.Vec3{dx / distance * step, 0, dz / distance * step}
	state.position = state.position.Add(delta)
	state.tick++
	flags := protocolInputFlags()
	forward := mgl32.Vec2{0, 1}
	yawRad := float64(state.yaw) * math.Pi / 180
	pitchRad := float64(state.pitch) * math.Pi / 180
	look := mgl32.Vec3{
		float32(-math.Sin(yawRad) * math.Cos(pitchRad)),
		float32(-math.Sin(pitchRad)),
		float32(math.Cos(yawRad) * math.Cos(pitchRad)),
	}
	return conn.WritePacket(&packet.PlayerAuthInput{
		Pitch: state.pitch, Yaw: state.yaw,
		Position: state.position, MoveVector: forward, HeadYaw: state.yaw,
		InputData: flags, InputMode: packet.InputModeMouse,
		PlayMode: packet.PlayModeNormal, InteractionModel: packet.InteractionModelCrosshair,
		InteractPitch: state.pitch, InteractYaw: state.yaw, Tick: state.tick,
		Delta: delta, AnalogueMoveVector: forward, CameraOrientation: look, RawMoveVector: forward,
	})
}

func protocolInputFlags() protocol.InputFlags {
	flags := protocol.NewInputFlags(packet.InputFlagCount)
	flags.Set(packet.InputFlagUp)
	return flags
}

func inView(position mgl32.Vec3, yaw, pitch float32, target mgl32.Vec3) bool {
	delta := target.Sub(position)
	distance := delta.Len()
	if distance < 0.001 || distance > 64 {
		return false
	}
	yawRad := float64(yaw) * math.Pi / 180
	pitchRad := float64(pitch) * math.Pi / 180
	forward := mgl32.Vec3{
		float32(-math.Sin(yawRad) * math.Cos(pitchRad)),
		float32(-math.Sin(pitchRad)),
		float32(math.Cos(yawRad) * math.Cos(pitchRad)),
	}
	return forward.Dot(delta.Mul(1/distance)) >= 0.5
}

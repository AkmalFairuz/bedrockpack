package stealer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/df-mc/dragonfly/server/world/mcdb"
	"github.com/df-mc/goleveldb/leveldb"
	"github.com/sandertv/gophertunnel/minecraft"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
	gtpacket "github.com/sandertv/gophertunnel/minecraft/protocol/packet"
)

type worldChunkKey struct {
	dimension int32
	x         int32
	z         int32
}

type partialWorldChunk struct {
	dimension     world.Dimension
	rangeY        cube.Range
	biomes        []byte
	subChunks     [][]byte
	blockEntities []chunk.BlockEntity
}

type pendingLevelChunk struct {
	packet *gtpacket.LevelChunk
	key    worldChunkKey
	blobs  map[uint64][]byte
}

type pendingSubChunk struct {
	key      worldChunkKey
	y        int32
	entities []byte
}

type levelDBWorld struct {
	mu sync.Mutex

	dir                 string
	db                  *mcdb.DB
	blocks              world.BlockRegistry
	useBlockHashes      bool
	upstream            *minecraft.Conn
	partial             map[worldChunkKey]*partialWorldChunk
	pendingChunks       map[*pendingLevelChunk]struct{}
	pendingChunksByHash map[uint64]map[*pendingLevelChunk]struct{}
	pendingSubChunks    map[uint64][]pendingSubChunk
}

func openLevelDBWorld(serverAddress string, gameData minecraft.GameData, upstream *minecraft.Conn) (*levelDBWorld, error) {
	registry, err := newLevelDBBlockRegistry(gameData.CustomBlocks)
	if err != nil {
		return nil, err
	}

	dir := filepath.Join("worlds", worldName(serverAddress))
	db, err := (mcdb.Config{Blocks: registry}).Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open Bedrock LevelDB world %q: %w", dir, err)
	}
	settings := db.Settings()
	settings.Name = worldName(serverAddress)
	db.SaveSettings(settings)

	return &levelDBWorld{
		dir:                 dir,
		db:                  db,
		blocks:              registry,
		useBlockHashes:      gameData.UseBlockNetworkIDHashes,
		upstream:            upstream,
		partial:             make(map[worldChunkKey]*partialWorldChunk),
		pendingChunks:       make(map[*pendingLevelChunk]struct{}),
		pendingChunksByHash: make(map[uint64]map[*pendingLevelChunk]struct{}),
		pendingSubChunks:    make(map[uint64][]pendingSubChunk),
	}, nil
}

type levelDBBlockRegistry struct {
	world.BlockRegistry
	customHashes map[uint32]uint32
}

func (r *levelDBBlockRegistry) HashToRuntimeID(hash uint32) (uint32, bool) {
	if runtimeID, ok := r.customHashes[hash]; ok {
		return runtimeID, true
	}
	return r.BlockRegistry.HashToRuntimeID(hash)
}

func newLevelDBBlockRegistry(entries []protocol.BlockEntry) (world.BlockRegistry, error) {
	world.DefaultBlockRegistry.Finalize()
	registry := world.NewBlockRegistry()
	stateRuntimeIDs := make(map[string]uint32)
	knownBlockNames := make(map[string]struct{})
	for runtimeID, block := range world.DefaultBlockRegistry.Blocks() {
		name, properties := block.EncodeBlock()
		stateRuntimeIDs[blockStateKey(name, properties)] = uint32(runtimeID)
		knownBlockNames[name] = struct{}{}
	}
	nextRuntimeID := uint32(len(world.DefaultBlockRegistry.Blocks()))
	customHashes := make(map[uint32]uint32, len(entries))
	registerState := func(name string, sourceProperties map[string]any) error {
		hash, err := levelDBNetworkBlockHash(name, sourceProperties)
		if err != nil {
			return fmt.Errorf("hash custom block %q: %w", name, err)
		}
		properties, err := levelDBBlockProperties(name, sourceProperties)
		if err != nil {
			return err
		}
		key := blockStateKey(name, properties)
		runtimeID, ok := stateRuntimeIDs[key]
		if !ok {
			registry.RegisterBlockState(world.BlockState{Name: name, Properties: properties})
			runtimeID = nextRuntimeID
			nextRuntimeID++
			stateRuntimeIDs[key] = runtimeID
		}
		customHashes[hash] = runtimeID
		return nil
	}
	for _, entry := range entries {
		if isBlockComponentDefinition(entry.Properties) {
			if _, vanilla := knownBlockNames[entry.Name]; vanilla {
				continue
			}
			states, err := blockDefinitionStates(entry.Properties)
			if err != nil {
				return nil, fmt.Errorf("read states for custom block %q: %w", entry.Name, err)
			}
			for _, properties := range states {
				if err := registerState(entry.Name, properties); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := registerState(entry.Name, entry.Properties); err != nil {
			return nil, err
		}
	}
	registry.Finalize()
	return &levelDBBlockRegistry{BlockRegistry: registry, customHashes: customHashes}, nil
}

func isBlockComponentDefinition(properties map[string]any) bool {
	for _, key := range []string{"components", "molangVersion", "menu_category", "vanilla_block_data", "permutations"} {
		if _, ok := properties[key]; ok {
			return true
		}
	}
	_, hasProperties := properties["properties"]
	return hasProperties
}

func blockDefinitionStates(definition map[string]any) ([]map[string]any, error) {
	rawProperties, ok := definition["properties"]
	if !ok {
		return []map[string]any{{}}, nil
	}
	definitions, ok := anySlice(rawProperties)
	if !ok {
		return nil, fmt.Errorf("properties definition has type %T, expected a list", rawProperties)
	}
	states := []map[string]any{{}}
	for _, raw := range definitions {
		property, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("property definition has type %T, expected a compound", raw)
		}
		name, ok := property["name"].(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("property definition is missing its name")
		}
		values, ok := anySlice(property["enum"])
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("property %q has no enum values", name)
		}
		if len(states) > (1<<16)/len(values) {
			return nil, fmt.Errorf("property combinations exceed the 65536 state limit")
		}
		next := make([]map[string]any, 0, len(states)*len(values))
		for _, state := range states {
			for _, value := range values {
				copy := make(map[string]any, len(state)+1)
				for key, existing := range state {
					copy[key] = existing
				}
				copy[name] = value
				next = append(next, copy)
			}
		}
		states = next
	}
	return states, nil
}

func anySlice(value any) ([]any, bool) {
	switch value := value.(type) {
	case []any:
		return value, true
	case []string:
		items := make([]any, len(value))
		for i := range value {
			items[i] = value[i]
		}
		return items, true
	case []map[string]any:
		items := make([]any, len(value))
		for i := range value {
			items[i] = value[i]
		}
		return items, true
	default:
		return nil, false
	}
}

func levelDBBlockProperties(name string, properties map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(properties))
	for key, value := range properties {
		switch value.(type) {
		case bool, uint8, int32, string:
			result[key] = value
		case int8:
			result[key] = uint8(value.(int8))
		case int16:
			result[key] = int32(value.(int16))
		case uint16:
			result[key] = int32(value.(uint16))
		case uint32:
			result[key] = int32(value.(uint32))
		default:
			if key == "blockTags" {
				continue
			}
			return nil, fmt.Errorf("custom block %q has unsupported property %q of type %T", name, key, value)
		}
	}
	return result, nil
}

func blockStateKey(name string, properties map[string]any) string {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%d:%s", len(name), name)
	for _, key := range keys {
		fmt.Fprintf(&b, "%d:%s:%T:%#v", len(key), key, properties[key], properties[key])
	}
	return b.String()
}

func levelDBNetworkBlockHash(name string, properties map[string]any) (uint32, error) {
	data := []byte{10, 0, 0}
	var err error
	data, err = appendHashTagString(data, "name", name)
	if err != nil {
		return 0, err
	}
	data = append(data, 10)
	data, err = appendHashString(data, "states")
	if err != nil {
		return 0, err
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		data, err = appendNetworkBlockProperty(data, key, properties[key])
		if err != nil {
			return 0, err
		}
	}
	data = append(data, 0, 0)
	h := fnv.New32a()
	_, _ = h.Write(data)
	return h.Sum32(), nil
}

func appendHashTagString(data []byte, name, value string) ([]byte, error) {
	data = append(data, 8)
	var err error
	data, err = appendHashString(data, name)
	if err != nil {
		return nil, err
	}
	return appendHashString(data, value)
}

func appendHashString(data []byte, value string) ([]byte, error) {
	if len(value) > int(^uint16(0)) {
		return nil, fmt.Errorf("NBT string is too long: %d bytes", len(value))
	}
	data = binary.LittleEndian.AppendUint16(data, uint16(len(value)))
	return append(data, value...), nil
}

func appendNetworkBlockProperty(data []byte, key string, value any) ([]byte, error) {
	var err error
	switch value := value.(type) {
	case bool:
		data = append(data, 1)
		data, err = appendHashString(data, key)
		if err != nil {
			return nil, err
		}
		if value {
			data = append(data, 1)
		} else {
			data = append(data, 0)
		}
		return data, nil
	case uint8:
		data = append(data, 1)
		data, err = appendHashString(data, key)
		return append(data, value), err
	case int8:
		data = append(data, 1)
		data, err = appendHashString(data, key)
		return append(data, byte(value)), err
	case int16:
		data = append(data, 2)
		data, err = appendHashString(data, key)
		return binary.LittleEndian.AppendUint16(data, uint16(value)), err
	case uint16:
		data = append(data, 2)
		data, err = appendHashString(data, key)
		return binary.LittleEndian.AppendUint16(data, value), err
	case int32:
		data = append(data, 3)
		data, err = appendHashString(data, key)
		return binary.LittleEndian.AppendUint32(data, uint32(value)), err
	case uint32:
		data = append(data, 3)
		data, err = appendHashString(data, key)
		return binary.LittleEndian.AppendUint32(data, value), err
	case string:
		return appendHashTagString(data, key, value)
	case []any:
		return appendNetworkStringList(data, key, value)
	case []string:
		items := make([]any, len(value))
		for i := range value {
			items[i] = value[i]
		}
		return appendNetworkStringList(data, key, items)
	default:
		return nil, fmt.Errorf("unsupported custom block property %q of type %T", key, value)
	}
}

func appendNetworkStringList(data []byte, key string, values []any) ([]byte, error) {
	data = append(data, 9)
	var err error
	data, err = appendHashString(data, key)
	if err != nil {
		return nil, err
	}
	data = append(data, 8)
	data = binary.LittleEndian.AppendUint32(data, uint32(len(values)))
	for _, value := range values {
		item, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("blockTags list contains unsupported value %T", value)
		}
		data, err = appendHashString(data, item)
		if err != nil {
			return nil, err
		}
	}
	return data, nil
}

func (w *levelDBWorld) Close() error {
	if w == nil || w.db == nil {
		return nil
	}
	return w.db.Close()
}

func (w *levelDBWorld) HandlePacket(pk gtpacket.Packet) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch pk := pk.(type) {
	case *gtpacket.LevelChunk:
		if err := w.handleLevelChunk(pk); err != nil {
			return 0, err
		}
		if pk.CacheEnabled {
			return 0, nil
		}
		return 1, nil
	case *gtpacket.SubChunk:
		return w.handleSubChunks(pk)
	case *gtpacket.ClientCacheMissResponse:
		return w.handleCacheBlobs(pk)
	default:
		return 0, nil
	}
}

func (w *levelDBWorld) ClearPending() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pendingChunks = make(map[*pendingLevelChunk]struct{})
	w.pendingChunksByHash = make(map[uint64]map[*pendingLevelChunk]struct{})
	w.pendingSubChunks = make(map[uint64][]pendingSubChunk)
}

func (w *levelDBWorld) handleLevelChunk(pk *gtpacket.LevelChunk) error {
	key := worldChunkKey{dimension: pk.Dimension, x: pk.Position[0], z: pk.Position[1]}
	if pk.CacheEnabled {
		if len(pk.BlobHashes) == 0 || (pk.SubChunkCount > 0 && len(pk.BlobHashes) < int(pk.SubChunkCount)+1) {
			return fmt.Errorf("chunk %v has an incomplete blob hash list", key)
		}
		pending := &pendingLevelChunk{
			packet: cloneLevelChunk(pk),
			key:    key,
			blobs:  make(map[uint64][]byte, len(pk.BlobHashes)),
		}
		w.pendingChunks[pending] = struct{}{}
		for _, hash := range pk.BlobHashes {
			if w.pendingChunksByHash[hash] == nil {
				w.pendingChunksByHash[hash] = make(map[*pendingLevelChunk]struct{})
			}
			w.pendingChunksByHash[hash][pending] = struct{}{}
		}
		return w.requestBlobs(pk.BlobHashes)
	}

	if pk.SubChunkCount == 0 {
		return w.storeBiomeOnlyChunk(key, pk.RawPayload)
	}
	return w.storeNetworkChunk(key, pk.SubChunkCount, pk.RawPayload)
}

func (w *levelDBWorld) handleSubChunks(pk *gtpacket.SubChunk) (int, error) {
	var requests []uint64
	saved := 0
	for _, entry := range pk.SubChunkEntries {
		x := pk.Position[0] + int32(entry.Offset[0])
		y := pk.Position[1] + int32(entry.Offset[1])
		z := pk.Position[2] + int32(entry.Offset[2])
		key := worldChunkKey{dimension: pk.Dimension, x: x, z: z}
		if entry.Result == protocol.SubChunkResultChunkNotFound || entry.Result == protocol.SubChunkResultInvalidDimension || entry.Result == protocol.SubChunkResultPlayerNotFound || entry.Result == protocol.SubChunkResultIndexOutOfBounds {
			continue
		}
		if hash, ok := entry.BlobHash.Value(); pk.CacheEnabled && ok {
			entities, _ := entry.RawPayload.Value()
			w.pendingSubChunks[hash] = append(w.pendingSubChunks[hash], pendingSubChunk{key: key, y: y, entities: append([]byte(nil), entities...)})
			requests = append(requests, hash)
			continue
		}

		var raw []byte
		if entry.Result != protocol.SubChunkResultSuccessAllAir {
			value, ok := entry.RawPayload.Value()
			if !ok {
				continue
			}
			length, err := networkSubChunkLength(value)
			if err != nil {
				return saved, fmt.Errorf("decode sub-chunk at %v/%d: %w", key, y, err)
			}
			raw = value[:length]
			if err := w.storePartialSubChunk(key, y, raw, value[length:]); err != nil {
				return saved, err
			}
			saved++
			continue
		}
		if err := w.storePartialSubChunk(key, y, raw, nil); err != nil {
			return saved, err
		}
		saved++
	}
	return saved, w.requestBlobs(requests)
}

func (w *levelDBWorld) handleCacheBlobs(pk *gtpacket.ClientCacheMissResponse) (int, error) {
	saved := 0
	for _, blob := range pk.Blobs {
		for pending := range w.pendingChunksByHash[blob.Hash] {
			pending.blobs[blob.Hash] = append([]byte(nil), blob.Payload...)
		}
		for _, pending := range w.pendingSubChunks[blob.Hash] {
			if err := w.storePartialSubChunk(pending.key, pending.y, blob.Payload, pending.entities); err != nil {
				return saved, err
			}
			saved++
		}
		delete(w.pendingSubChunks, blob.Hash)
	}

	for pending := range w.pendingChunks {
		complete := true
		for _, hash := range pending.packet.BlobHashes {
			if _, ok := pending.blobs[hash]; !ok {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		if pending.packet.SubChunkCount == 0 {
			biomes := pending.blobs[pending.packet.BlobHashes[0]]
			err := w.storeBiomeOnlyChunk(pending.key, append(append([]byte(nil), biomes...), pending.packet.RawPayload...))
			if err != nil {
				return saved, err
			}
		} else {
			payload := make([]byte, 0, len(pending.packet.RawPayload)+1024)
			for i := 0; i < int(pending.packet.SubChunkCount); i++ {
				blob := pending.blobs[pending.packet.BlobHashes[i]]
				payload = append(payload, blob...)
			}
			payload = append(payload, pending.blobs[pending.packet.BlobHashes[pending.packet.SubChunkCount]]...)
			payload = append(payload, pending.packet.RawPayload...)
			if err := w.storeNetworkChunk(pending.key, pending.packet.SubChunkCount, payload); err != nil {
				return saved, err
			}
		}
		saved++
		w.removePendingChunk(pending)
	}
	return saved, nil
}

func (w *levelDBWorld) storeBiomeOnlyChunk(key worldChunkKey, payload []byte) error {
	dimension, r, err := levelDimension(key.dimension)
	if err != nil {
		return err
	}
	data, err := w.rewriteChunkPayload(payload, 0)
	if err != nil {
		return err
	}
	c, err := chunk.NetworkDecode(w.blocks, data, 0, r)
	if err != nil {
		return fmt.Errorf("decode biomes for chunk %v: %w", key, err)
	}
	partial, err := w.partialChunk(key, dimension, r)
	if err != nil {
		return err
	}
	partial.biomes = chunk.EncodeBiomes(c, chunk.NetworkEncoding)
	if offset, err := networkChunkDataLength(data, 0, r); err == nil && offset < len(data) {
		entities, supported, err := decodeChunkBlockEntities(data[offset:])
		if err != nil {
			return fmt.Errorf("decode block entities for chunk %v: %w", key, err)
		}
		if supported {
			partial.blockEntities = entities
		}
	}
	return w.savePartial(key, partial)
}

func (w *levelDBWorld) storeNetworkChunk(key worldChunkKey, count uint32, payload []byte) error {
	dimension, r, err := levelDimension(key.dimension)
	if err != nil {
		return err
	}
	data, err := w.rewriteChunkPayload(payload, int(count))
	if err != nil {
		return fmt.Errorf("translate chunk %v: %w", key, err)
	}
	c, err := chunk.NetworkDecode(w.blocks, data, int(count), r)
	if err != nil {
		return fmt.Errorf("decode chunk %v: %w", key, err)
	}
	metadataOffset, err := networkChunkDataLength(data, int(count), r)
	if err != nil {
		return fmt.Errorf("read chunk metadata %v: %w", key, err)
	}
	entities, supported, err := decodeChunkBlockEntities(data[metadataOffset:])
	if err != nil {
		return fmt.Errorf("decode block entities for chunk %v: %w", key, err)
	}
	column := &chunk.Column{Chunk: c}
	if supported {
		column.BlockEntities = entities
	}
	if err := w.db.StoreColumn(world.ChunkPos{key.x, key.z}, dimension, column); err != nil {
		return fmt.Errorf("write chunk %v to LevelDB: %w", key, err)
	}
	encoded := chunk.Encode(c, chunk.NetworkEncoding)
	w.partial[key] = &partialWorldChunk{dimension: dimension, rangeY: r, biomes: encoded.Biomes, subChunks: encoded.SubChunks, blockEntities: column.BlockEntities}
	return nil
}

func (w *levelDBWorld) storePartialSubChunk(key worldChunkKey, y int32, raw, entityPayload []byte) error {
	dimension, r, err := levelDimension(key.dimension)
	if err != nil {
		return err
	}
	minY, maxY := int32(r[0]>>4), int32(r[1]>>4)
	if y < minY || y > maxY {
		return fmt.Errorf("sub-chunk Y %d outside dimension %d range [%d,%d]", y, key.dimension, minY, maxY)
	}
	partial, err := w.partialChunk(key, dimension, r)
	if err != nil {
		return err
	}
	index := int(y - minY)
	if len(raw) == 0 {
		raw = emptyNetworkSubChunk(y)
	}
	translated, err := w.rewriteSubChunk(raw)
	if err != nil {
		return fmt.Errorf("translate sub-chunk %v/%d: %w", key, y, err)
	}
	partial.subChunks[index] = translated
	entities, err := decodeSubChunkBlockEntities(entityPayload)
	if err != nil {
		return fmt.Errorf("decode block entities for sub-chunk %v/%d: %w", key, y, err)
	}
	partial.blockEntities = replaceSubChunkBlockEntities(partial.blockEntities, entities, y)
	return w.savePartial(key, partial)
}

func (w *levelDBWorld) partialChunk(key worldChunkKey, dimension world.Dimension, r cube.Range) (*partialWorldChunk, error) {
	partial := w.partial[key]
	if partial != nil {
		return partial, nil
	}
	if column, err := w.db.LoadColumn(world.ChunkPos{key.x, key.z}, dimension); err == nil {
		encoded := chunk.Encode(column.Chunk, chunk.NetworkEncoding)
		partial = &partialWorldChunk{dimension: dimension, rangeY: r, biomes: encoded.Biomes, subChunks: encoded.SubChunks, blockEntities: column.BlockEntities}
		w.partial[key] = partial
		return partial, nil
	} else if !errors.Is(err, leveldb.ErrNotFound) {
		return nil, fmt.Errorf("load existing chunk %v: %w", key, err)
	}
	blank := chunk.New(w.blocks, r)
	partial = &partialWorldChunk{
		dimension: dimension,
		rangeY:    r,
		biomes:    chunk.EncodeBiomes(blank, chunk.NetworkEncoding),
		subChunks: make([][]byte, (r.Height()>>4)+1),
	}
	w.partial[key] = partial
	return partial, nil
}

func (w *levelDBWorld) savePartial(key worldChunkKey, partial *partialWorldChunk) error {
	var payload []byte
	minY := partial.rangeY[0] >> 4
	for i, subChunk := range partial.subChunks {
		if len(subChunk) == 0 {
			subChunk = emptyNetworkSubChunk(int32(minY + i))
		}
		payload = append(payload, subChunk...)
	}
	payload = append(payload, partial.biomes...)
	c, err := chunk.NetworkDecode(w.blocks, payload, len(partial.subChunks), partial.rangeY)
	if err != nil {
		return fmt.Errorf("decode partial chunk %v: %w", key, err)
	}
	if err := w.db.StoreColumn(world.ChunkPos{key.x, key.z}, partial.dimension, &chunk.Column{Chunk: c, BlockEntities: partial.blockEntities}); err != nil {
		return fmt.Errorf("write partial chunk %v to LevelDB: %w", key, err)
	}
	return nil
}

func (w *levelDBWorld) requestBlobs(hashes []uint64) error {
	if len(hashes) == 0 {
		return nil
	}
	return w.upstream.WritePacket(&gtpacket.ClientCacheBlobStatus{MissHashes: hashes})
}

func (w *levelDBWorld) removePendingChunk(pending *pendingLevelChunk) {
	delete(w.pendingChunks, pending)
	for _, hash := range pending.packet.BlobHashes {
		delete(w.pendingChunksByHash[hash], pending)
		if len(w.pendingChunksByHash[hash]) == 0 {
			delete(w.pendingChunksByHash, hash)
		}
	}
}

func (w *levelDBWorld) rewriteChunkPayload(payload []byte, subChunkCount int) ([]byte, error) {
	if !w.useBlockHashes || subChunkCount == 0 {
		return payload, nil
	}
	var out bytes.Buffer
	offset := 0
	for i := 0; i < subChunkCount; i++ {
		if offset >= len(payload) {
			return nil, fmt.Errorf("missing sub-chunk %d of %d", i+1, subChunkCount)
		}
		raw := payload[offset:]
		length, err := networkSubChunkLength(raw)
		if err != nil {
			return nil, fmt.Errorf("read sub-chunk %d: %w", i+1, err)
		}
		translated, err := w.rewriteSubChunk(raw[:length])
		if err != nil {
			return nil, err
		}
		out.Write(translated)
		offset += length
	}
	out.Write(payload[offset:])
	return out.Bytes(), nil
}

func (w *levelDBWorld) rewriteSubChunk(raw []byte) ([]byte, error) {
	if !w.useBlockHashes {
		length, err := networkSubChunkLength(raw)
		if err != nil {
			return nil, err
		}
		return append([]byte(nil), raw[:length]...), nil
	}
	length, err := networkSubChunkLength(raw)
	if err != nil {
		return nil, err
	}
	raw = raw[:length]
	var out bytes.Buffer
	offset := 0
	readByte := func() (byte, error) {
		if offset >= len(raw) {
			return 0, fmt.Errorf("unexpected end of sub-chunk")
		}
		v := raw[offset]
		offset++
		return v, nil
	}
	version, err := readByte()
	if err != nil {
		return nil, err
	}
	storageCount := byte(1)
	if version == 8 || version == 9 {
		storageCount, err = readByte()
		if err != nil {
			return nil, err
		}
		if version == 9 {
			if _, err = readByte(); err != nil {
				return nil, err
			}
		}
	} else if version != 1 {
		return nil, fmt.Errorf("unsupported sub-chunk version %d", version)
	}
	out.Write(raw[:offset])
	for storage := byte(0); storage < storageCount; storage++ {
		header, err := readByte()
		if err != nil {
			return nil, err
		}
		bits := int(header >> 1)
		if bits == 0x7f {
			return nil, fmt.Errorf("sub-chunk block palette references a previous palette")
		}
		words, err := networkStorageWordCount(bits)
		if err != nil {
			return nil, err
		}
		byteCount := words * 4
		if offset+byteCount > len(raw) {
			return nil, fmt.Errorf("truncated block indices")
		}
		out.WriteByte(header)
		out.Write(raw[offset : offset+byteCount])
		offset += byteCount
		paletteCount := int32(1)
		if bits != 0 {
			paletteCount, err = readNetworkVarint32(raw, &offset)
			if err != nil {
				return nil, err
			}
			if paletteCount <= 0 || paletteCount > 1<<16 {
				return nil, fmt.Errorf("invalid palette size %d", paletteCount)
			}
			if err := protocol.WriteVarint32(&out, paletteCount); err != nil {
				return nil, err
			}
		}
		for i := int32(0); i < paletteCount; i++ {
			hash, err := readNetworkVarint32(raw, &offset)
			if err != nil {
				return nil, fmt.Errorf("read block hash: %w", err)
			}
			runtimeID, ok := w.blocks.HashToRuntimeID(uint32(hash))
			if !ok {
				return nil, fmt.Errorf("unknown block network hash 0x%08x", uint32(hash))
			}
			if err := protocol.WriteVarint32(&out, int32(runtimeID)); err != nil {
				return nil, err
			}
		}
	}
	out.Write(raw[offset:])
	return out.Bytes(), nil
}

func networkSubChunkLength(data []byte) (int, error) {
	offset := 0
	if len(data) == 0 {
		return 0, fmt.Errorf("empty sub-chunk payload")
	}
	version := data[offset]
	offset++
	storageCount := byte(1)
	switch version {
	case 1:
	case 8, 9:
		if offset >= len(data) {
			return 0, fmt.Errorf("missing storage count")
		}
		storageCount = data[offset]
		offset++
		if version == 9 {
			if offset >= len(data) {
				return 0, fmt.Errorf("missing sub-chunk Y")
			}
			offset++
		}
	default:
		return 0, fmt.Errorf("unsupported sub-chunk version %d", version)
	}
	for storage := byte(0); storage < storageCount; storage++ {
		if offset >= len(data) {
			return 0, fmt.Errorf("missing storage header")
		}
		bits := int(data[offset] >> 1)
		offset++
		words, err := networkStorageWordCount(bits)
		if err != nil {
			return 0, err
		}
		byteCount := words * 4
		if offset+byteCount > len(data) {
			return 0, fmt.Errorf("truncated block indices")
		}
		offset += byteCount
		paletteCount := int32(1)
		if bits != 0 {
			paletteCount, err = readNetworkVarint32(data, &offset)
			if err != nil {
				return 0, fmt.Errorf("read palette size: %w", err)
			}
			if paletteCount <= 0 || paletteCount > 1<<16 {
				return 0, fmt.Errorf("invalid palette size %d", paletteCount)
			}
		}
		for i := int32(0); i < paletteCount; i++ {
			if _, err := readNetworkVarint32(data, &offset); err != nil {
				return 0, fmt.Errorf("read palette entry: %w", err)
			}
		}
	}
	return offset, nil
}

func networkChunkDataLength(data []byte, subChunkCount int, r cube.Range) (int, error) {
	offset := 0
	for i := 0; i < subChunkCount; i++ {
		length, err := networkSubChunkLength(data[offset:])
		if err != nil {
			return 0, fmt.Errorf("read sub-chunk %d: %w", i+1, err)
		}
		offset += length
	}
	biomeCount := (r.Height() >> 4) + 1
	for i := 0; i < biomeCount; i++ {
		if offset >= len(data) {
			return 0, fmt.Errorf("missing biome palette %d of %d", i+1, biomeCount)
		}
		header := data[offset]
		offset++
		bits := int(header >> 1)
		if bits == 0x7f {
			continue
		}
		words, err := networkStorageWordCount(bits)
		if err != nil {
			return 0, fmt.Errorf("biome palette: %w", err)
		}
		if offset+words*4 > len(data) {
			return 0, fmt.Errorf("truncated biome indices")
		}
		offset += words * 4
		paletteCount := int32(1)
		if bits != 0 {
			paletteCount, err = readNetworkVarint32(data, &offset)
			if err != nil {
				return 0, fmt.Errorf("read biome palette size: %w", err)
			}
			if paletteCount <= 0 || paletteCount > 1<<16 {
				return 0, fmt.Errorf("invalid biome palette size %d", paletteCount)
			}
		}
		for j := int32(0); j < paletteCount; j++ {
			if _, err := readNetworkVarint32(data, &offset); err != nil {
				return 0, fmt.Errorf("read biome palette entry: %w", err)
			}
		}
	}
	return offset, nil
}

func decodeChunkBlockEntities(data []byte) ([]chunk.BlockEntity, bool, error) {
	if len(data) == 0 {
		return nil, true, nil
	}
	if data[0] != 0 {
		return nil, false, nil
	}
	entities, err := decodeSubChunkBlockEntities(data[1:])
	return entities, true, err
}

func decodeSubChunkBlockEntities(data []byte) ([]chunk.BlockEntity, error) {
	reader := bytes.NewReader(data)
	decoder := nbt.NewDecoderWithEncoding(reader, nbt.NetworkLittleEndian)
	decoder.AllowZero = true
	var entities []chunk.BlockEntity
	for reader.Len() > 0 {
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if len(value) == 0 {
			continue
		}
		x, xOK := value["x"].(int32)
		y, yOK := value["y"].(int32)
		z, zOK := value["z"].(int32)
		if !xOK || !yOK || !zOK {
			return nil, fmt.Errorf("block entity is missing integer x, y, or z")
		}
		entities = append(entities, chunk.BlockEntity{Pos: cube.Pos{int(x), int(y), int(z)}, Data: value})
	}
	return entities, nil
}

func replaceSubChunkBlockEntities(existing, replacement []chunk.BlockEntity, subChunkY int32) []chunk.BlockEntity {
	result := make([]chunk.BlockEntity, 0, len(existing)+len(replacement))
	for _, entity := range existing {
		if int32(entity.Pos[1]>>4) != subChunkY {
			result = append(result, entity)
		}
	}
	return append(result, replacement...)
}

func networkStorageWordCount(bits int) (int, error) {
	if bits == 0 {
		return 0, nil
	}
	if bits < 1 || bits > 16 || bits == 7 || bits == 9 || bits == 10 || bits == 11 || bits == 12 || bits == 13 || bits == 14 || bits == 15 {
		return 0, fmt.Errorf("unsupported palette bit size %d", bits)
	}
	perWord := 32 / bits
	words := 4096 / perWord
	if bits == 3 || bits == 5 || bits == 6 {
		words++
	}
	return words, nil
}

func readNetworkVarint32(data []byte, offset *int) (int32, error) {
	reader := bytes.NewReader(data[*offset:])
	var value int32
	if err := protocol.Varint32(reader, &value); err != nil {
		return 0, err
	}
	*offset += len(data[*offset:]) - reader.Len()
	return value, nil
}

func emptyNetworkSubChunk(y int32) []byte {
	return []byte{chunk.SubChunkVersion, 0, byte(int8(y))}
}

func levelDimension(id int32) (world.Dimension, cube.Range, error) {
	dimension, ok := world.DimensionByID(int(id))
	if !ok {
		return nil, cube.Range{}, fmt.Errorf("dimension %d is not supported for LevelDB export", id)
	}
	return dimension, dimension.Range(), nil
}

func cloneLevelChunk(pk *gtpacket.LevelChunk) *gtpacket.LevelChunk {
	copy := *pk
	copy.BlobHashes = append([]uint64(nil), pk.BlobHashes...)
	copy.RawPayload = append([]byte(nil), pk.RawPayload...)
	return &copy
}

func worldName(address string) string {
	name := address
	if parsed, err := url.Parse(address); err == nil && parsed.Hostname() != "" {
		name = parsed.Hostname()
		if parsed.Port() != "" {
			name += "_" + parsed.Port()
		}
	} else if host, port, err := net.SplitHostPort(address); err == nil {
		name = host + "_" + port
	}
	return strings.Trim(safeName(name), "._")
}

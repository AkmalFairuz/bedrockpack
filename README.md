# bedrockpack
A Minecraft Bedrock tool for decrypting, encrypting, managing, and stealing resource packs!

## Download
https://github.com/AkmalFairuz/bedrockpack/releases/

## Usage

#### Decrypt the resource pack using the given key
```
bedrockpack decrypt <path to resource pack> <key>
```

#### Encrypt the resource pack using either the given key or a generated key
- Automatically minify all the JSON files
- Automatically regenerate the UUID of the resource pack in manifest.json
- Automatically compress .png files with the best compression level.
```
bedrockpack encrypt <path to resource pack> <key (optional)>
```

#### Steal the resource pack from a server and decrypt it automatically
- Xbox authentication is required.
```
bedrockpack steal <server ip:port>
```

#### Interactive client
Run the program without arguments to choose between downloading resource packs and joining as a bot:
```
./bedrockpack
```

The download mode saves each pack as a decrypted ZIP and as an extracted directory under `resource_packs/`.
Encrypted pack keys are written to a separate `.key.txt` file with owner-only permissions.

The bot mode logs server chat to the console with Minecraft colours. It can optionally follow a player while that
player is within a 120-degree view cone and 64 blocks away. Movement is direct and does not pathfind around blocks.

Choose either RakNet or NetherNet in the menu. RakNet accepts `host` or `host:port` (default port `19132`).
NetherNet accepts `https://host:port` and defaults to port `19132` when omitted. The server must have NetherNet
transport configured and reachable at that URL.

Command-line forms are also available:
```
bedrockpack download <server address> [raknet|nethernet]
bedrockpack bot <server address> [raknet|nethernet] [player]
bedrockpack proxy <server address> [raknet|nethernet]
```

#### Local proxy and world capture
Choose `3. Прокси` from the menu and enter the upstream server address. The proxy listens on RakNet UDP `19141` and
NetherNet signaling HTTP/TCP `26060` on all interfaces. Connect to the host's public IP or domain using the matching
transport and port. Allow inbound UDP `19141` and TCP `26060` in the host firewall. NetherNet carries game data over
WebRTC after signaling, so its ICE media path must also be reachable. Only one client is accepted across both listeners.
The proxy forwards the upstream server's resource packs to the local client.

Dot commands are handled locally and are never forwarded as chat:
- `.rplist` prints the upstream resource pack names, versions, UUIDs, and content keys.
- `.chunk` toggles capture, `.chunk on` starts it, and `.chunk off` stops it.
- `.chunkdownload` starts capture (`.chunkdowloand` is accepted as an alias).
- `.help` lists the commands.

Commands can be entered in the proxy terminal or in the local client's chat. Dot commands are never forwarded to the
upstream chat. Once capture is enabled, chunks and sub-chunk responses are decoded and written to a Bedrock world
under `worlds/<server>/` as they arrive. The world contains `level.dat` and a `db/` LevelDB database and can be
copied into the Bedrock worlds directory after the proxy is stopped.

## On The Fly Resource Pack

This feature allows servers to use resource packs from a GitHub repository. The server will monitor the repository for changes and automatically update the resource pack on the server. This is useful for servers that want to use a custom resource pack without having to manually upload it to the server and restarting the server.

See [example/otf.go](example/otf.go)

### Features
- UUID are automatically generated based on the pack content
- Automatically encrypt the pack and the encryption key are generated based on the pack content
- Automatically minify all the JSON files
- Automatically compress .png files with the best compression level.

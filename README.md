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

## On The Fly Resource Pack

This feature allows servers to use resource packs from a GitHub repository. The server will monitor the repository for changes and automatically update the resource pack on the server. This is useful for servers that want to use a custom resource pack without having to manually upload it to the server and restarting the server.

See [example/otf.go](example/otf.go)

### Features
- UUID are automatically generated based on the pack content
- Automatically encrypt the pack and the encryption key are generated based on the pack content
- Automatically minify all the JSON files
- Automatically compress .png files with the best compression level.

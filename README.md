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

This feature allows servers to download a resource pack from a GitHub repository and apply it.

See [example/otf.go](example/otf.go)

### Features 
- UUID are automatically generated based on the pack content
- Automatically encrypt the pack and the encryption key are generated based on the pack content
- Automatically minify all the JSON files
- Automatically compress .png files with the best compression level.
